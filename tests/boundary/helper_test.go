// Package boundary 是 db-mysql 的边界测试面（G-20 production 判据①）。
//
// 四类覆盖：异常输入 / 并发 / 权限不足 / 资源耗尽。
//
// 环境口径：本机可能没有 MySQL 服务。所有用例都通过「假客户端脚本」走插件的真实
// 执行面——真实 os/exec 子进程、真实管道、真实退出码与 stderr；脚本本身不连接任何
// 数据库，用例与断言也不假装连过真库。真实 MySQL 的 L2 验证在 tests/e2e。
package boundary

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pengtang1024/kore-plugin-db-mysql/internal/mysqlop"
	"github.com/pengtang1024/kore/pkg/pluginapi"
)

// fakeMySQLTemplate 是假 mysql 客户端的脚本模板：__COUNT__ 由用例注入。
//
// 它按 argv / stdin 的形状返回确定性 TSV，覆盖插件用到的四类调用：
// 元数据查询（-e）、行数查询（stdin COUNT）、sql_mode 探测（stdin）、DDL（stdin 无输出）。
const fakeMySQLTemplate = `#!/bin/sh
# 边界测试假客户端：真实子进程，不连接任何数据库。
count=__COUNT__
sql=""
from_e=0
skip_names=0
while [ "$#" -gt 0 ]; do
	case "$1" in
		-e)
			from_e=1
			sql=$2
			shift 2
			;;
		--skip-column-names)
			skip_names=1
			shift
			;;
		*)
			shift
			;;
	esac
done
if [ "$from_e" -eq 0 ]; then
	sql=$(cat)
fi
upper=$(printf '%s' "$sql" | tr 'a-z' 'A-Z')
case "$upper" in
	*"@@SESSION.SQL_MODE"*)
		printf 'STRICT_TRANS_TABLES\n'
		;;
	*"SHOW DATABASES"*)
		printf 'Database\nkore_a\nkore_b\nmysql\n'
		;;
	*"COUNT("*)
		printf '%s\n' "$count"
		;;
	*"SELECT 1, VERSION()"*)
		printf '1\t8.0.99-fake\n'
		;;
	*"SELECT "*)
		tag=$(printf '%s' "$sql" | sed -n "s/.*'\([^']*\)'.*/\1/p")
		if [ "$skip_names" -eq 1 ]; then
			printf '%s\n' "$tag"
		else
			printf 'marker\n%s\n' "$tag"
		fi
		;;
esac
exit 0
`

// denyMySQLScript 模拟「MySQL 用户无权限访问某库」：客户端 stderr 报 1044 并失败。
const denyMySQLScript = `#!/bin/sh
printf "ERROR 1044 (42000): Access denied for user 'app'@'localhost' to database 'secret_db'\n" >&2
exit 1
`

// deniedChangeScript 模拟「连接账号无 ALTER USER 权限」：COUNT/sql_mode 正常，改密语句报 1142。
const deniedChangeScript = `#!/bin/sh
sql=""
from_e=0
while [ "$#" -gt 0 ]; do
	case "$1" in
		-e)
			from_e=1
			sql=$2
			shift 2
			;;
		*)
			shift
			;;
	esac
done
if [ "$from_e" -eq 0 ]; then
	sql=$(cat)
fi
upper=$(printf '%s' "$sql" | tr 'a-z' 'A-Z')
case "$upper" in
	*"@@SESSION.SQL_MODE"*)
		printf 'STRICT_TRANS_TABLES\n'
		;;
	*"COUNT("*)
		printf '1\n'
		;;
	*"ALTER USER"*)
		printf "ERROR 1142 (42000): ALTER command denied to user 'app'@'localhost' for table 'user'\n" >&2
		exit 1
		;;
esac
exit 0
`

// hugeDumpScript 是假 mysqldump：向 stdout 写 256 KiB，用于导出路径的资源上限注入。
// 用 exec 让 dd 顶替 shell：单进程写入，承载 stderr 的 copy goroutine 不会因子进程残留而挂住。
const hugeDumpScript = `#!/bin/sh
exec dd if=/dev/zero bs=1024 count=256 2>/dev/null
`

// hugeLineScript 是假 mysql：输出一条 2 MiB 的单行结果，用于单行上限的资源防护断言。
// exec dd 让 dd 顶替 shell（单进程、无管道）：cancel 直接命中写端，不会残留孙进程。
const hugeLineScript = `#!/bin/sh
printf 'marker\n'
exec dd if=/dev/zero bs=1024 count=2048 2>/dev/null
`

// writeScript 把脚本写到测试临时目录并置可执行位（真实 fork/exec 需要）。
func writeScript(t *testing.T, dir, name, body string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatalf("写测试脚本 %s 失败: %v", name, err)
	}
	return path
}

// fakeMySQL 生成指定 COUNT 返回值的假 mysql 客户端路径。
// count=0 模拟「库/用户不存在」，count=1 模拟「已存在」；其它调用与 count 无关。
func fakeMySQL(t *testing.T, count int) string {
	t.Helper()
	dir := t.TempDir()
	body := strings.ReplaceAll(fakeMySQLTemplate, "__COUNT__", string(rune('0'+count)))
	return writeScript(t, dir, "mysql", body)
}

// boundaryConfig 构造带一个实例的配置；bin/dumpBin 指向假客户端，exportDir 为导出白名单根。
func boundaryConfig(bin, dumpBin, exportDir string) mysqlop.Config {
	return mysqlop.Config{
		Bin:       bin,
		DumpBin:   dumpBin,
		Timeout:   10 * time.Second,
		Admin:     true,
		ExportDir: exportDir,
		Instances: []mysqlop.InstanceConfig{
			{Name: "primary", Host: "127.0.0.1", Port: 3306, User: "root"},
		},
	}
}

// tripwireExecutor 是「校验阶段不应触达执行面」的探针：任何调用都计数并返回错误。
type tripwireExecutor struct {
	calls int
}

func (e *tripwireExecutor) hit() error {
	e.calls++
	return errors.New("校验阶段不应触达执行面")
}

func (e *tripwireExecutor) Instances([]mysqlop.InstanceConfig) ([]mysqlop.InstanceStatus, error) {
	return nil, e.hit()
}

func (e *tripwireExecutor) Databases(mysqlop.InstanceConfig, bool) ([]string, error) {
	return nil, e.hit()
}

func (e *tripwireExecutor) Query(mysqlop.InstanceConfig, string, int) (mysqlop.QueryResult, error) {
	return mysqlop.QueryResult{}, e.hit()
}

func (e *tripwireExecutor) Status(mysqlop.InstanceConfig) (mysqlop.StatusResult, error) {
	return mysqlop.StatusResult{}, e.hit()
}

func (e *tripwireExecutor) CreateDatabase(mysqlop.InstanceConfig, string, string, string) (bool, error) {
	return false, e.hit()
}

func (e *tripwireExecutor) DropDatabase(mysqlop.InstanceConfig, string) (bool, error) {
	return false, e.hit()
}

func (e *tripwireExecutor) CreateUser(mysqlop.InstanceConfig, string, string, string) (bool, error) {
	return false, e.hit()
}

func (e *tripwireExecutor) ChangePassword(mysqlop.InstanceConfig, string, string, string) (bool, error) {
	return false, e.hit()
}

func (e *tripwireExecutor) DropUser(mysqlop.InstanceConfig, string, string) (bool, error) {
	return false, e.hit()
}

func (e *tripwireExecutor) Grant(mysqlop.InstanceConfig, string, string, string, []string) error {
	return e.hit()
}

func (e *tripwireExecutor) Export(mysqlop.InstanceConfig, string, []string, string) (int64, error) {
	return 0, e.hit()
}

func (e *tripwireExecutor) Import(mysqlop.InstanceConfig, string, string) error {
	return e.hit()
}

// requireCode 断言错误码与消息关键词（错误码必须精确匹配，禁止「泛化失败」）。
func requireCode(t *testing.T, perr *pluginapi.Error, code string, wants ...string) {
	t.Helper()
	if perr == nil {
		t.Fatalf("期望错误码 %s，实际未返回错误", code)
	}
	if perr.Code != code {
		t.Fatalf("错误码=%q，期望 %q（message=%q）", perr.Code, code, perr.Message)
	}
	for _, want := range wants {
		if !strings.Contains(perr.Message, want) {
			t.Fatalf("错误信息 %q 未包含 %q", perr.Message, want)
		}
	}
}

// decodeResult 把结果解到目标结构，失败即终止用例。
func decodeResult(t *testing.T, raw json.RawMessage, target any) {
	t.Helper()
	if err := json.Unmarshal(raw, target); err != nil {
		t.Fatalf("结果不是合法 JSON: %v（原值=%s）", err, raw)
	}
}
