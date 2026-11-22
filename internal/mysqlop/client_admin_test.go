package mysqlop

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pengtang1024/kore/pkg/pluginapi"
)

// TestSQLBuildersIdentifiers 所有标识符都带反引号，账号用 'u'@'h' 字面量。
func TestSQLBuildersIdentifiers(t *testing.T) {
	if got := createDatabaseSQL("app_db", "utf8mb4", "utf8mb4_unicode_ci"); got !=
		"CREATE DATABASE IF NOT EXISTS `app_db` CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;" {
		t.Fatalf("建库 SQL=%q", got)
	}
	if got := createDatabaseSQL("order", "", ""); got != "CREATE DATABASE IF NOT EXISTS `order`;" {
		t.Fatalf("保留字库名未被反引号包裹: %q", got)
	}
	if got := dropDatabaseSQL("app_db"); got != "DROP DATABASE `app_db`;" {
		t.Fatalf("删库 SQL=%q", got)
	}
	if got := createUserSQL("app_user", "localhost", "s3cr3t", true); got !=
		"CREATE USER 'app_user'@'localhost' IDENTIFIED BY 's3cr3t';" {
		t.Fatalf("建用户 SQL=%q", got)
	}
	if got := createUserSQL("app_user", "%", "", true); got != "CREATE USER 'app_user'@'%';" {
		t.Fatalf("无密码建用户 SQL=%q", got)
	}
	if got := dropUserSQL("app_user", "%"); got != "DROP USER 'app_user'@'%';" {
		t.Fatalf("删用户 SQL=%q", got)
	}
	if got := grantSQL("app_user", "localhost", "app_db", []string{"SELECT", "INSERT"}); got !=
		"GRANT SELECT,INSERT ON `app_db`.* TO 'app_user'@'localhost';" {
		t.Fatalf("授权 SQL=%q", got)
	}
}

// TestEscapeSQLString 单引号/反斜杠在两种 sql_mode 下的编码。
func TestEscapeSQLString(t *testing.T) {
	cases := []struct {
		in        string
		backslash bool
		want      string
	}{
		{`plain`, true, `plain`},
		{`a'b`, true, `a''b`},
		{`a\\b`, true, `a\\\\b`},
		{`a\\b`, false, `a\\b`},
		{`a'b\\c`, false, `a''b\\c`},
	}
	for _, c := range cases {
		if got := escapeSQLString(c.in, c.backslash); got != c.want {
			t.Fatalf("escapeSQLString(%q, %v)=%q，期望 %q", c.in, c.backslash, got, c.want)
		}
	}
}

// TestStdinArgsNoPasswordAndNoExecute 写操作走 stdin：argv 里既无 -e 也无 -p。
func TestStdinArgsNoPasswordAndNoExecute(t *testing.T) {
	client := NewClient(Config{})
	args := client.stdinArgs(testInstance())
	joined := strings.Join(args, " ")
	if strings.Contains(joined, "-e ") || strings.Contains(joined, "--password") ||
		strings.Contains(joined, " -p") {
		t.Fatalf("argv 含 SQL/密码入口: %v", args)
	}
	for _, needle := range []string{"--no-defaults", "--batch", "-h 127.0.0.1", "-P 13306", "-u root"} {
		if !strings.Contains(joined, needle) {
			t.Fatalf("argv 缺少 %q: %v", needle, args)
		}
	}
}

// TestRunStdinKeepsPasswordOutOfArgv 密码只出现在 stdin，不出现在 argv。
func TestRunStdinKeepsPasswordOutOfArgv(t *testing.T) {
	dir := t.TempDir()
	argsFile := filepath.Join(dir, "args.txt")
	stdinFile := filepath.Join(dir, "stdin.txt")
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > '" + argsFile + "'\ncat > '" + stdinFile + "'\n"
	bin := filepath.Join(dir, "mysql")
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	client := NewClient(Config{Bin: bin})
	instance := InstanceConfig{Name: "primary", Host: "127.0.0.1", Port: 3306, User: "root", Password: "s3cr3t-pwd"}
	statement := "CREATE USER 'app_user'@'localhost' IDENTIFIED BY 's3cr3t-pwd';"
	if _, err := client.runStdin(instance, statement); err != nil {
		t.Fatalf("runStdin 失败: %v", err)
	}
	argv, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatalf("读取 argv 记录失败: %v", err)
	}
	argvText := string(argv)
	if strings.Contains(argvText, "s3cr3t-pwd") {
		t.Fatalf("密码出现在 argv: %s", argvText)
	}
	if strings.Contains(argvText, "-e") || strings.Contains(argvText, "--password") {
		t.Fatalf("argv 出现 -e/--password: %s", argvText)
	}
	stdin, err := os.ReadFile(stdinFile)
	if err != nil {
		t.Fatalf("读取 stdin 记录失败: %v", err)
	}
	if string(stdin) != statement {
		t.Fatalf("SQL 未通过 stdin 传入: %q", stdin)
	}
}

// TestDumpArgsFixedOrder dump 固定 argv：库名/表名只在末尾位置参数位。
func TestDumpArgsFixedOrder(t *testing.T) {
	client := NewClient(Config{})
	args := client.dumpArgs(testInstance(), "app_db", []string{"t1", "t2"})
	joined := strings.Join(args, " ")
	for _, needle := range []string{"--no-defaults", "--single-transaction", "--quick", "-h 127.0.0.1", "-P 13306", "-u root"} {
		if !strings.Contains(joined, needle) {
			t.Fatalf("dump argv 缺少 %q: %v", needle, args)
		}
	}
	if args[len(args)-3] != "app_db" || args[len(args)-2] != "t1" || args[len(args)-1] != "t2" {
		t.Fatalf("库名/表名不在末尾位置参数位: %v", args)
	}
	if strings.Contains(joined, "--password") || strings.Contains(joined, "--connect-timeout") {
		t.Fatalf("dump argv 含密码入口或不支持的选项: %v", args)
	}
}

// TestImportArgsDatabaseFlag import 用 --database 指定库，SQL 走 stdin。
func TestImportArgsDatabaseFlag(t *testing.T) {
	client := NewClient(Config{})
	args := client.importArgs(testInstance(), "app_db")
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "--database=app_db") {
		t.Fatalf("import argv 缺少 --database: %v", args)
	}
	if strings.Contains(joined, "-e ") || strings.Contains(joined, "--password") {
		t.Fatalf("import argv 含 SQL/密码入口: %v", args)
	}
}

// TestNewClientDefaults mysql/mysqldump 缺省路径与超时补齐。
func TestNewClientDefaults(t *testing.T) {
	client := NewClient(Config{})
	if client.bin != defaultBin || client.dumpBin != defaultDumpBin || client.timeout != defaultTimeout {
		t.Fatalf("缺省值错: bin=%q dump=%q timeout=%s", client.bin, client.dumpBin, client.timeout)
	}
	custom := NewClient(Config{Bin: "/opt/mysql", DumpBin: "/opt/mysqldump"})
	if custom.bin != "/opt/mysql" || custom.dumpBin != "/opt/mysqldump" {
		t.Fatalf("自定义路径未生效: %+v", custom)
	}
}

// TestClientAdminRejectsBadIdentifiers 执行面自身也做白名单校验（防绕过方法层）。
func TestClientAdminRejectsBadIdentifiers(t *testing.T) {
	client := NewClient(Config{})
	instance := InstanceConfig{Name: "primary", Host: "127.0.0.1", Port: 3306, User: "root"}
	cases := []struct {
		name string
		err  error
	}{
		{"bad database", func() error { _, err := client.CreateDatabase(instance, "a;b", "utf8mb4", ""); return err }()},
		{"bad charset", func() error { _, err := client.CreateDatabase(instance, "app", "utf8mb4;x", ""); return err }()},
		{"bad user", func() error { _, err := client.CreateUser(instance, "u'--", "localhost", ""); return err }()},
		{"bad host", func() error { _, err := client.DropUser(instance, "app", "h h"); return err }()},
		{"bad grant", func() error { return client.Grant(instance, "app", "localhost", "`x`", []string{"SELECT"}) }()},
	}
	for _, c := range cases {
		var op *OpError
		if !asOpError(c.err, &op) || op.Code != pluginapi.E_INVALID {
			t.Fatalf("%s 错误=%v，期望 E_INVALID", c.name, c.err)
		}
	}
}

// asOpError 是 errors.As 的本地薄封装（避免测试文件再引 errors 与类型断言噪音）。
func asOpError(err error, target **OpError) bool {
	op, ok := err.(*OpError)
	if ok {
		*target = op
	}
	return ok
}
