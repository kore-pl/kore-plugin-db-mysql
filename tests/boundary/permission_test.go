package boundary

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pengtang1024/kore-plugin-db-mysql/internal/mysqlop"
	"github.com/pengtang1024/kore/pkg/pluginapi"
)

// TestPermissionCapabilityGate 权限不足（插件能力级）：
// 未声明 system:admin 时，所有写操作必须在触达执行面之前返回 E_UNAUTHORIZED，
// 且错误信息指出缺失的能力——不是笼统的失败。
func TestPermissionCapabilityGate(t *testing.T) {
	cfg := boundaryConfig("/bin/false", "/bin/false", t.TempDir())
	cfg.Admin = false

	cases := []struct {
		name   string
		method string
		params string
	}{
		{"createDatabase", "db-mysql.createDatabase", `{"instance":"primary","name":"kore_perm"}`},
		{"dropDatabase", "db-mysql.dropDatabase", `{"instance":"primary","name":"kore_perm","confirm":true}`},
		{"createUser", "db-mysql.createUser", `{"instance":"primary","user":"kore_perm_user"}`},
		{"changePassword", "db-mysql.changePassword", `{"instance":"primary","user":"kore_perm_user","password":"Str0ngPass!"}`},
		{"dropUser", "db-mysql.dropUser", `{"instance":"primary","user":"kore_perm_user","confirm":true}`},
		{"grant", "db-mysql.grant", `{"instance":"primary","user":"kore_perm_user","database":"kore_perm","privileges":["SELECT"]}`},
		{"export", "db-mysql.export", `{"instance":"primary","database":"kore_perm","file_path":"perm.sql"}`},
		{"import", "db-mysql.import", `{"instance":"primary","database":"kore_perm","file_path":"perm.sql"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			exec := &tripwireExecutor{}
			_, perr := mysqlop.Call(tc.method, json.RawMessage(tc.params), cfg, exec)
			if perr == nil || perr.Code != pluginapi.E_UNAUTHORIZED {
				t.Fatalf("错误=%v，期望 E_UNAUTHORIZED", perr)
			}
			if !strings.Contains(perr.Message, "system:admin") {
				t.Fatalf("错误信息 %q 未指出缺失的 system:admin 能力", perr.Message)
			}
			if exec.calls != 0 {
				t.Fatalf("权限不足仍在执行面调用了 %d 次，期望 0 次", exec.calls)
			}
		})
	}
}

// TestPermissionConfigFileUnreadable 权限不足（配置文件级）：
// KORE_MYSQL_CONFIG 指向 chmod 000 的文件时，配置加载必须保留 EACCES 语义，
// 方法层返回明确的 E_INVALID（而不是空实例、也不是崩溃）。
func TestPermissionConfigFileUnreadable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "instances.json")
	if err := os.WriteFile(path, []byte(`[]`), 0o600); err != nil {
		t.Fatalf("写配置文件失败: %v", err)
	}
	if err := os.Chmod(path, 0o000); err != nil {
		t.Fatalf("chmod 000 失败: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(path, 0o600) })

	// 注入有效性自检：当前用户必须真的读不到（root / ACL 环境会读成功，此时跳过而不是假装通过）。
	if _, err := os.ReadFile(path); err == nil {
		t.Skip("当前用户可读 chmod 000 文件（root/ACL 环境），无法注入 EACCES")
	} else if !errors.Is(err, fs.ErrPermission) {
		t.Fatalf("读 000 文件的错误=%v，期望 EACCES", err)
	}

	t.Setenv(mysqlop.EnvInstances, "")
	t.Setenv(mysqlop.EnvConfig, path)
	_, loadErr := mysqlop.LoadConfig()
	if loadErr == nil {
		t.Fatal("配置文件不可读时 LoadConfig 应返回错误")
	}
	if !errors.Is(loadErr, fs.ErrPermission) {
		t.Fatalf("LoadConfig 错误=%v，未保留 EACCES 语义", loadErr)
	}

	// 方法面：配置错误收口为 E_INVALID，且不触达执行面。
	exec := &tripwireExecutor{}
	_, perr := mysqlop.Call("db-mysql.query", json.RawMessage(`{"instance":"primary","sql":"SELECT 1"}`),
		mysqlop.Config{Err: loadErr}, exec)
	requireCode(t, perr, pluginapi.E_INVALID, "MySQL 实例配置非法", "permission denied")
	if exec.calls != 0 {
		t.Fatalf("配置非法仍在执行面调用了 %d 次，期望 0 次", exec.calls)
	}
}

// TestPermissionMySQLUserDenied 权限不足（MySQL 账号级）：
// 连接账号无目标库权限时，客户端 stderr 的 1044 必须原样保留在 E_UPSTREAM 里，
// 便于用户定位是权限问题而不是「执行面失败」。
func TestPermissionMySQLUserDenied(t *testing.T) {
	cfg := boundaryConfig(writeScript(t, t.TempDir(), "mysql", denyMySQLScript), "", "")

	_, perr := mysqlop.Call("db-mysql.query",
		json.RawMessage(`{"instance":"primary","sql":"SELECT 1"}`), cfg, nil)
	requireCode(t, perr, pluginapi.E_UPSTREAM, "1044", "Access denied")

	_, perr = mysqlop.Call("db-mysql.databases",
		json.RawMessage(`{"instance":"primary"}`), cfg, nil)
	requireCode(t, perr, pluginapi.E_UPSTREAM, "1044", "Access denied")
}

// TestPermissionChangePasswordDeniedMapping 权限不足的改密映射：
// MySQL 1142（ALTER 被拒）必须映射为 E_UNAUTHORIZED + 可操作提示，且不得回显新密码。
func TestPermissionChangePasswordDeniedMapping(t *testing.T) {
	cfg := boundaryConfig(writeScript(t, t.TempDir(), "mysql", deniedChangeScript), "", "")
	const password = "Str0ngPass!"

	_, perr := mysqlop.Call("db-mysql.changePassword",
		json.RawMessage(fmt.Sprintf(`{"instance":"primary","user":"app","password":%q}`, password)), cfg, nil)
	requireCode(t, perr, pluginapi.E_UNAUTHORIZED, "权限不足")
	if strings.Contains(perr.Message, password) {
		t.Fatalf("错误信息泄漏了新密码: %q", perr.Message)
	}
}
