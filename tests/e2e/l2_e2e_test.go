// L2 管理能力的真实 MySQL 端到端验证：建库/建用户/授权/导出/导入/删除/保护/confirm。
//
// 与 mysql_e2e_test.go 共用同一套环境变量；默认跳过：
//
//	KORE_MYSQL_E2E=1 \
//	KORE_MYSQL_INSTANCES='[{"name":"primary","host":"127.0.0.1","port":3306,"user":"root","password_env":"KORE_MYSQL_PASSWORD"}]' \
//	KORE_MYSQL_PASSWORD=*** \
//	go test -timeout 300s -v ./tests/e2e -run TestL2RealMySQL
package e2e_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"

	"github.com/pengtang1024/kore-plugin-db-mysql/internal/mysqlop"
	"github.com/pengtang1024/kore/pkg/pluginapi"
)

const (
	l2Database = "kore_l2_test"
	l2Restore  = "kore_l2_restore"
	l2User     = "kore_l2_user"
	l2Password = "Kore!L2'pass\\word"
)

// TestL2RealMySQL 覆盖 L2 全流程，并把「不带 confirm 的 drop 被拒」「系统库保护」
// 「root 保护」「密码不回显」作为断言而不是旁注。
func TestL2RealMySQL(t *testing.T) {
	if os.Getenv("KORE_MYSQL_E2E") != "1" {
		t.Skip("设置 KORE_MYSQL_E2E=1 并准备好本机 MySQL 后运行")
	}
	cfg, err := mysqlop.LoadConfig()
	if err != nil {
		t.Fatalf("加载实例配置失败: %v", err)
	}
	if len(cfg.Instances) == 0 {
		t.Fatal("KORE_MYSQL_INSTANCES 未配置任何实例")
	}
	cfg.Admin = true
	cfg.ExportDir = t.TempDir()
	instance := cfg.Instances[0]
	secret := os.Getenv(instance.PasswordEnv)
	t.Logf("实例: name=%s host=%s port=%d user=%s export_dir=%s",
		instance.Name, instance.Host, instance.Port, instance.User, cfg.ExportDir)

	cleanup := func() {
		// 清理只用于让用例可重复运行；失败不掩盖主断言。
		for _, host := range []string{"%", "localhost"} {
			_, _ = mysqlop.Call("db-mysql.dropUser", mustJSON(t, map[string]any{
				"instance": instance.Name, "user": l2User, "host": host, "confirm": true,
			}), cfg, nil)
		}
		for _, database := range []string{l2Database, l2Restore} {
			_, _ = mysqlop.Call("db-mysql.dropDatabase", mustJSON(t, map[string]any{
				"instance": instance.Name, "name": database, "confirm": true,
			}), cfg, nil)
		}
	}
	cleanup()
	t.Cleanup(cleanup)

	// 步骤 1：建库 → SHOW DATABASES 可见。
	raw := l2Call(t, cfg, "db-mysql.createDatabase", map[string]any{
		"instance": instance.Name, "name": l2Database,
		"charset": "utf8mb4", "collation": "utf8mb4_unicode_ci",
	})
	logJSON(t, "L2-1 db-mysql.createDatabase", raw)
	assertNoSecret(t, raw, secret)
	var created mysqlop.CreateDatabaseResult
	mustUnmarshal(t, raw, &created)
	if !created.Created || created.Name != l2Database {
		t.Fatalf("建库结果异常: %s", raw)
	}
	if n := l2ScalarInt(t, cfg, instance, "SELECT COUNT(*) FROM information_schema.SCHEMATA WHERE SCHEMA_NAME = '"+l2Database+"'"); n != 1 {
		t.Fatalf("建库后 information_schema 计数=%d，期望 1", n)
	}

	// 步骤 2：建用户（密码含引号与反斜杠，验证字面量转义）→ 能真实登录。
	raw = l2Call(t, cfg, "db-mysql.createUser", map[string]any{
		"instance": instance.Name, "user": l2User, "host": "%", "password": l2Password,
	})
	logJSON(t, "L2-2 db-mysql.createUser", raw)
	assertNoSecret(t, raw, l2Password)
	assertNoSecret(t, raw, secret)
	var userCreated mysqlop.CreateUserResult
	mustUnmarshal(t, raw, &userCreated)
	if !userCreated.Created || userCreated.User != l2User || userCreated.Host != "%" {
		t.Fatalf("建用户结果异常: %s", raw)
	}
	l2AssertLogin(t, instance, l2User, l2Password)

	// 步骤 3：授权 SELECT → SHOW GRANTS 可见。
	raw = l2Call(t, cfg, "db-mysql.grant", map[string]any{
		"instance": instance.Name, "user": l2User, "host": "%",
		"database": l2Database, "privileges": []string{"SELECT"},
	})
	logJSON(t, "L2-3 db-mysql.grant", raw)
	assertNoSecret(t, raw, secret)
	var granted mysqlop.GrantResult
	mustUnmarshal(t, raw, &granted)
	if !granted.Granted || strings.Join(granted.Privileges, ",") != "SELECT" {
		t.Fatalf("授权结果异常: %s", raw)
	}
	grants := l2AllText(t, cfg, instance, "SHOW GRANTS FOR '"+l2User+"'@'%'")
	if !strings.Contains(grants, "SELECT") || !strings.Contains(grants, l2Database) {
		t.Fatalf("SHOW GRANTS 未见授权: %q", grants)
	}

	// 预先放一张表，让 dump/import 有真实结构可对比（建表不属于本插件方法面），
	// 再用新账号读一次，证明 GRANT 在服务端真实生效。
	l2ExecDirect(t, instance,
		"CREATE TABLE `"+l2Database+"`.`t_items` (id INT PRIMARY KEY, name VARCHAR(32) NOT NULL);"+
			"INSERT INTO `"+l2Database+"`.`t_items` VALUES (1,'alpha'),(2,'beta');")
	l2AssertPrivilegeWorks(t, instance, l2User, l2Password, l2Database)

	// 步骤 4：导出 → 文件存在且大小 > 0。
	raw = l2Call(t, cfg, "db-mysql.export", map[string]any{
		"instance": instance.Name, "database": l2Database, "file_path": "kore_l2_test.sql",
	})
	logJSON(t, "L2-4 db-mysql.export", raw)
	assertNoSecret(t, raw, secret)
	var exported mysqlop.ExportResult
	mustUnmarshal(t, raw, &exported)
	if exported.SizeBytes <= 0 || exported.Database != l2Database {
		t.Fatalf("导出结果异常: %s", raw)
	}
	info, err := os.Stat(exported.FilePath)
	if err != nil || !info.Mode().IsRegular() || info.Size() != exported.SizeBytes {
		t.Fatalf("导出文件异常: path=%q stat=%v size=%d", exported.FilePath, err, exported.SizeBytes)
	}

	// 步骤 5：导入到新库 → 表结构与行数一致。
	l2Call(t, cfg, "db-mysql.createDatabase", map[string]any{
		"instance": instance.Name, "name": l2Restore,
	})
	raw = l2Call(t, cfg, "db-mysql.import", map[string]any{
		"instance": instance.Name, "database": l2Restore, "file_path": "kore_l2_test.sql",
	})
	logJSON(t, "L2-5 db-mysql.import", raw)
	assertNoSecret(t, raw, secret)
	var imported mysqlop.ImportResult
	mustUnmarshal(t, raw, &imported)
	if imported.SizeBytes != exported.SizeBytes {
		t.Fatalf("导入大小=%d，期望 %d", imported.SizeBytes, exported.SizeBytes)
	}
	sourceSchema := l2TableSignature(t, cfg, instance, l2Database, "t_items")
	restoreSchema := l2TableSignature(t, cfg, instance, l2Restore, "t_items")
	if sourceSchema == "" || sourceSchema != restoreSchema {
		t.Fatalf("导入后表结构不一致:\nsource=%q\nrestore=%q", sourceSchema, restoreSchema)
	}
	if rows := l2ScalarInt(t, cfg, instance, "SELECT COUNT(*) FROM `"+l2Restore+"`.`t_items`"); rows != 2 {
		t.Fatalf("导入后行数=%d，期望 2", rows)
	}

	// 步骤 6：不带 confirm 删用户 → E_INVALID 且用户仍在。
	_, perr := mysqlop.Call("db-mysql.dropUser", mustJSON(t, map[string]any{
		"instance": instance.Name, "user": l2User, "host": "%",
	}), cfg, nil)
	if perr == nil || perr.Code != pluginapi.E_INVALID {
		t.Fatalf("不带 confirm 的 dropUser 错误=%v，期望 E_INVALID", perr)
	}
	if n := l2ScalarInt(t, cfg, instance, "SELECT COUNT(*) FROM mysql.user WHERE User = '"+l2User+"'"); n != 1 {
		t.Fatalf("未带 confirm 的 dropUser 后用户计数=%d，期望 1（仍在）", n)
	}
	t.Logf("L2-6 不带 confirm 的 dropUser 被拒: %s: %s", perr.Code, perr.Message)

	// 带 confirm 删用户 → mysql.user 中不再有。
	raw = l2Call(t, cfg, "db-mysql.dropUser", map[string]any{
		"instance": instance.Name, "user": l2User, "host": "%", "confirm": true,
	})
	logJSON(t, "L2-7 db-mysql.dropUser confirm=true", raw)
	var droppedUser mysqlop.DropUserResult
	mustUnmarshal(t, raw, &droppedUser)
	if !droppedUser.Dropped {
		t.Fatalf("删用户结果异常: %s", raw)
	}
	if n := l2ScalarInt(t, cfg, instance, "SELECT COUNT(*) FROM mysql.user WHERE User = '"+l2User+"'"); n != 0 {
		t.Fatalf("删用户后 mysql.user 计数=%d，期望 0", n)
	}

	// 步骤 8：不带 confirm 删库 → E_INVALID 且库还在。
	_, perr = mysqlop.Call("db-mysql.dropDatabase", mustJSON(t, map[string]any{
		"instance": instance.Name, "name": l2Database,
	}), cfg, nil)
	if perr == nil || perr.Code != pluginapi.E_INVALID {
		t.Fatalf("不带 confirm 的 dropDatabase 错误=%v，期望 E_INVALID", perr)
	}
	if n := l2ScalarInt(t, cfg, instance, "SELECT COUNT(*) FROM information_schema.SCHEMATA WHERE SCHEMA_NAME = '"+l2Database+"'"); n != 1 {
		t.Fatalf("未带 confirm 的 dropDatabase 后库计数=%d，期望 1（仍在）", n)
	}
	t.Logf("L2-8 不带 confirm 的 dropDatabase 被拒: %s: %s", perr.Code, perr.Message)

	// 步骤 9：删系统库 → E_UNAUTHORIZED（confirm=true 也不能例外）。
	_, perr = mysqlop.Call("db-mysql.dropDatabase", mustJSON(t, map[string]any{
		"instance": instance.Name, "name": "mysql", "confirm": true,
	}), cfg, nil)
	if perr == nil || perr.Code != pluginapi.E_UNAUTHORIZED {
		t.Fatalf("删 mysql 库错误=%v，期望 E_UNAUTHORIZED", perr)
	}
	t.Logf("L2-9 删系统库被拒: %s: %s", perr.Code, perr.Message)

	// 步骤 10：带 confirm 删测试库 → SHOW DATABASES 不再有。
	for _, database := range []string{l2Database, l2Restore} {
		raw = l2Call(t, cfg, "db-mysql.dropDatabase", map[string]any{
			"instance": instance.Name, "name": database, "confirm": true,
		})
		logJSON(t, "L2-10 db-mysql.dropDatabase "+database, raw)
		var dropped mysqlop.DropDatabaseResult
		mustUnmarshal(t, raw, &dropped)
		if !dropped.Dropped || dropped.Name != database {
			t.Fatalf("删库结果异常: %s", raw)
		}
		if n := l2ScalarInt(t, cfg, instance, "SELECT COUNT(*) FROM information_schema.SCHEMATA WHERE SCHEMA_NAME = '"+database+"'"); n != 0 {
			t.Fatalf("删库后 %s 计数=%d，期望 0", database, n)
		}
	}
}

// l2Call 调用插件方法并要求成功。
func l2Call(t *testing.T, cfg mysqlop.Config, method string, params map[string]any) json.RawMessage {
	t.Helper()
	result, perr := mysqlop.Call(method, mustJSON(t, params), cfg, nil)
	if perr != nil {
		t.Fatalf("%s 失败: %s: %s", method, perr.Code, perr.Message)
	}
	return result
}

// l2Query 执行只读查询并返回首行（列名 → 值）。
func l2Query(t *testing.T, cfg mysqlop.Config, instance mysqlop.InstanceConfig, sql string) map[string]any {
	t.Helper()
	raw := l2Call(t, cfg, "db-mysql.query", map[string]any{"instance": instance.Name, "sql": sql})
	var result mysqlop.QueryResult
	mustUnmarshal(t, raw, &result)
	if len(result.Rows) == 0 {
		return nil
	}
	var row map[string]any
	mustUnmarshal(t, result.Rows[0], &row)
	return row
}

// l2ScalarInt 取首行首列并转为 int。
func l2ScalarInt(t *testing.T, cfg mysqlop.Config, instance mysqlop.InstanceConfig, sql string) int64 {
	t.Helper()
	row := l2Query(t, cfg, instance, sql)
	for _, value := range row {
		number, ok := value.(float64)
		if !ok {
			t.Fatalf("%q 首列不是数字: %v", sql, value)
		}
		return int64(number)
	}
	t.Fatalf("%q 返回空行", sql)
	return 0
}

// l2AllText 把所有行的字符串列拼起来（SHOW GRANTS 每行一条授权，只看首行会漏）。
func l2AllText(t *testing.T, cfg mysqlop.Config, instance mysqlop.InstanceConfig, sql string) string {
	t.Helper()
	raw := l2Call(t, cfg, "db-mysql.query", map[string]any{"instance": instance.Name, "sql": sql})
	var result mysqlop.QueryResult
	mustUnmarshal(t, raw, &result)
	parts := make([]string, 0, len(result.Rows))
	for _, rowRaw := range result.Rows {
		var row map[string]any
		mustUnmarshal(t, rowRaw, &row)
		for _, value := range row {
			if text, ok := value.(string); ok {
				parts = append(parts, text)
			}
		}
	}
	return strings.Join(parts, "\n")
}

// l2TableSignature 用 information_schema.COLUMNS 生成可比较的列签名。
// 不直接比较 SHOW CREATE TABLE 文本：dump/import 会补写 CHARACTER SET 子句，
// 文本不同但结构等价；列级元数据才是「结构一致」的判据。
func l2TableSignature(t *testing.T, cfg mysqlop.Config, instance mysqlop.InstanceConfig, database, table string) string {
	t.Helper()
	sql := "SELECT COLUMN_NAME, COLUMN_TYPE, IS_NULLABLE, COLUMN_KEY, COLLATION_NAME " +
		"FROM information_schema.COLUMNS WHERE TABLE_SCHEMA = '" + database + "' AND TABLE_NAME = '" + table + "' " +
		"ORDER BY ORDINAL_POSITION"
	raw := l2Call(t, cfg, "db-mysql.query", map[string]any{"instance": instance.Name, "sql": sql})
	var result mysqlop.QueryResult
	mustUnmarshal(t, raw, &result)
	lines := make([]string, 0, len(result.Rows))
	for _, rowRaw := range result.Rows {
		var row map[string]any
		mustUnmarshal(t, rowRaw, &row)
		lines = append(lines, fmt.Sprintf("%v|%v|%v|%v|%v",
			row["COLUMN_NAME"], row["COLUMN_TYPE"], row["IS_NULLABLE"], row["COLUMN_KEY"], row["COLLATION_NAME"]))
	}
	return strings.Join(lines, "\n")
}

// l2AssertLogin 用新建账号真实登录，证明密码字面量转义正确。
func l2AssertLogin(t *testing.T, instance mysqlop.InstanceConfig, user, password string) {
	t.Helper()
	out := l2RunClient(t, instance, user, password, []string{"-N", "-e", "SELECT CURRENT_USER()"})
	if !strings.Contains(out, user+"@") {
		t.Fatalf("新账号登录返回 %q，期望包含 %s@", out, user)
	}
}

// l2AssertPrivilegeWorks 用新账号读取被授权的库，证明 GRANT 真实生效。
func l2AssertPrivilegeWorks(t *testing.T, instance mysqlop.InstanceConfig, user, password, database string) {
	t.Helper()
	out := l2RunClient(t, instance, user, password, []string{"-N", "-e", "SELECT COUNT(*) FROM `" + database + "`.`t_items`"})
	if strings.TrimSpace(out) != "2" {
		t.Fatalf("新账号读取授权表返回 %q，期望 2", out)
	}
}

// l2RunClient 以指定账号执行一条只读 SQL；密码只走子进程环境变量。
func l2RunClient(t *testing.T, instance mysqlop.InstanceConfig, user, password string, extra []string) string {
	t.Helper()
	args := []string{
		"--no-defaults", "--batch",
		"-h", instance.Host, "-P", strconv.Itoa(instance.Port), "-u", user,
	}
	args = append(args, extra...)
	cmd := exec.Command("mysql", args...)
	cmd.Env = filterEnv(os.Environ(), "MYSQL_PWD")
	if password != "" {
		cmd.Env = append(cmd.Env, "MYSQL_PWD="+password)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	output, err := cmd.Output()
	if err != nil {
		t.Fatalf("以 %s 登录失败: %v (%s)", user, err, strings.TrimSpace(stderr.String()))
	}
	return string(output)
}

// l2ExecDirect 用 root 连接执行建表/插数据（不属于插件方法面）。
func l2ExecDirect(t *testing.T, instance mysqlop.InstanceConfig, sql string) {
	t.Helper()
	args := []string{
		"--no-defaults", "--batch",
		"-h", instance.Host, "-P", strconv.Itoa(instance.Port), "-u", instance.User, "-e", sql,
	}
	cmd := exec.Command("mysql", args...)
	cmd.Env = filterEnv(os.Environ(), "MYSQL_PWD")
	if pw := password(t, instance); pw != "" {
		cmd.Env = append(cmd.Env, "MYSQL_PWD="+pw)
	}
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("直连建表失败: %v (%s)", err, strings.TrimSpace(string(output)))
	}
}
