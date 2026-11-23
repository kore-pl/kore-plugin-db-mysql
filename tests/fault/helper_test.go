// Package fault 对 db-mysql 插件做四类故障注入，验证方法级错误返回。
// 每个用例向 mysqlop.Call 的执行面注入一种故障，断言方法返回的结构化错误码。
package fault

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/pengtang1024/kore-plugin-db-mysql/internal/mysqlop"
	"github.com/pengtang1024/kore/pkg/pluginapi"
)

// faultExportDir 是故障用例的导出白名单根，由 TestMain 创建临时目录并清理：
// L2 的 export/import 先过路径校验，未配置导出目录会在触达执行面之前返回 E_INVALID。
var faultExportDir string

// testConfig 是带一个实例、已声明 system:admin 的插件配置（导出目录在 TestMain 里补齐）。
var testConfig = mysqlop.Config{
	Bin:     "mysql",
	Timeout: 0,
	Admin:   true,
	Instances: []mysqlop.InstanceConfig{
		{Name: "primary", Host: "127.0.0.1", Port: 3306, User: "root"},
	},
}

// TestMain 准备临时导出目录与一个可导入的种子文件（import 要求文件已存在），结束后清理。
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "kore-db-mysql-fault-")
	if err != nil {
		os.Exit(1)
	}
	faultExportDir = dir
	testConfig.ExportDir = dir
	if err := os.WriteFile(filepath.Join(dir, "fault-seed.sql"), []byte("SELECT 1;\n"), 0o600); err != nil {
		_ = os.RemoveAll(dir)
		os.Exit(1)
	}
	code := m.Run()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}

// faultExecutor 按注入类别让每个方法都失败，模拟四类运行时故障。
type faultExecutor struct {
	kind mysqlop.FaultKind
}

func (f faultExecutor) fail(what string) error {
	return mysqlop.NewFaultError(f.kind, what+" 执行面故障")
}

func (f faultExecutor) Instances(instances []mysqlop.InstanceConfig) ([]mysqlop.InstanceStatus, error) {
	return nil, f.fail("instances")
}

func (f faultExecutor) Databases(instance mysqlop.InstanceConfig, includeSystem bool) ([]string, error) {
	return nil, f.fail("databases")
}

func (f faultExecutor) Query(instance mysqlop.InstanceConfig, sql string, limit int) (mysqlop.QueryResult, error) {
	return mysqlop.QueryResult{}, f.fail("query")
}

func (f faultExecutor) Status(instance mysqlop.InstanceConfig) (mysqlop.StatusResult, error) {
	return mysqlop.StatusResult{}, f.fail("status")
}

func (f faultExecutor) CreateDatabase(instance mysqlop.InstanceConfig, name, charset, collation string) (bool, error) {
	return false, f.fail("createDatabase")
}

func (f faultExecutor) DropDatabase(instance mysqlop.InstanceConfig, name string) (bool, error) {
	return false, f.fail("dropDatabase")
}

func (f faultExecutor) CreateUser(instance mysqlop.InstanceConfig, user, host, password string) (bool, error) {
	return false, f.fail("createUser")
}

func (f faultExecutor) ChangePassword(instance mysqlop.InstanceConfig, user, host, password string) (bool, error) {
	return false, f.fail("changePassword")
}

func (f faultExecutor) DropUser(instance mysqlop.InstanceConfig, user, host string) (bool, error) {
	return false, f.fail("dropUser")
}

func (f faultExecutor) Grant(instance mysqlop.InstanceConfig, user, host, database string, privileges []string) error {
	return f.fail("grant")
}

func (f faultExecutor) Export(instance mysqlop.InstanceConfig, database string, tables []string, filePath string) (int64, error) {
	return 0, f.fail("export")
}

func (f faultExecutor) Import(instance mysqlop.InstanceConfig, database, filePath string) error {
	return f.fail("import")
}

// okExecutor 是无故障执行面，用于 sanity 反证。
type okExecutor struct{}

func (okExecutor) Instances(instances []mysqlop.InstanceConfig) ([]mysqlop.InstanceStatus, error) {
	return []mysqlop.InstanceStatus{}, nil
}

func (okExecutor) Databases(instance mysqlop.InstanceConfig, includeSystem bool) ([]string, error) {
	return []string{}, nil
}

func (okExecutor) Query(instance mysqlop.InstanceConfig, sql string, limit int) (mysqlop.QueryResult, error) {
	return mysqlop.QueryResult{Columns: []string{}, Rows: []json.RawMessage{}}, nil
}

func (okExecutor) Status(instance mysqlop.InstanceConfig) (mysqlop.StatusResult, error) {
	return mysqlop.StatusResult{Instance: instance.Name, Version: "8.0.0"}, nil
}

func (okExecutor) CreateDatabase(instance mysqlop.InstanceConfig, name, charset, collation string) (bool, error) {
	return true, nil
}

func (okExecutor) DropDatabase(instance mysqlop.InstanceConfig, name string) (bool, error) {
	return true, nil
}

func (okExecutor) CreateUser(instance mysqlop.InstanceConfig, user, host, password string) (bool, error) {
	return true, nil
}

func (okExecutor) ChangePassword(instance mysqlop.InstanceConfig, user, host, password string) (bool, error) {
	return true, nil
}

func (okExecutor) DropUser(instance mysqlop.InstanceConfig, user, host string) (bool, error) {
	return true, nil
}

func (okExecutor) Grant(instance mysqlop.InstanceConfig, user, host, database string, privileges []string) error {
	return nil
}

func (okExecutor) Export(instance mysqlop.InstanceConfig, database string, tables []string, filePath string) (int64, error) {
	return 1, nil
}

func (okExecutor) Import(instance mysqlop.InstanceConfig, database, filePath string) error {
	return nil
}

// methods 是全部方法及其合法参数（含 L2 写操作）。
var methods = []struct {
	name   string
	method string
	params string
}{
	{"instances", "db-mysql.instances", `{}`},
	{"databases", "db-mysql.databases", `{"instance":"primary"}`},
	{"query", "db-mysql.query", `{"instance":"primary","sql":"SELECT 1"}`},
	{"status", "db-mysql.status", `{"instance":"primary"}`},
	{"createDatabase", "db-mysql.createDatabase", `{"instance":"primary","name":"kore_fault"}`},
	{"dropDatabase", "db-mysql.dropDatabase", `{"instance":"primary","name":"kore_fault","confirm":true}`},
	{"createUser", "db-mysql.createUser", `{"instance":"primary","user":"kore_fault_user"}`},
	{"dropUser", "db-mysql.dropUser", `{"instance":"primary","user":"kore_fault_user","confirm":true}`},
	{"changePassword", "db-mysql.changePassword", `{"instance":"primary","user":"kore_fault_user","password":"Str0ngPass!","confirm":true}`},
	{"grant", "db-mysql.grant", `{"instance":"primary","user":"kore_fault_user","database":"kore_fault","privileges":["SELECT"]}`},
	{"export", "db-mysql.export", `{"instance":"primary","database":"kore_fault","file_path":"fault.sql"}`},
	{"import", "db-mysql.import", `{"instance":"primary","database":"kore_fault","file_path":"fault-seed.sql"}`},
	{"slowlog", "db-mysql.slowlog", `{"instance":"primary"}`},
}

// assertFault 对每个方法注入故障，断言返回 nil 结果 + 预期错误码与类别关键词。
func assertFault(t *testing.T, kind mysqlop.FaultKind, wantCode, wantHint string) {
	t.Helper()
	exec := faultExecutor{kind: kind}
	for _, m := range methods {
		t.Run(m.name, func(t *testing.T) {
			result, perr := mysqlop.Call(m.method, json.RawMessage(m.params), testConfig, exec)
			if perr == nil {
				t.Fatalf("%s 注入 %s 后未返回错误（结果=%s）", m.method, kind, result)
			}
			if result != nil {
				t.Fatalf("%s 出错时不应带结果: %s", m.method, result)
			}
			if perr.Code != wantCode {
				t.Fatalf("%s 错误码=%q，期望 %q（message=%q）", m.method, perr.Code, wantCode, perr.Message)
			}
			if !contains(perr.Message, wantHint) {
				t.Fatalf("%s 错误信息 %q 未含类别关键词 %q", m.method, perr.Message, wantHint)
			}
		})
	}
}

// contains 是 strings.Contains 的本地封装。
func contains(s, sub string) bool {
	if len(sub) > len(s) {
		return false
	}
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

// sanityNoFault 验证无故障时四方法正常返回，证明故障断言不是"永远失败"的假通过。
func sanityNoFault(t *testing.T) {
	t.Helper()
	for _, m := range methods {
		result, perr := mysqlop.Call(m.method, json.RawMessage(m.params), testConfig, okExecutor{})
		if perr != nil {
			t.Fatalf("无故障时 %s 意外返回错误: %v", m.method, perr)
		}
		if len(result) == 0 {
			t.Fatalf("无故障时 %s 返回空结果", m.method)
		}
	}
}

// verifyErrorContract 断言错误体是标准协议结构。
func verifyErrorContract(t *testing.T, perr *pluginapi.Error) {
	t.Helper()
	if perr == nil {
		t.Fatal("错误体为 nil")
	}
	if perr.Code == "" || perr.Message == "" {
		t.Fatalf("错误体字段不完整: %+v", perr)
	}
}
