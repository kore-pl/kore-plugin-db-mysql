package mysqlop

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pengtang1024/kore/pkg/pluginapi"
)

// adminTestConfig 是已声明 system:admin、带导出目录的单实例配置。
func adminTestConfig(t *testing.T) Config {
	t.Helper()
	cfg := callTestConfig()
	cfg.Admin = true
	cfg.ExportDir = t.TempDir()
	return cfg
}

// decodeCall 解码方法结果为给定结构。
func decodeCall(t *testing.T, raw json.RawMessage, target any) {
	t.Helper()
	if err := json.Unmarshal(raw, target); err != nil {
		t.Fatalf("解析结果失败: %v (%s)", err, raw)
	}
}

// TestCreateDatabaseDefaults 默认字符集/排序规则与 created 语义。
func TestCreateDatabaseDefaults(t *testing.T) {
	exec := &fakeExecutor{created: true}
	result, perr := Call("db-mysql.createDatabase", json.RawMessage(`{"name":"app_db"}`), adminTestConfig(t), exec)
	if perr != nil {
		t.Fatalf("createDatabase 错误: %v", perr)
	}
	if exec.adminCalls != 1 || exec.lastDatabase != "app_db" ||
		exec.lastCharset != "utf8mb4" || exec.lastCollation != "utf8mb4_unicode_ci" {
		t.Fatalf("执行面入参错: calls=%d db=%q charset=%q collation=%q",
			exec.adminCalls, exec.lastDatabase, exec.lastCharset, exec.lastCollation)
	}
	var decoded CreateDatabaseResult
	decodeCall(t, result, &decoded)
	if !decoded.Created || decoded.Name != "app_db" {
		t.Fatalf("结果=%s", result)
	}
}

// TestCreateDatabaseCustomCharset 显式 charset 时不再强塞 utf8mb4 排序规则。
func TestCreateDatabaseCustomCharset(t *testing.T) {
	exec := &fakeExecutor{created: true}
	_, perr := Call("db-mysql.createDatabase",
		json.RawMessage(`{"name":"latin_db","charset":"latin1"}`), adminTestConfig(t), exec)
	if perr != nil {
		t.Fatalf("createDatabase 错误: %v", perr)
	}
	if exec.lastCharset != "latin1" || exec.lastCollation != "" {
		t.Fatalf("charset/collation 未按显式值传递: %q/%q", exec.lastCharset, exec.lastCollation)
	}
}

// TestCreateDatabaseRejectsInjection 标识符白名单拒绝注入形态，且不触达执行面。
func TestCreateDatabaseRejectsInjection(t *testing.T) {
	badNames := []string{
		"",
		"a; DROP DATABASE mysql",
		"a`b",
		"a'b",
		"a\"b",
		"a b",
		"../etc",
		"a/b",
		strings.Repeat("x", 65),
	}
	for _, name := range badNames {
		exec := &fakeExecutor{}
		params, _ := json.Marshal(map[string]string{"name": name})
		_, perr := Call("db-mysql.createDatabase", params, adminTestConfig(t), exec)
		if perr == nil || perr.Code != pluginapi.E_INVALID {
			t.Fatalf("name=%q 错误=%v，期望 E_INVALID", name, perr)
		}
		if exec.adminCalls != 0 {
			t.Fatalf("name=%q 被拒后仍触达执行面", name)
		}
	}
	// charset/collation 也必须是纯标识符。
	for _, params := range []string{
		`{"name":"app","charset":"utf8mb4; DROP DATABASE x"}`,
		`{"name":"app","collation":"utf8mb4_unicode_ci'--"}`,
	} {
		exec := &fakeExecutor{}
		_, perr := Call("db-mysql.createDatabase", json.RawMessage(params), adminTestConfig(t), exec)
		if perr == nil || perr.Code != pluginapi.E_INVALID || exec.adminCalls != 0 {
			t.Fatalf("params=%s 错误=%v 调用=%d，期望 E_INVALID 且不触达执行面", params, perr, exec.adminCalls)
		}
	}
}

// TestAdminRequiresCapability 未声明 system:admin 时全部写方法 E_UNAUTHORIZED。
func TestAdminRequiresCapability(t *testing.T) {
	cases := []struct {
		method string
		params string
	}{
		{"db-mysql.createDatabase", `{"name":"app"}`},
		{"db-mysql.dropDatabase", `{"name":"app","confirm":true}`},
		{"db-mysql.createUser", `{"user":"app_user"}`},
		{"db-mysql.dropUser", `{"user":"app_user","confirm":true}`},
		{"db-mysql.grant", `{"user":"app_user","database":"app","privileges":["SELECT"]}`},
		{"db-mysql.export", `{"database":"app","file_path":"dump.sql"}`},
		{"db-mysql.import", `{"database":"app","file_path":"dump.sql"}`},
	}
	for _, c := range cases {
		cfg := adminTestConfig(t)
		cfg.Admin = false
		exec := &fakeExecutor{}
		_, perr := Call(c.method, json.RawMessage(c.params), cfg, exec)
		if perr == nil || perr.Code != pluginapi.E_UNAUTHORIZED {
			t.Fatalf("%s 错误=%v，期望 E_UNAUTHORIZED", c.method, perr)
		}
		if !strings.Contains(perr.Message, "system:admin") {
			t.Fatalf("%s 错误信息未说明能力要求: %q", c.method, perr.Message)
		}
		if exec.adminCalls != 0 {
			t.Fatalf("%s 越权后仍触达执行面", c.method)
		}
	}
}

// TestDropDatabaseConfirm confirm 缺失/false 一律 E_INVALID，且库不会被删除。
func TestDropDatabaseConfirm(t *testing.T) {
	for _, params := range []string{
		`{"name":"app_db"}`,
		`{"name":"app_db","confirm":false}`,
	} {
		exec := &fakeExecutor{dropped: true}
		_, perr := Call("db-mysql.dropDatabase", json.RawMessage(params), adminTestConfig(t), exec)
		if perr == nil || perr.Code != pluginapi.E_INVALID {
			t.Fatalf("params=%s 错误=%v，期望 E_INVALID", params, perr)
		}
		if !strings.Contains(perr.Message, "confirm=true") {
			t.Fatalf("错误信息未提示 confirm=true: %q", perr.Message)
		}
		if exec.adminCalls != 0 {
			t.Fatalf("缺少 confirm 仍执行了删库: %s", params)
		}
	}
	exec := &fakeExecutor{dropped: true}
	result, perr := Call("db-mysql.dropDatabase",
		json.RawMessage(`{"name":"app_db","confirm":true}`), adminTestConfig(t), exec)
	if perr != nil {
		t.Fatalf("confirm=true 仍失败: %v", perr)
	}
	var decoded DropDatabaseResult
	decodeCall(t, result, &decoded)
	if !decoded.Dropped || decoded.Name != "app_db" {
		t.Fatalf("结果=%s", result)
	}
}

// TestDropDatabaseSystemProtected 四个系统库在任何 confirm 下都 E_UNAUTHORIZED。
func TestDropDatabaseSystemProtected(t *testing.T) {
	for _, name := range []string{"mysql", "information_schema", "performance_schema", "sys", "MySQL"} {
		for _, confirm := range []bool{false, true} {
			exec := &fakeExecutor{}
			params, _ := json.Marshal(map[string]any{"name": name, "confirm": confirm})
			_, perr := Call("db-mysql.dropDatabase", params, adminTestConfig(t), exec)
			if perr == nil || perr.Code != pluginapi.E_UNAUTHORIZED {
				t.Fatalf("name=%q confirm=%v 错误=%v，期望 E_UNAUTHORIZED", name, confirm, perr)
			}
			if exec.adminCalls != 0 {
				t.Fatalf("name=%q 被保护后仍触达执行面", name)
			}
		}
	}
}

// TestCreateUserDefaultsAndNoPasswordEcho 默认 host=localhost，返回值不含密码。
func TestCreateUserDefaultsAndNoPasswordEcho(t *testing.T) {
	exec := &fakeExecutor{created: true}
	result, perr := Call("db-mysql.createUser",
		json.RawMessage(`{"user":"app_user","password":"s3cr3t-value"}`), adminTestConfig(t), exec)
	if perr != nil {
		t.Fatalf("createUser 错误: %v", perr)
	}
	if exec.lastUser != "app_user" || exec.lastHost != "localhost" || exec.lastPassword != "s3cr3t-value" {
		t.Fatalf("执行面入参错: user=%q host=%q password=%q", exec.lastUser, exec.lastHost, exec.lastPassword)
	}
	if strings.Contains(string(result), "s3cr3t-value") || strings.Contains(string(result), "password") {
		t.Fatalf("返回值泄漏密码: %s", result)
	}
	var decoded CreateUserResult
	decodeCall(t, result, &decoded)
	if !decoded.Created || decoded.User != "app_user" || decoded.Host != "localhost" {
		t.Fatalf("结果=%s", result)
	}
}

// TestCreateUserRejectsInjection 用户名/host 白名单与密码 NUL 校验。
func TestCreateUserRejectsInjection(t *testing.T) {
	cases := []string{
		`{"user":"app'; DROP USER root; --"}`,
		`{"user":"app user"}`,
		`{"user":""}`,
		`{"user":"` + strings.Repeat("x", 33) + `"}`,
		`{"user":"app","host":"localhost' OR '1'='1"}`,
		`{"user":"app","host":"a b"}`,
		`{"user":"app","password":"a\u0000b"}`,
		`{"user":"app","password":"` + strings.Repeat("p", 1025) + `"}`,
	}
	for _, params := range cases {
		exec := &fakeExecutor{created: true}
		_, perr := Call("db-mysql.createUser", json.RawMessage(params), adminTestConfig(t), exec)
		if perr == nil || perr.Code != pluginapi.E_INVALID {
			t.Fatalf("params=%s 错误=%v，期望 E_INVALID", params, perr)
		}
		if exec.adminCalls != 0 {
			t.Fatalf("params=%s 被拒后仍触达执行面", params)
		}
	}
}

// TestCreateUserEscapesPassword 引号/反斜杠密码按 sql_mode 在两个候选实现下都安全。
func TestCreateUserEscapesPassword(t *testing.T) {
	// 默认模式：反斜杠需要转义；NO_BACKSLASH_ESCAPES：反斜杠保持原样。
	defaultSQL := createUserSQL("app_user", "localhost", `p@ss'w0rd\x`, true)
	if !strings.Contains(defaultSQL, `'p@ss''w0rd\\x'`) {
		t.Fatalf("默认模式密码转义错误: %s", defaultSQL)
	}
	noBackslashSQL := createUserSQL("app_user", "localhost", `p@ss'w0rd\x`, false)
	if !strings.Contains(noBackslashSQL, `'p@ss''w0rd\x'`) {
		t.Fatalf("NO_BACKSLASH_ESCAPES 下密码转义错误: %s", noBackslashSQL)
	}
	if strings.Contains(defaultSQL, "p@ss'w0rd") {
		t.Fatalf("密码未转义单引号: %s", defaultSQL)
	}
}

// TestCreateUserRedactsPasswordInError 执行面回显密码时，错误信息也必须脱敏。
func TestCreateUserRedactsPasswordInError(t *testing.T) {
	exec := &fakeExecutor{err: opErrorf(pluginapi.E_UPSTREAM, "CREATE USER failed for password s3cr3t-value")}
	_, perr := Call("db-mysql.createUser",
		json.RawMessage(`{"user":"app_user","password":"s3cr3t-value"}`), adminTestConfig(t), exec)
	if perr == nil {
		t.Fatal("执行面错误未上报")
	}
	if strings.Contains(perr.Message, "s3cr3t-value") {
		t.Fatalf("错误信息泄漏密码: %q", perr.Message)
	}
	if !strings.Contains(perr.Message, "***") {
		t.Fatalf("密码未替换为 ***: %q", perr.Message)
	}
}

// TestCreateUserGrantsSpec createUser 的 grants 便捷项复用授权白名单。
func TestCreateUserGrantsSpec(t *testing.T) {
	exec := &fakeExecutor{created: true}
	_, perr := Call("db-mysql.createUser",
		json.RawMessage(`{"user":"app_user","grants":["app_db:SELECT,insert"]}`), adminTestConfig(t), exec)
	if perr != nil {
		t.Fatalf("createUser+grants 错误: %v", perr)
	}
	if exec.adminCalls != 2 || exec.lastDatabase != "app_db" ||
		strings.Join(exec.lastPrivileges, ",") != "SELECT,INSERT" {
		t.Fatalf("grants 未按白名单下发: calls=%d db=%q privs=%v",
			exec.adminCalls, exec.lastDatabase, exec.lastPrivileges)
	}
	for _, params := range []string{
		`{"user":"app_user","grants":["app_db:ALL PRIVILEGES"]}`,
		`{"user":"app_user","grants":["app_db"]}`,
		`{"user":"app_user","grants":[":SELECT"]}`,
		`{"user":"app_user","grants":["bad db:SELECT"]}`,
	} {
		exec := &fakeExecutor{created: true}
		_, perr := Call("db-mysql.createUser", json.RawMessage(params), adminTestConfig(t), exec)
		if perr == nil || perr.Code != pluginapi.E_INVALID {
			t.Fatalf("params=%s 错误=%v，期望 E_INVALID", params, perr)
		}
	}
}

// TestDropUserProtections root 受保护、confirm 缺失被拒。
func TestDropUserProtections(t *testing.T) {
	for _, host := range []string{"localhost", "%", "10.0.0.1"} {
		params, _ := json.Marshal(map[string]any{"user": "root", "host": host, "confirm": true})
		exec := &fakeExecutor{}
		_, perr := Call("db-mysql.dropUser", params, adminTestConfig(t), exec)
		if perr == nil || perr.Code != pluginapi.E_UNAUTHORIZED {
			t.Fatalf("root@%s 错误=%v，期望 E_UNAUTHORIZED", host, perr)
		}
		if exec.adminCalls != 0 {
			t.Fatalf("root@%s 被保护后仍触达执行面", host)
		}
	}
	exec := &fakeExecutor{}
	_, perr := Call("db-mysql.dropUser", json.RawMessage(`{"user":"app_user"}`), adminTestConfig(t), exec)
	if perr == nil || perr.Code != pluginapi.E_INVALID || !strings.Contains(perr.Message, "confirm=true") {
		t.Fatalf("缺少 confirm 错误=%v，期望 E_INVALID 且提示 confirm=true", perr)
	}
	if exec.adminCalls != 0 {
		t.Fatal("缺少 confirm 仍执行了删用户")
	}
	exec = &fakeExecutor{dropped: true}
	result, perr := Call("db-mysql.dropUser",
		json.RawMessage(`{"user":"app_user","confirm":true}`), adminTestConfig(t), exec)
	if perr != nil {
		t.Fatalf("confirm=true 仍失败: %v", perr)
	}
	var decoded DropUserResult
	decodeCall(t, result, &decoded)
	if !decoded.Dropped {
		t.Fatalf("结果=%s", result)
	}
}

// TestGrantWhitelist 权限白名单：允许集合去重归一，ALL PRIVILEGES / GRANT OPTION 拒绝。
func TestGrantWhitelist(t *testing.T) {
	exec := &fakeExecutor{}
	result, perr := Call("db-mysql.grant",
		json.RawMessage(`{"user":"app_user","database":"app_db","privileges":["SELECT","select","INSERT"]}`),
		adminTestConfig(t), exec)
	if perr != nil {
		t.Fatalf("grant 错误: %v", perr)
	}
	if strings.Join(exec.lastPrivileges, ",") != "SELECT,INSERT" {
		t.Fatalf("权限未归一化: %v", exec.lastPrivileges)
	}
	var decoded GrantResult
	decodeCall(t, result, &decoded)
	if !decoded.Granted || strings.Join(decoded.Privileges, ",") != "SELECT,INSERT" {
		t.Fatalf("结果=%s", result)
	}
	for _, privileges := range []string{
		`[]`,
		`[""]`,
		`["ALL PRIVILEGES"]`,
		`["ALL"]`,
		`["GRANT OPTION"]`,
		`["SELECT","FILE"]`,
		`["SELECT","WITH GRANT OPTION"]`,
	} {
		exec := &fakeExecutor{}
		params := `{"user":"app_user","database":"app_db","privileges":` + privileges + `}`
		_, perr := Call("db-mysql.grant", json.RawMessage(params), adminTestConfig(t), exec)
		if perr == nil || perr.Code != pluginapi.E_INVALID {
			t.Fatalf("privileges=%s 错误=%v，期望 E_INVALID", privileges, perr)
		}
		if exec.adminCalls != 0 {
			t.Fatalf("privileges=%s 被拒后仍触达执行面", privileges)
		}
	}
}

// TestGrantRejectsInjection 库名/用户名注入形态在执行面之前被拒。
func TestGrantRejectsInjection(t *testing.T) {
	for _, params := range []string{
		`{"user":"app'; --","database":"app","privileges":["SELECT"]}`,
		`{"user":"app_user","database":"app` + "`" + `; DROP DATABASE mysql","privileges":["SELECT"]}`,
		`{"user":"app_user","database":"../app","privileges":["SELECT"]}`,
	} {
		exec := &fakeExecutor{}
		_, perr := Call("db-mysql.grant", json.RawMessage(params), adminTestConfig(t), exec)
		if perr == nil || perr.Code != pluginapi.E_INVALID || exec.adminCalls != 0 {
			t.Fatalf("params=%s 错误=%v 调用=%d，期望 E_INVALID 且不触达执行面", params, perr, exec.adminCalls)
		}
	}
}

// TestAdminInstanceResolution 未指定 instance 时用 default；多实例无 default 必须显式指定。
func TestAdminInstanceResolution(t *testing.T) {
	cfg := Config{Admin: true, Instances: []InstanceConfig{
		{Name: "default", Host: "127.0.0.1", Port: 3306, User: "root"},
		{Name: "replica", Host: "127.0.0.1", Port: 3307, User: "root"},
	}}
	exec := &fakeExecutor{created: true}
	if _, perr := Call("db-mysql.createDatabase", json.RawMessage(`{"name":"app"}`), cfg, exec); perr != nil {
		t.Fatalf("default 实例解析失败: %v", perr)
	}
	cfg.Instances = []InstanceConfig{
		{Name: "primary", Host: "127.0.0.1", Port: 3306, User: "root"},
		{Name: "replica", Host: "127.0.0.1", Port: 3307, User: "root"},
	}
	_, perr := Call("db-mysql.createDatabase", json.RawMessage(`{"name":"app"}`), cfg, &fakeExecutor{created: true})
	if perr == nil || perr.Code != pluginapi.E_INVALID || !strings.Contains(perr.Message, "instance") {
		t.Fatalf("多实例未指定错误=%v，期望 E_INVALID 提示 instance", perr)
	}
	if _, perr := Call("db-mysql.createDatabase", json.RawMessage(`{"name":"app","instance":"replica"}`), cfg, &fakeExecutor{created: true}); perr != nil {
		t.Fatalf("显式 instance 解析失败: %v", perr)
	}
}

// TestExportValidatesPathAndTables 路径穿越与表名注入在触达执行面之前被拒。
func TestExportValidatesPathAndTables(t *testing.T) {
	cfg := adminTestConfig(t)
	badPaths := []string{
		"../escape.sql",
		"/etc/passwd",
		"a/../../b.sql",
		"a\\b.sql",
		"",
		" leading.sql",
	}
	for _, path := range badPaths {
		exec := &fakeExecutor{}
		params, _ := json.Marshal(map[string]string{"database": "app_db", "file_path": path})
		_, perr := Call("db-mysql.export", params, cfg, exec)
		if perr == nil || perr.Code != pluginapi.E_INVALID {
			t.Fatalf("file_path=%q 错误=%v，期望 E_INVALID", path, perr)
		}
		if exec.adminCalls != 0 {
			t.Fatalf("file_path=%q 被拒后仍触达执行面", path)
		}
	}
	for _, table := range []string{"t; DROP TABLE x", "t`x", "../t", "t x"} {
		exec := &fakeExecutor{}
		params, _ := json.Marshal(map[string]any{
			"database": "app_db", "file_path": "dump.sql", "tables": []string{table},
		})
		_, perr := Call("db-mysql.export", params, cfg, exec)
		if perr == nil || perr.Code != pluginapi.E_INVALID || exec.adminCalls != 0 {
			t.Fatalf("table=%q 错误=%v 调用=%d，期望 E_INVALID", table, perr, exec.adminCalls)
		}
	}
}

// TestExportResolvesUnderRoot 合法相对/绝对路径都落在导出根目录内，结果携带大小与表名。
func TestExportResolvesUnderRoot(t *testing.T) {
	cfg := adminTestConfig(t)
	exec := &fakeExecutor{exportedSize: 2048}
	result, perr := Call("db-mysql.export",
		json.RawMessage(`{"database":"app_db","tables":["t1","t2","t1"],"file_path":"sub/dump.sql"}`), cfg, exec)
	if perr != nil {
		t.Fatalf("export 错误: %v", perr)
	}
	wantPath := filepath.Join(cfg.ExportDir, "sub", "dump.sql")
	if exec.lastFilePath != wantPath {
		t.Fatalf("导出路径=%q，期望 %q", exec.lastFilePath, wantPath)
	}
	if strings.Join(exec.lastTables, ",") != "t1,t2" {
		t.Fatalf("表名未去重: %v", exec.lastTables)
	}
	var decoded ExportResult
	decodeCall(t, result, &decoded)
	if decoded.SizeBytes != 2048 || decoded.Database != "app_db" ||
		strings.Join(decoded.Tables, ",") != "t1,t2" || decoded.FilePath != wantPath {
		t.Fatalf("结果=%s", result)
	}

	exec = &fakeExecutor{exportedSize: 1}
	absolute := filepath.Join(cfg.ExportDir, "abs.sql")
	params, _ := json.Marshal(map[string]string{"database": "app_db", "file_path": absolute})
	if _, perr := Call("db-mysql.export", params, cfg, exec); perr != nil {
		t.Fatalf("导出根目录内的绝对路径应放行: %v", perr)
	}
	if exec.lastFilePath != absolute {
		t.Fatalf("绝对路径解析=%q，期望 %q", exec.lastFilePath, absolute)
	}
}

// TestExportSizeLimit 超过 256 MiB 拒绝并删除半成品文件。
func TestExportSizeLimit(t *testing.T) {
	cfg := adminTestConfig(t)
	target := filepath.Join(cfg.ExportDir, "big.sql")
	if err := os.WriteFile(target, []byte("partial"), 0o600); err != nil {
		t.Fatal(err)
	}
	exec := &fakeExecutor{exportedSize: maxTransferBytes + 1}
	_, perr := Call("db-mysql.export",
		json.RawMessage(`{"database":"app_db","file_path":"big.sql"}`), cfg, exec)
	if perr == nil || perr.Code != pluginapi.E_INVALID || !strings.Contains(perr.Message, "256 MiB") {
		t.Fatalf("超限错误=%v，期望 E_INVALID 且说明 256 MiB", perr)
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatalf("超限文件未删除: %v", err)
	}
}

// TestExportMissingExportDir 未配置导出目录时给出可操作提示。
func TestExportMissingExportDir(t *testing.T) {
	cfg := adminTestConfig(t)
	cfg.ExportDir = ""
	_, perr := Call("db-mysql.export",
		json.RawMessage(`{"database":"app_db","file_path":"dump.sql"}`), cfg, &fakeExecutor{})
	if perr == nil || perr.Code != pluginapi.E_INVALID || !strings.Contains(perr.Message, "KORE_DB_EXPORT_DIR") {
		t.Fatalf("错误=%v，期望 E_INVALID 且提示配置项", perr)
	}
}

// TestImportValidatesFile 不存在/空/超限/目录都拒绝，合法文件返回大小。
func TestImportValidatesFile(t *testing.T) {
	cfg := adminTestConfig(t)
	exec := &fakeExecutor{}
	_, perr := Call("db-mysql.import",
		json.RawMessage(`{"database":"app_db","file_path":"missing.sql"}`), cfg, exec)
	if perr == nil || perr.Code != pluginapi.E_INVALID || !strings.Contains(perr.Message, "不存在") {
		t.Fatalf("缺失文件错误=%v，期望 E_INVALID", perr)
	}
	if exec.adminCalls != 0 {
		t.Fatal("缺失文件仍触达执行面")
	}

	empty := filepath.Join(cfg.ExportDir, "empty.sql")
	if err := os.WriteFile(empty, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, perr := Call("db-mysql.import",
		json.RawMessage(`{"database":"app_db","file_path":"empty.sql"}`), cfg, &fakeExecutor{}); perr == nil ||
		perr.Code != pluginapi.E_INVALID || !strings.Contains(perr.Message, "为空") {
		t.Fatalf("空文件错误=%v，期望 E_INVALID 且说明为空", perr)
	}

	big := filepath.Join(cfg.ExportDir, "big.sql")
	if err := os.WriteFile(big, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Truncate(big, maxTransferBytes+1); err != nil {
		t.Fatal(err)
	}
	if _, perr := Call("db-mysql.import",
		json.RawMessage(`{"database":"app_db","file_path":"big.sql"}`), cfg, &fakeExecutor{}); perr == nil ||
		perr.Code != pluginapi.E_INVALID || !strings.Contains(perr.Message, "256 MiB") {
		t.Fatalf("超限文件错误=%v，期望 E_INVALID 且说明 256 MiB", perr)
	}

	seed := filepath.Join(cfg.ExportDir, "seed.sql")
	if err := os.WriteFile(seed, []byte("SELECT 1;\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	result, perr := Call("db-mysql.import",
		json.RawMessage(`{"database":"app_db","file_path":"seed.sql"}`), cfg, exec)
	if perr != nil {
		t.Fatalf("import 错误: %v", perr)
	}
	if exec.adminCalls != 1 || exec.lastDatabase != "app_db" || exec.lastFilePath != seed {
		t.Fatalf("执行面入参错: calls=%d db=%q file=%q", exec.adminCalls, exec.lastDatabase, exec.lastFilePath)
	}
	var decoded ImportResult
	decodeCall(t, result, &decoded)
	if decoded.SizeBytes != int64(len("SELECT 1;\n")) || decoded.FilePath != seed {
		t.Fatalf("结果=%s", result)
	}
}
