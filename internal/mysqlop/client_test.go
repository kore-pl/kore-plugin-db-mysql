package mysqlop

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pengtang1024/kore/pkg/pluginapi"
)

func testInstance() InstanceConfig {
	return InstanceConfig{Name: "primary", Host: "127.0.0.1", Port: 13306, User: "root", PasswordEnv: "PRIMARY_PASSWORD"}
}

// TestCommandArgsNoPassword 密码不进 argv；--no-defaults 必须在首位。
func TestCommandArgsNoPassword(t *testing.T) {
	client := NewClient(Config{})
	args := client.args(testInstance(), "SELECT 1", false, false)
	if args[0] != "--no-defaults" {
		t.Fatalf("首个参数=%q，期望 --no-defaults", args[0])
	}
	joined := strings.Join(args, " ")
	for _, needle := range []string{"--batch", "-h 127.0.0.1", "-P 13306", "-u root", "-e SELECT 1"} {
		if !strings.Contains(joined, needle) {
			t.Fatalf("参数缺少 %q: %v", needle, args)
		}
	}
	if strings.Contains(joined, "--password") || strings.Contains(joined, "PRIMARY_PASSWORD") {
		t.Fatalf("argv 疑似携带密码: %v", args)
	}
	if args[len(args)-2] != "-e" || args[len(args)-1] != "SELECT 1" {
		t.Fatalf("SQL 不是最后一个参数: %v", args)
	}
	if strings.Contains(joined, "--raw") {
		t.Fatalf("用户数据查询不应启用 --raw（会关闭字段转义）: %v", args)
	}
}

// TestCommandArgsMetadataRaw 元数据查询允许 --raw + --skip-column-names。
func TestCommandArgsMetadataRaw(t *testing.T) {
	client := NewClient(Config{})
	joined := strings.Join(client.args(testInstance(), "SELECT 1, VERSION()", true, true), " ")
	for _, needle := range []string{"--raw", "--skip-column-names", "--batch"} {
		if !strings.Contains(joined, needle) {
			t.Fatalf("参数缺少 %q: %s", needle, joined)
		}
	}
}

// TestBuildCommandEnv 只透传最小环境 + 当前实例 MYSQL_PWD，不带其它实例密码。
func TestBuildCommandEnv(t *testing.T) {
	t.Setenv("KORE_MYSQL_INSTANCES", `[{"name":"other","host":"h","password_env":"OTHER"}]`)
	t.Setenv("OTHER", "other-secret")
	env := strings.Join(buildCommandEnv("current-secret"), "\n")
	if !strings.Contains(env, "MYSQL_PWD=current-secret") {
		t.Fatalf("MYSQL_PWD 未注入: %s", env)
	}
	if strings.Contains(env, "other-secret") || strings.Contains(env, "KORE_MYSQL_INSTANCES") {
		t.Fatalf("子进程环境泄漏其它实例配置: %s", env)
	}
	if env := strings.Join(buildCommandEnv(""), "\n"); strings.Contains(env, "MYSQL_PWD") {
		t.Fatalf("空密码不应注入 MYSQL_PWD: %s", env)
	}
}

// TestSanitizeStderr 过滤 WARNING、脱敏密码、截断长度。
func TestSanitizeStderr(t *testing.T) {
	raw := "WARNING: option --ssl-verify-server-cert is disabled\nERROR 1045: Access denied for wrong-pass\n"
	got := sanitizeStderr(raw, "wrong-pass")
	if strings.Contains(got, "WARNING") || strings.Contains(got, "wrong-pass") {
		t.Fatalf("未过滤/脱敏: %q", got)
	}
	if !strings.Contains(got, "Access denied") || !strings.Contains(got, "***") {
		t.Fatalf("错误信息丢失: %q", got)
	}
	long := sanitizeStderr(strings.Repeat("x", 2000))
	if !strings.HasSuffix(long, "…") || len([]rune(long)) > 501 {
		t.Fatalf("错误信息未截断: %d", len([]rune(long)))
	}
}

// TestClientMissingBinary 客户端不存在返回 E_UNSUPPORTED。
func TestClientMissingBinary(t *testing.T) {
	client := NewClient(Config{Bin: "/nonexistent/mysql-client"})
	_, err := client.Instances([]InstanceConfig{testInstance()})
	if err == nil {
		t.Fatal("缺少客户端未报错")
	}
	var op *OpError
	if !errors.As(err, &op) || op.Code != pluginapi.E_UNSUPPORTED {
		t.Fatalf("错误=%v，期望 E_UNSUPPORTED", err)
	}
}

// TestQueryMissingPasswordEnv 密码环境变量缺失返回 E_INVALID，且不启动客户端。
func TestQueryMissingPasswordEnv(t *testing.T) {
	t.Setenv("PRIMARY_PASSWORD", "")
	client := NewClient(Config{Bin: "/nonexistent/mysql-client"})
	_, err := client.Query(testInstance(), "SELECT 1", 10)
	var op *OpError
	if !errors.As(err, &op) || op.Code != pluginapi.E_INVALID {
		t.Fatalf("错误=%v，期望 E_INVALID（密码环境变量缺失）", err)
	}
}

// TestQueryTimeout 命令超时后终止客户端并返回 E_UPSTREAM。
func TestQueryTimeout(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "mysql")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nexec sleep 5\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	client := NewClient(Config{Bin: script, Timeout: 100 * time.Millisecond})
	start := time.Now()
	_, err := client.Query(InstanceConfig{Name: "primary", Host: "127.0.0.1", Port: 3306, User: "root"}, "SELECT 1", 10)
	var op *OpError
	if !errors.As(err, &op) || op.Code != pluginapi.E_UPSTREAM || !strings.Contains(op.Message, "超时") {
		t.Fatalf("错误=%v，期望超时 E_UPSTREAM", err)
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Fatalf("超时未及时生效: %s", elapsed)
	}
}

// TestReadQueryStream 流式解析：NULL、制表符、行数上限与 truncated。
func TestReadQueryStream(t *testing.T) {
	output := "n\ts\tt\n1\tNULL\ta\\tb\n2\tx\ty\n3\tz\tw\n"
	result, err := readQueryStream(strings.NewReader(output), 2, func() {})
	if err != nil {
		t.Fatalf("readQueryStream 失败: %v", err)
	}
	if len(result.Columns) != 3 || result.Columns[0] != "n" {
		t.Fatalf("列=%v", result.Columns)
	}
	if result.RowCount != 2 || len(result.Rows) != 2 || !result.Truncated {
		t.Fatalf("结果=%+v，期望 2 行且 truncated", result)
	}
	var first map[string]any
	if err := json.Unmarshal(result.Rows[0], &first); err != nil {
		t.Fatalf("行 JSON 非法: %v", err)
	}
	if first["n"] != float64(1) || first["s"] != nil || first["t"] != "a\tb" {
		t.Fatalf("首行=%v", first)
	}
}

// TestReadQueryStreamExactLimit 行数正好等于 limit 时不算截断。
func TestReadQueryStreamExactLimit(t *testing.T) {
	result, err := readQueryStream(strings.NewReader("n\n1\n2\n"), 2, func() {})
	if err != nil {
		t.Fatalf("readQueryStream 失败: %v", err)
	}
	if result.RowCount != 2 || result.Truncated {
		t.Fatalf("结果=%+v，期望 2 行且未截断", result)
	}
}

// TestReadQueryStreamEmptyRows 零行结果仍返回列名。
func TestReadQueryStreamEmptyRows(t *testing.T) {
	result, err := readQueryStream(strings.NewReader("n\ts\n"), 10, func() {})
	if err != nil {
		t.Fatalf("readQueryStream 失败: %v", err)
	}
	if result.RowCount != 0 || len(result.Rows) != 0 || len(result.Columns) != 2 {
		t.Fatalf("结果=%+v", result)
	}
	if result.Rows == nil || result.Columns == nil {
		t.Fatal("空结果应返回非 nil 切片，保证 JSON 是 [] 而不是 null")
	}
}
