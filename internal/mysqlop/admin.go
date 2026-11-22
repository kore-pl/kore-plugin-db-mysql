package mysqlop

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/pengtang1024/kore/pkg/pluginapi"
)

// L2 日常管理方法：建库/删库、建用户/删用户、授权。
//
// 共同防线（任一缺失都不允许触达数据库）：
//  1. 写操作要求插件自身声明 system:admin（requireAdmin）；
//  2. 标识符过白名单 + 反引号包裹，绝不字符串拼接用户输入的引号；
//  3. 删库/删用户必须 confirm=true，系统库与 root 账号直接 E_UNAUTHORIZED；
//  4. 返回值不含密码；错误信息把密码替换成 *** 后再返回。

// grantPrivileges 是 GRANT 允许的权限白名单（大小写不敏感，统一输出大写）。
// 明确拒绝 ALL PRIVILEGES 与 GRANT OPTION：授权面只放行最小集合。
var grantPrivileges = map[string]struct{}{
	"SELECT":  {},
	"INSERT":  {},
	"UPDATE":  {},
	"DELETE":  {},
	"CREATE":  {},
	"DROP":    {},
	"ALTER":   {},
	"INDEX":   {},
	"EXECUTE": {},
}

// privilegeOrder 只用于错误提示里的可读列表。
const privilegeOrder = "SELECT/INSERT/UPDATE/DELETE/CREATE/DROP/ALTER/INDEX/EXECUTE"

// requireAdmin 是写操作的能力门控：插件 manifest 未声明 system:admin 时拒绝。
// 内核侧同样按 info.json 的 capabilities 授权（hello 声明必须是 manifest 子集）。
func requireAdmin(cfg Config) *pluginapi.Error {
	if cfg.Admin {
		return nil
	}
	return &pluginapi.Error{
		Code:    pluginapi.E_UNAUTHORIZED,
		Message: "写操作需要 system:admin 能力：请确认 info.json.capabilities 已声明并经内核授予",
	}
}

// resolveInstance 解析可选 instance：显式名字优先；否则用 default；只有一个实例时用它。
func resolveInstance(cfg Config, name string) (InstanceConfig, *pluginapi.Error) {
	if name != "" {
		return findInstance(cfg, name)
	}
	if len(cfg.Instances) == 0 {
		return InstanceConfig{}, invalid("未配置 MySQL 实例：请先在设置中填写连接信息")
	}
	for _, instance := range cfg.Instances {
		if instance.Name == "default" {
			return instance, nil
		}
	}
	if len(cfg.Instances) == 1 {
		return cfg.Instances[0], nil
	}
	return InstanceConfig{}, invalid("存在多个实例，请用 instance 指定目标实例")
}

// callCreateDatabase 处理 db-mysql.createDatabase：校验 → CREATE DATABASE IF NOT EXISTS。
func callCreateDatabase(raw json.RawMessage, cfg Config, exec Executor) (json.RawMessage, *pluginapi.Error) {
	if perr := requireAdmin(cfg); perr != nil {
		return nil, perr
	}
	var p createDatabaseParams
	if perr := decodeParams(raw, &p); perr != nil {
		return nil, perr
	}
	if p.Name == "" {
		return nil, invalid("缺少必填参数 name")
	}
	if err := ValidateDatabaseName(p.Name); err != nil {
		return nil, invalid(err.Error())
	}
	charset := p.Charset
	if charset == "" {
		charset = "utf8mb4"
	}
	if err := ValidateCharset(charset); err != nil {
		return nil, invalid(err.Error())
	}
	collation := p.Collation
	if collation == "" && p.Charset == "" {
		collation = "utf8mb4_unicode_ci"
	}
	if collation != "" {
		if err := ValidateCollation(collation); err != nil {
			return nil, invalid(err.Error())
		}
	}
	instance, perr := resolveInstance(cfg, p.Instance)
	if perr != nil {
		return nil, perr
	}
	created, err := exec.CreateDatabase(instance, p.Name, charset, collation)
	if err != nil {
		return nil, classify(err)
	}
	return encode(CreateDatabaseResult{Created: created, Name: p.Name})
}

// callDropDatabase 处理 db-mysql.dropDatabase：系统库保护 → confirm → DROP DATABASE。
// 顺序固定：系统库先于 confirm，保证「删 mysql」在任何参数下都是 E_UNAUTHORIZED。
func callDropDatabase(raw json.RawMessage, cfg Config, exec Executor) (json.RawMessage, *pluginapi.Error) {
	if perr := requireAdmin(cfg); perr != nil {
		return nil, perr
	}
	var p dropDatabaseParams
	if perr := decodeParams(raw, &p); perr != nil {
		return nil, perr
	}
	if p.Name == "" {
		return nil, invalid("缺少必填参数 name")
	}
	if err := ValidateDatabaseName(p.Name); err != nil {
		return nil, invalid(err.Error())
	}
	if _, system := systemDatabases[strings.ToLower(p.Name)]; system {
		return nil, unauthorized("禁止删除系统库 " + p.Name)
	}
	if !p.Confirm {
		return nil, invalid("删库不可恢复，请传 confirm=true")
	}
	instance, perr := resolveInstance(cfg, p.Instance)
	if perr != nil {
		return nil, perr
	}
	dropped, err := exec.DropDatabase(instance, p.Name)
	if err != nil {
		return nil, classify(err)
	}
	return encode(DropDatabaseResult{Dropped: dropped, Name: p.Name})
}

// callCreateUser 处理 db-mysql.createUser：默认 host=localhost，密码只进 SQL（stdin），
// 响应与错误都不回显密码；grants 为可选的「库:权限列表」便捷授权。
func callCreateUser(raw json.RawMessage, cfg Config, exec Executor) (json.RawMessage, *pluginapi.Error) {
	if perr := requireAdmin(cfg); perr != nil {
		return nil, perr
	}
	var p createUserParams
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
	if err := ValidateAccountPassword(p.Password); err != nil {
		return nil, invalid(err.Error())
	}
	instance, perr := resolveInstance(cfg, p.Instance)
	if perr != nil {
		return nil, perr
	}
	created, err := exec.CreateUser(instance, p.User, host, p.Password)
	if err != nil {
		return nil, redactError(classify(err), p.Password)
	}
	if len(p.Grants) > 0 {
		if perr := applyGrantSpecs(exec, instance, p.User, host, p.Grants); perr != nil {
			return nil, redactError(perr, p.Password)
		}
	}
	return encode(CreateUserResult{Created: created, User: p.User, Host: host})
}

// callDropUser 处理 db-mysql.dropUser：root 保护 → confirm → DROP USER。
func callDropUser(raw json.RawMessage, cfg Config, exec Executor) (json.RawMessage, *pluginapi.Error) {
	if perr := requireAdmin(cfg); perr != nil {
		return nil, perr
	}
	var p dropUserParams
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
	if strings.EqualFold(p.User, "root") {
		return nil, unauthorized("禁止删除 root 账号")
	}
	if !p.Confirm {
		return nil, invalid("删用户不可恢复，请传 confirm=true")
	}
	instance, perr := resolveInstance(cfg, p.Instance)
	if perr != nil {
		return nil, perr
	}
	dropped, err := exec.DropUser(instance, p.User, host)
	if err != nil {
		return nil, classify(err)
	}
	return encode(DropUserResult{Dropped: dropped})
}

// callGrant 处理 db-mysql.grant：权限白名单 → GRANT <privs> ON `db`.* TO 'u'@'h'。
func callGrant(raw json.RawMessage, cfg Config, exec Executor) (json.RawMessage, *pluginapi.Error) {
	if perr := requireAdmin(cfg); perr != nil {
		return nil, perr
	}
	var p grantParams
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
	if p.Database == "" {
		return nil, invalid("缺少必填参数 database")
	}
	if err := ValidateDatabaseName(p.Database); err != nil {
		return nil, invalid(err.Error())
	}
	privileges, perr := normalizePrivileges(p.Privileges)
	if perr != nil {
		return nil, perr
	}
	instance, perr := resolveInstance(cfg, p.Instance)
	if perr != nil {
		return nil, perr
	}
	if err := exec.Grant(instance, p.User, host, p.Database, privileges); err != nil {
		return nil, classify(err)
	}
	return encode(GrantResult{Granted: true, Privileges: privileges})
}

// normalizePrivileges 把权限列表归一化为大写白名单集合：去空、去重、保持输入顺序。
func normalizePrivileges(raw []string) ([]string, *pluginapi.Error) {
	if len(raw) == 0 {
		return nil, invalid("缺少必填参数 privileges")
	}
	seen := make(map[string]struct{}, len(raw))
	out := make([]string, 0, len(raw))
	for _, item := range raw {
		privilege := strings.ToUpper(strings.TrimSpace(item))
		switch {
		case privilege == "":
			return nil, invalid("privileges 含空项")
		case privilege == "ALL" || privilege == "ALL PRIVILEGES":
			return nil, invalid("拒绝 ALL PRIVILEGES：请显式列出白名单内的权限（" + privilegeOrder + "）")
		case privilege == "GRANT OPTION":
			return nil, invalid("拒绝 GRANT OPTION：授权不可再转授")
		}
		if _, ok := grantPrivileges[privilege]; !ok {
			return nil, invalid(fmt.Sprintf("权限 %q 不在白名单内（允许: %s）", item, privilegeOrder))
		}
		if _, dup := seen[privilege]; dup {
			continue
		}
		seen[privilege] = struct{}{}
		out = append(out, privilege)
	}
	if len(out) == 0 {
		return nil, invalid("缺少必填参数 privileges")
	}
	return out, nil
}

// applyGrantSpecs 处理 createUser 的可选 grants：每项格式为 "<database>:<权限>[,<权限>...]"。
// 复用 grant 的白名单与标识符校验，不允许 ALL PRIVILEGES / GRANT OPTION。
func applyGrantSpecs(exec Executor, instance InstanceConfig, user, host string, specs []string) *pluginapi.Error {
	for _, spec := range specs {
		index := strings.Index(spec, ":")
		if index <= 0 || index == len(spec)-1 {
			return invalid(fmt.Sprintf("grants 项 %q 格式非法：应为 <database>:<权限>[,<权限>...]", spec))
		}
		database := strings.TrimSpace(spec[:index])
		if err := ValidateDatabaseName(database); err != nil {
			return invalid(err.Error())
		}
		privileges, perr := normalizePrivileges(strings.Split(spec[index+1:], ","))
		if perr != nil {
			return perr
		}
		if err := exec.Grant(instance, user, host, database, privileges); err != nil {
			classified := classify(err)
			return &pluginapi.Error{
				Code:    classified.Code,
				Message: "用户已创建/已存在，但授权失败: " + classified.Message,
			}
		}
	}
	return nil
}

// redactError 把已知密码从错误信息中替换为 ***；返回新的错误体，避免改动上游对象。
func redactError(perr *pluginapi.Error, secrets ...string) *pluginapi.Error {
	if perr == nil {
		return nil
	}
	message := perr.Message
	for _, secret := range secrets {
		if secret != "" {
			message = strings.ReplaceAll(message, secret, "***")
		}
	}
	return &pluginapi.Error{Code: perr.Code, Message: message, Detail: perr.Detail}
}

// unauthorized 构造 E_UNAUTHORIZED 错误。
func unauthorized(msg string) *pluginapi.Error {
	return &pluginapi.Error{Code: pluginapi.E_UNAUTHORIZED, Message: msg}
}
