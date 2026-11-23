package mysqlop

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/pengtang1024/kore/pkg/pluginapi"
)

// 新密码强度与受保护账号（改密属高风险操作）。
const (
	// MinChangePasswordBytes 是新密码的最小字节数（低于此值回 E_INVALID 并给可操作提示）。
	MinChangePasswordBytes = 8
)

// protectedAccounts 是改密前必须 confirm=true 的账号（root 与 MySQL 内置系统账号）。
var protectedAccounts = map[string]struct{}{
	"root":             {},
	"mysql.sys":        {},
	"mysql.session":    {},
	"mysql.infoschema": {},
	"debian-sys-maint": {},
}

// mysqlErrnoRE 从脱敏后的客户端 stderr 里取 MySQL 错误号（如 "ERROR 1396 (HY000)"）。
var mysqlErrnoRE = regexp.MustCompile(`ERROR\s+(\d+)`)

// changePasswordParams 是 db-mysql.changePassword 的入参形状。
// Confirm 仅在目标为 root/系统账号时必填。
type changePasswordParams struct {
	Instance string `json:"instance"`
	User     string `json:"user"`
	Host     string `json:"host"`
	Password string `json:"password"`
	Confirm  bool   `json:"confirm"`
}

// ChangePasswordResult 是 db-mysql.changePassword 的结果；**不含密码**。
type ChangePasswordResult struct {
	User    string `json:"user"`
	Host    string `json:"host"`
	Changed bool   `json:"changed"`
}

// isProtectedAccount 判断目标账号是否属于 root/MySQL 内置系统账号。
func isProtectedAccount(user string) bool {
	_, ok := protectedAccounts[strings.ToLower(strings.TrimSpace(user))]
	return ok
}

// validateNewPassword 做强度校验：长度下限 + 与账号同名拒绝 + 既有字面量约束。
func validateNewPassword(user, password string) error {
	if err := ValidateAccountPassword(password); err != nil {
		return err
	}
	if len(password) < MinChangePasswordBytes {
		return fmt.Errorf("新密码至少 %d 字节：请加长密码后重试", MinChangePasswordBytes)
	}
	if strings.EqualFold(password, user) {
		return fmt.Errorf("新密码不得与账号名相同：请换一个更强的密码")
	}
	return nil
}

// callChangePassword 处理 db-mysql.changePassword：校验 → 参数绑定 → ALTER USER。
// 顺序固定：能力门控 → 标识符/强度 → 受保护账号 confirm → 执行。
func callChangePassword(raw json.RawMessage, cfg Config, exec Executor) (json.RawMessage, *pluginapi.Error) {
	if perr := requireAdmin(cfg); perr != nil {
		return nil, perr
	}
	var p changePasswordParams
	if perr := decodeParams(raw, &p); perr != nil {
		return nil, perr
	}
	if p.User == "" {
		return nil, invalid("缺少必填参数 user")
	}
	if err := ValidateAccountUser(p.User); err != nil {
		return nil, invalid(err.Error())
	}
	host := p.Host
	if host == "" {
		host = "localhost"
	}
	if err := ValidateAccountHost(host); err != nil {
		return nil, invalid(err.Error())
	}
	if err := validateNewPassword(p.User, p.Password); err != nil {
		return nil, invalid(err.Error())
	}
	if isProtectedAccount(p.User) && !p.Confirm {
		return nil, invalid(fmt.Sprintf(
			"改 %s 账号密码属高风险操作，请传 confirm=true 复核后重试（目标 %s@%s）", p.User, p.User, host))
	}
	instance, perr := resolveInstance(cfg, p.Instance)
	if perr != nil {
		return nil, perr
	}
	changed, err := exec.ChangePassword(instance, p.User, host, p.Password)
	if err != nil {
		return nil, classifyChangePassword(err, p.Password)
	}
	return encode(ChangePasswordResult{User: p.User, Host: host, Changed: changed})
}

// ChangePassword 实现改密：账号存在性 → sql_mode → 模板绑定 → stdin 执行。
// 密码只出现在绑定后的 SQL 文本里（经 stdin），不进 argv / 日志 / 结果。
func (c *Client) ChangePassword(instance InstanceConfig, user, host, password string) (bool, error) {
	if err := ValidateAccountUser(user); err != nil {
		return false, opErrorf(pluginapi.E_INVALID, "%s", err.Error())
	}
	if err := ValidateAccountHost(host); err != nil {
		return false, opErrorf(pluginapi.E_INVALID, "%s", err.Error())
	}
	if err := ValidateAccountPassword(password); err != nil {
		return false, opErrorf(pluginapi.E_INVALID, "%s", err.Error())
	}
	// user/host 已过白名单（不含引号/反斜杠/分号），这里的字符串字面量与既有 CreateUser 同口径。
	count, err := c.scalarInt(instance,
		"SELECT COUNT(*) FROM mysql.user WHERE User = '"+user+"' AND Host = '"+host+"'")
	if err != nil {
		return false, err
	}
	if count == 0 {
		return false, opErrorf(pluginapi.E_NOTFOUND, "用户 %q@%q 不存在", user, host)
	}
	escapes, err := c.backslashEscapes(instance)
	if err != nil {
		return false, err
	}
	statement, bindErr := renderBoundStatement(changePasswordStatement(user, host), []string{password}, escapes)
	if bindErr != nil {
		return false, opErrorf(pluginapi.E_INTERNAL, "改密语句绑定失败: %v", bindErr)
	}
	if err := c.execSQL(instance, statement); err != nil {
		return false, changePasswordError(err, password)
	}
	return true, nil
}

// changePasswordError 把 MySQL 错误号映射为人话协议错误（先把新密码从消息里抹掉）。
func changePasswordError(err error, password string) error {
	var opErr *OpError
	if !errors.As(err, &opErr) {
		return err
	}
	message := opErr.Message
	if password != "" {
		message = strings.ReplaceAll(message, password, "***")
	}
	if match := mysqlErrnoRE.FindStringSubmatch(message); match != nil {
		switch match[1] {
		case "1045":
			return opErrorf(pluginapi.E_UNAUTHORIZED,
				"连接被拒绝（ER_ACCESS_DENIED_ERROR）：实例账号或密码错误，请检查 MySQL 连接设置后重试")
		case "1396":
			return opErrorf(pluginapi.E_NOTFOUND,
				"用户不存在或当前账号无权修改该用户（ER_CANNOT_USER）：请确认 user@host 拼写与权限")
		case "1227", "1142":
			return opErrorf(pluginapi.E_UNAUTHORIZED,
				"当前连接账号权限不足，无法修改该用户密码（ER_SPECIFIC_ACCESS_DENIED_ERROR）：请用有 CREATE USER/UPDATE mysql.user 权限的账号连接")
		case "1819":
			return opErrorf(pluginapi.E_INVALID,
				"新密码不满足服务端密码策略（ER_NOT_VALID_PASSWORD）：请加长密码或增加字符种类")
		}
	}
	return opErrorf(opErr.Code, "%s", message)
}

// classifyChangePassword 把执行面错误映射为协议错误体：MySQL 错误号优先映射人话，
// 其余保留原错误码；无论哪条路径都先抹掉新密码。
func classifyChangePassword(err error, password string) *pluginapi.Error {
	code, message := "", ""
	var opErr *OpError
	if errors.As(err, &opErr) {
		code, message = opErr.Code, opErr.Message
	} else {
		classified := classify(err)
		code, message = classified.Code, classified.Message
	}
	if password != "" {
		message = strings.ReplaceAll(message, password, "***")
	}
	if match := mysqlErrnoRE.FindStringSubmatch(message); match != nil {
		if mapped, ok := mysqlErrnoHints[match[1]]; ok {
			return &pluginapi.Error{Code: mapped.code, Message: mapped.message}
		}
	}
	return &pluginapi.Error{Code: code, Message: message}
}

// mysqlErrnoHint 是 MySQL 错误号到协议错误码 + 人话的映射。
type mysqlErrnoHint struct {
	code    string
	message string
}

// mysqlErrnoHints 覆盖改密最常见的四类失败（连接鉴权 / 账号不存在 / 权限 / 密码策略）。
var mysqlErrnoHints = map[string]mysqlErrnoHint{
	"1045": {pluginapi.E_UNAUTHORIZED, "连接被拒绝（ER_ACCESS_DENIED_ERROR）：实例账号或密码错误，请检查 MySQL 连接设置后重试"},
	"1396": {pluginapi.E_NOTFOUND, "用户不存在或当前账号无权修改该用户（ER_CANNOT_USER）：请确认 user@host 拼写与权限"},
	"1227": {pluginapi.E_UNAUTHORIZED, "当前连接账号权限不足，无法修改该用户密码（ER_SPECIFIC_ACCESS_DENIED_ERROR）：请换有 CREATE USER 权限的账号连接"},
	"1142": {pluginapi.E_UNAUTHORIZED, "当前连接账号权限不足，无法修改该用户密码（ER_SPECIFIC_ACCESS_DENIED_ERROR）：请换有 CREATE USER 权限的账号连接"},
	"1819": {pluginapi.E_INVALID, "新密码不满足服务端密码策略（ER_NOT_VALID_PASSWORD）：请加长密码或增加字符种类"},
}
