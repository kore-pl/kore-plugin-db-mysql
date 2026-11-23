package mysqlop

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/pengtang1024/kore/pkg/pluginapi"
)

const testNewPassword = "Str0ng-New-Passw0rd!"

// TestChangePasswordStatementBindsPassword 是参数化断言：模板不含密码，绑定后密码只在
// 单引号字面量里出现一次，且引号/反斜杠按服务端 sql_mode 安全转义。
func TestChangePasswordStatementBindsPassword(t *testing.T) {
	template := changePasswordStatement("app_user", "localhost")
	if strings.Contains(template, testNewPassword) {
		t.Fatalf("改密模板泄漏密码: %q", template)
	}
	if !strings.Contains(template, "ALTER USER 'app_user'@'localhost' IDENTIFIED BY ?") {
		t.Fatalf("改密模板形状不符: %q", template)
	}

	bound, err := renderBoundStatement(template, []string{testNewPassword}, true)
	if err != nil {
		t.Fatalf("绑定失败: %v", err)
	}
	if strings.Count(bound, testNewPassword) != 1 {
		t.Fatalf("绑定后密码出现次数=%d，期望 1: %q", strings.Count(bound, testNewPassword), bound)
	}
	if !strings.HasSuffix(bound, "';") {
		t.Fatalf("绑定结果不是完整字面量语句: %q", bound)
	}

	// 引号必须写成两个单引号；反斜杠在默认 sql_mode 下双写、在 NO_BACKSLASH_ESCAPES 下保持原样。
	quoted, err := renderBoundStatement("X ?", []string{`a'b\c`}, true)
	if err != nil || quoted != `X 'a''b\\c'` {
		t.Fatalf("默认 sql_mode 转义不符: %q err=%v", quoted, err)
	}
	plain, err := renderBoundStatement("X ?", []string{`a'b\c`}, false)
	if err != nil || plain != `X 'a''b\c'` {
		t.Fatalf("NO_BACKSLASH_ESCAPES 转义不符: %q err=%v", plain, err)
	}

	// 占位符与绑定值数量不一致必须拒绝（不允许半绑定）。
	if _, err := renderBoundStatement("A ? B ?", []string{"x"}, true); err == nil {
		t.Fatal("占位符多于绑定值时应报错")
	}
	if _, err := renderBoundStatement("A ?", []string{"x", "y"}, true); err == nil {
		t.Fatal("绑定值多于占位符时应报错")
	}
}

// TestChangePasswordRejectsIdentifierInjection 覆盖含引号/分号/反引号的 user/host 被拒。
func TestChangePasswordRejectsIdentifierInjection(t *testing.T) {
	exec := &fakeExecutor{created: true}
	cases := []struct {
		name   string
		params string
	}{
		{"user 含单引号", `{"user":"app'--","password":"Str0ngPass!","confirm":true}`},
		{"user 含分号", `{"user":"app;DROP USER root","password":"Str0ngPass!","confirm":true}`},
		{"user 含反引号", "{\"user\":\"app`x\",\"password\":\"Str0ngPass!\",\"confirm\":true}"},
		{"host 含单引号", `{"user":"app","host":"local'host","password":"Str0ngPass!","confirm":true}`},
		{"host 含分号", `{"user":"app","host":"localhost;--","password":"Str0ngPass!","confirm":true}`},
	}
	for _, c := range cases {
		_, perr := Call("db-mysql.changePassword", json.RawMessage(c.params), adminTestConfig(t), exec)
		if perr == nil || perr.Code != pluginapi.E_INVALID {
			t.Fatalf("%s 错误=%+v，期望 E_INVALID", c.name, perr)
		}
	}
	if exec.adminCalls != 0 {
		t.Fatalf("非法标识符不应触达执行面: calls=%d", exec.adminCalls)
	}
}

// TestChangePasswordProtectedAccountNeedsConfirm 覆盖 root/系统账号缺 confirm 拒绝（带 hint）。
func TestChangePasswordProtectedAccountNeedsConfirm(t *testing.T) {
	// 注意：mysql.sys / debian-sys-maint 含 . 与 -，会先被标识符白名单拒绝（更严格），
	// 能走到 confirm 分支的受保护账号是 root。
	for _, user := range []string{"root"} {
		exec := &fakeExecutor{created: true}
		raw := json.RawMessage(`{"user":"` + user + `","password":"Str0ngPass!"}`)
		_, perr := Call("db-mysql.changePassword", raw, adminTestConfig(t), exec)
		if perr == nil || perr.Code != pluginapi.E_INVALID {
			t.Fatalf("%s 缺 confirm 错误=%+v，期望 E_INVALID", user, perr)
		}
		if !strings.Contains(perr.Message, "confirm=true") || !strings.Contains(perr.Message, user) {
			t.Fatalf("%s 缺 confirm 提示不可操作: %q", user, perr.Message)
		}
		if exec.adminCalls != 0 {
			t.Fatalf("%s 缺 confirm 时不应触达执行面", user)
		}
	}

	// 含点/连字符的系统账号直接被标识符白名单拒绝。
	for _, user := range []string{"mysql.sys", "debian-sys-maint"} {
		exec := &fakeExecutor{created: true}
		_, perr := Call("db-mysql.changePassword",
			json.RawMessage(`{"user":"`+user+`","password":"Str0ngPass!","confirm":true}`), adminTestConfig(t), exec)
		if perr == nil || perr.Code != pluginapi.E_INVALID {
			t.Fatalf("系统账号 %s 错误=%+v，期望 E_INVALID", user, perr)
		}
	}

	// 普通账号无需 confirm。
	exec := &fakeExecutor{created: true}
	if _, perr := Call("db-mysql.changePassword",
		json.RawMessage(`{"user":"app_user","password":"Str0ngPass!"}`), adminTestConfig(t), exec); perr != nil {
		t.Fatalf("普通账号改密失败: %v", perr)
	}
	// root 带 confirm 放行。
	exec = &fakeExecutor{created: true}
	if _, perr := Call("db-mysql.changePassword",
		json.RawMessage(`{"user":"root","password":"Str0ngPass!","confirm":true}`), adminTestConfig(t), exec); perr != nil {
		t.Fatalf("root 带 confirm 改密失败: %v", perr)
	}
}

// TestChangePasswordWeakPassword 覆盖强度校验（长度下限、与账号同名）。
func TestChangePasswordWeakPassword(t *testing.T) {
	exec := &fakeExecutor{created: true}
	for _, password := range []string{"short", "app_user"} {
		_, perr := Call("db-mysql.changePassword",
			json.RawMessage(`{"user":"app_user","password":"`+password+`"}`), adminTestConfig(t), exec)
		if perr == nil || perr.Code != pluginapi.E_INVALID {
			t.Fatalf("弱密码 %q 错误=%+v，期望 E_INVALID", password, perr)
		}
		if !strings.Contains(perr.Message, "至少 8") && !strings.Contains(perr.Message, "不得与账号名相同") {
			t.Fatalf("弱密码提示不可操作: %q", perr.Message)
		}
	}
}

// TestChangePasswordSuccessKeepsPasswordSecret 覆盖成功结果形状与密码不落结果/错误。
func TestChangePasswordSuccessKeepsPasswordSecret(t *testing.T) {
	exec := &fakeExecutor{created: true}
	raw, perr := Call("db-mysql.changePassword",
		json.RawMessage(`{"user":"app_user","password":"`+testNewPassword+`"}`), adminTestConfig(t), exec)
	if perr != nil {
		t.Fatalf("改密失败: %v", perr)
	}
	if strings.Contains(string(raw), testNewPassword) {
		t.Fatalf("响应含明文密码: %s", raw)
	}
	var result ChangePasswordResult
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatalf("解析结果失败: %v", err)
	}
	if result.User != "app_user" || result.Host != "localhost" || !result.Changed {
		t.Fatalf("结果不符: %+v", result)
	}
	if exec.adminCalls != 1 || exec.lastPassword != testNewPassword {
		t.Fatalf("执行面入参不符: calls=%d", exec.adminCalls)
	}
}

// TestChangePasswordErrorMapping 覆盖 MySQL 错误号 → 人话错误码映射，且错误里密码被脱敏。
func TestChangePasswordErrorMapping(t *testing.T) {
	cases := []struct {
		name   string
		stderr string
		code   string
		want   string
	}{
		{"ER_CANNOT_USER", "ERROR 1396 (HY000): Operation ALTER USER failed for 'ghost'@'localhost'", pluginapi.E_NOTFOUND, "ER_CANNOT_USER"},
		{"ER_ACCESS_DENIED", "ERROR 1045 (28000): Access denied for user 'root'@'localhost'", pluginapi.E_UNAUTHORIZED, "ER_ACCESS_DENIED_ERROR"},
		{"密码策略", "ERROR 1819 (HY000): Your password does not satisfy the current policy requirements", pluginapi.E_INVALID, "ER_NOT_VALID_PASSWORD"},
		{"权限不足", "ERROR 1227 (42000): Access denied; you need (at least one of) the CREATE USER privilege(s)", pluginapi.E_UNAUTHORIZED, "权限不足"},
	}
	for _, c := range cases {
		exec := &fakeExecutor{created: false, err: opErrorf(pluginapi.E_UPSTREAM, "%s", "mysql 客户端失败: "+c.stderr)}
		_, perr := Call("db-mysql.changePassword",
			json.RawMessage(`{"user":"app_user","password":"`+testNewPassword+`"}`), adminTestConfig(t), exec)
		if perr == nil || perr.Code != c.code {
			t.Fatalf("%s 错误=%+v，期望 %s", c.name, perr, c.code)
		}
		if !strings.Contains(perr.Message, c.want) {
			t.Fatalf("%s 提示=%q，期望包含 %q", c.name, perr.Message, c.want)
		}
		if strings.Contains(perr.Message, testNewPassword) {
			t.Fatalf("%s 错误消息泄漏密码: %q", c.name, perr.Message)
		}
	}
}

// TestChangePasswordRequiresAdmin 断言未声明 system:admin 时在执行面之前拒绝。
func TestChangePasswordRequiresAdmin(t *testing.T) {
	cfg := callTestConfig()
	cfg.Admin = false
	exec := &fakeExecutor{created: true}
	_, perr := Call("db-mysql.changePassword",
		json.RawMessage(`{"user":"app_user","password":"Str0ngPass!"}`), cfg, exec)
	if perr == nil || perr.Code != pluginapi.E_UNAUTHORIZED {
		t.Fatalf("非 admin 错误=%+v，期望 E_UNAUTHORIZED", perr)
	}
	if exec.adminCalls != 0 {
		t.Fatal("非 admin 不应触达执行面")
	}
}
