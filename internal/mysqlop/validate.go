package mysqlop

import (
	"errors"
	"regexp"
	"strings"
)

// 实例名：小写字母/数字开头，仅含小写字母、数字、点与连字符，≤64 字符。
var instanceNameRE = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]{0,63}$`)

// 主机：允许主机名 / IPv4 / IPv6 字面量与端口无关的字符集；不允许空白、控制字符，
// 也不允许以 "-" 开头（避免被 mysql 客户端当成命令行选项）。
var hostRE = regexp.MustCompile(`^[A-Za-z0-9._:%-]+$`)

// 用户名：常见 MySQL 用户名形态；同样禁止 "-" 开头，避免 argv 选项注入。
var userRE = regexp.MustCompile(`^[A-Za-z0-9._@%:-]+$`)

// 密码来源环境变量名。
var passwordEnvRE = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,127}$`)

// systemDatabases 是 mysql.databases 默认过滤的系统库。
var systemDatabases = map[string]struct{}{
	"information_schema": {},
	"mysql":              {},
	"performance_schema": {},
	"sys":                {},
}

// ValidateInstance 校验实例名，返回协议错误（E_INVALID）。
func ValidateInstance(name string) error {
	return validateInstanceName(name)
}

// validateInstanceName 校验实例名，配置解析与请求参数共用。
func validateInstanceName(name string) error {
	if name == "" {
		return errors.New("实例名不得为空")
	}
	if strings.ContainsRune(name, 0) {
		return errors.New("实例名含 NUL 字节")
	}
	if strings.ContainsAny(name, "/\\") {
		return errors.New("实例名不得含路径分隔符")
	}
	if !instanceNameRE.MatchString(name) {
		return errors.New("实例名非法：须以小写字母/数字开头，仅含小写字母、数字、. 与 -，且不超过 64 字符")
	}
	if strings.Contains(name, "..") {
		return errors.New("实例名含 .. 段，禁止穿越")
	}
	return nil
}

// validateHost 校验实例主机，拒绝 argv 选项注入与不可打印字符。
func validateHost(host string) error {
	if host == "" {
		return errors.New("host 不得为空")
	}
	if len(host) > 253 {
		return errors.New("host 超过 253 字符")
	}
	if strings.HasPrefix(host, "-") {
		return errors.New("host 不得以 - 开头（防止被当作客户端选项）")
	}
	if !hostRE.MatchString(host) {
		return errors.New("host 含非法字符：仅允许字母、数字、. - _ : %")
	}
	return nil
}

// validateUser 校验用户名，拒绝 argv 选项注入。
func validateUser(user string) error {
	if user == "" {
		return errors.New("user 不得为空")
	}
	if len(user) > 128 {
		return errors.New("user 超过 128 字符")
	}
	if strings.HasPrefix(user, "-") {
		return errors.New("user 不得以 - 开头（防止被当作客户端选项）")
	}
	if !userRE.MatchString(user) {
		return errors.New("user 含非法字符：仅允许字母、数字、. - _ @ % :")
	}
	return nil
}
