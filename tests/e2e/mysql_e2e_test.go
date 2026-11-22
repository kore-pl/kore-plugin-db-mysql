// Package e2e 对真实 MySQL 容器做端到端验证：实例探测、库列表、只读查询、写操作拒绝。
//
// 默认跳过；需要先启动容器并导出配置：
//
//	docker run -d --name kore-mysql-test -e MYSQL_ROOT_PASSWORD=test123 -p 13306:3306 mysql:8
//	KORE_MYSQL_E2E=1 \
//	KORE_MYSQL_INSTANCES='[{"name":"primary","host":"127.0.0.1","port":13306,"user":"root","password_env":"KORE_MYSQL_PASSWORD"}]' \
//	KORE_MYSQL_PASSWORD=test123 \
//	go test -timeout 120s -v ./tests/e2e
package e2e_test

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"

	"github.com/pengtang1024/kore-plugin-db-mysql/internal/mysqlop"
	"github.com/pengtang1024/kore/pkg/pluginapi"
)

// TestDockerMySQL 四步真实 E2E：instances / databases / query / 写操作拒绝。
func TestDockerMySQL(t *testing.T) {
	if os.Getenv("KORE_MYSQL_E2E") != "1" {
		t.Skip("设置 KORE_MYSQL_E2E=1 并启动 kore-mysql-test 容器后运行")
	}
	cfg, err := mysqlop.LoadConfig()
	if err != nil {
		t.Fatalf("加载实例配置失败: %v", err)
	}
	if len(cfg.Instances) == 0 {
		t.Fatal("KORE_MYSQL_INSTANCES 未配置任何实例")
	}
	instance := cfg.Instances[0]
	secret := os.Getenv(instance.PasswordEnv)
	t.Logf("实例: name=%s host=%s port=%d user=%s", instance.Name, instance.Host, instance.Port, instance.User)

	seedDatabase(t, instance)

	// 步骤 1：db-mysql.instances 显示 reachable + version。
	raw := call(t, cfg, "db-mysql.instances", map[string]any{"instance": instance.Name})
	logJSON(t, "STEP1 db-mysql.instances", raw)
	assertNoSecret(t, raw, secret)
	var instances mysqlop.InstancesResult
	mustUnmarshal(t, raw, &instances)
	if instances.Count != 1 || len(instances.Instances) != 1 {
		t.Fatalf("实例数=%d，期望 1", instances.Count)
	}
	if !instances.Instances[0].Reachable {
		t.Fatalf("实例不可达: %+v", instances.Instances[0])
	}
	if instances.Instances[0].Version == "" {
		t.Fatalf("未返回版本: %+v", instances.Instances[0])
	}
	if instances.Instances[0].Error != "" {
		t.Fatalf("可达实例不应带 error: %+v", instances.Instances[0])
	}

	// 步骤 2：db-mysql.databases 列出库并过滤系统库。
	raw = call(t, cfg, "db-mysql.databases", map[string]any{"instance": instance.Name})
	logJSON(t, "STEP2 db-mysql.databases", raw)
	assertNoSecret(t, raw, secret)
	var databases mysqlop.DatabasesResult
	mustUnmarshal(t, raw, &databases)
	if !contains(databases.Databases, "kore_e2e") {
		t.Fatalf("库列表未包含 kore_e2e: %v", databases.Databases)
	}
	for _, system := range []string{"information_schema", "mysql", "performance_schema", "sys"} {
		if contains(databases.Databases, system) {
			t.Fatalf("默认结果不应包含系统库 %s: %v", system, databases.Databases)
		}
	}

	// 步骤 3：db-mysql.query "SELECT 1 AS n" → [{n:1}]。
	raw = call(t, cfg, "db-mysql.query", map[string]any{"instance": instance.Name, "sql": "SELECT 1 AS n"})
	logJSON(t, "STEP3 db-mysql.query SELECT 1 AS n", raw)
	assertNoSecret(t, raw, secret)
	var query mysqlop.QueryResult
	mustUnmarshal(t, raw, &query)
	if query.RowCount != 1 || len(query.Rows) != 1 {
		t.Fatalf("行数=%d，期望 1", query.RowCount)
	}
	var firstRow map[string]any
	mustUnmarshal(t, query.Rows[0], &firstRow)
	if firstRow["n"] != float64(1) {
		t.Fatalf("首行=%v，期望 {n:1}", firstRow)
	}

	// 步骤 4：写操作被 E_INVALID 拒绝。
	result, perr := mysqlop.Call("db-mysql.query", mustJSON(t, map[string]any{
		"instance": instance.Name, "sql": "DROP DATABASE mysql",
	}), cfg, nil)
	if perr == nil {
		t.Fatalf("写操作未被拒绝，结果=%s", result)
	}
	if perr.Code != pluginapi.E_INVALID {
		t.Fatalf("写操作错误码=%q，期望 E_INVALID（message=%q）", perr.Code, perr.Message)
	}
	if result != nil {
		t.Fatalf("写操作被拒时不应返回结果: %s", result)
	}
	assertNoSecret(t, []byte(perr.Message), secret)
	t.Logf("STEP4 写操作被拒: %s: %s", perr.Code, perr.Message)

	// 附加：NULL 与字段内制表符、结果截断、status 指标、多语句拒绝。
	raw = call(t, cfg, "db-mysql.query", map[string]any{
		"instance": instance.Name, "sql": "SELECT NULL AS n, 'a\tb' AS t",
	})
	logJSON(t, "STEP5 db-mysql.query NULL + tab", raw)
	assertNoSecret(t, raw, secret)
	var nullQuery mysqlop.QueryResult
	mustUnmarshal(t, raw, &nullQuery)
	var nullRow map[string]any
	mustUnmarshal(t, nullQuery.Rows[0], &nullRow)
	if nullRow["n"] != nil {
		t.Fatalf("NULL 未映射为 null: %v", nullRow)
	}
	if nullRow["t"] != "a\tb" {
		t.Fatalf("字段内制表符被破坏: %q", nullRow["t"])
	}

	raw = call(t, cfg, "db-mysql.query", map[string]any{
		"instance": instance.Name, "sql": "SELECT 1 AS n UNION ALL SELECT 2 UNION ALL SELECT 3", "limit": 2,
	})
	logJSON(t, "STEP6 db-mysql.query limit=2", raw)
	var truncated mysqlop.QueryResult
	mustUnmarshal(t, raw, &truncated)
	if truncated.RowCount != 2 || !truncated.Truncated {
		t.Fatalf("截断结果=%+v，期望 2 行且 truncated=true", truncated)
	}

	raw = call(t, cfg, "db-mysql.status", map[string]any{"instance": instance.Name})
	logJSON(t, "STEP7 db-mysql.status", raw)
	assertNoSecret(t, raw, secret)
	var status mysqlop.StatusResult
	mustUnmarshal(t, raw, &status)
	if status.Version == "" || status.MaxConnections <= 0 || status.ThreadsConnected < 1 {
		t.Fatalf("status 指标异常: %+v", status)
	}

	_, perr = mysqlop.Call("db-mysql.query", mustJSON(t, map[string]any{
		"instance": instance.Name, "sql": "SELECT 1; DROP DATABASE mysql",
	}), cfg, nil)
	if perr == nil || perr.Code != pluginapi.E_INVALID {
		t.Fatalf("多语句未被拒绝: %v", perr)
	}
	t.Logf("STEP8 多语句被拒: %s: %s", perr.Code, perr.Message)
}

// seedDatabase 用本机客户端创建测试库（插件自身不提供写操作）。
func seedDatabase(t *testing.T, instance mysqlop.InstanceConfig) {
	t.Helper()
	args := []string{
		"--no-defaults", "--batch",
		"-h", instance.Host,
		"-P", strconv.Itoa(instance.Port),
		"-u", instance.User,
		"-e", "CREATE DATABASE IF NOT EXISTS kore_e2e",
	}
	cmd := exec.Command("mysql", args...)
	cmd.Env = filterEnv(os.Environ(), "MYSQL_PWD")
	if pw := password(t, instance); pw != "" {
		cmd.Env = append(cmd.Env, "MYSQL_PWD="+pw)
	}
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("创建测试库失败: %v (%s)", err, strings.TrimSpace(string(out)))
	}
}

// password 读取实例声明的密码环境变量；缺失时直接失败。
func password(t *testing.T, instance mysqlop.InstanceConfig) string {
	t.Helper()
	if instance.PasswordEnv == "" {
		return ""
	}
	value := os.Getenv(instance.PasswordEnv)
	if value == "" {
		t.Fatalf("密码环境变量 %s 未设置", instance.PasswordEnv)
	}
	return value
}

// filterEnv 删除指定 key 的环境变量，保证追加值生效。
func filterEnv(env []string, key string) []string {
	prefix := key + "="
	out := env[:0]
	for _, item := range env {
		if !strings.HasPrefix(item, prefix) {
			out = append(out, item)
		}
	}
	return out
}

// call 调用方法并要求成功。
func call(t *testing.T, cfg mysqlop.Config, method string, params map[string]any) json.RawMessage {
	t.Helper()
	result, perr := mysqlop.Call(method, mustJSON(t, params), cfg, nil)
	if perr != nil {
		t.Fatalf("%s 失败: %s: %s", method, perr.Code, perr.Message)
	}
	return result
}

func mustJSON(t *testing.T, value any) json.RawMessage {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("序列化参数失败: %v", err)
	}
	return data
}

func mustUnmarshal(t *testing.T, raw json.RawMessage, target any) {
	t.Helper()
	if err := json.Unmarshal(raw, target); err != nil {
		t.Fatalf("解析结果失败: %v (%s)", err, raw)
	}
}

// logJSON 以缩进 JSON 输出到测试日志，作为验收证据。
func logJSON(t *testing.T, label string, raw json.RawMessage) {
	t.Helper()
	var buffer bytes.Buffer
	if err := json.Indent(&buffer, raw, "", "  "); err != nil {
		t.Logf("%s: %s", label, raw)
		return
	}
	t.Logf("%s:\n%s", label, buffer.String())
}

// assertNoSecret 断言返回内容不含实例密码（密码只应存在于子进程 MYSQL_PWD）。
func assertNoSecret(t *testing.T, raw []byte, secret string) {
	t.Helper()
	if secret != "" && bytes.Contains(raw, []byte(secret)) {
		t.Fatal("返回内容泄漏实例密码")
	}
}

func contains(list []string, want string) bool {
	for _, item := range list {
		if item == want {
			return true
		}
	}
	return false
}
