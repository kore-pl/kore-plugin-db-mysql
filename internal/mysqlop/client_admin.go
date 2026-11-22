package mysqlop

import (
	"bytes"
	"context"
	"errors"
	"os/exec"
	"strconv"
	"strings"

	"github.com/pengtang1024/kore/pkg/pluginapi"
)

// 本文件是 L2 写操作的真实执行面：SQL 通过 stdin 交给 mysql 客户端，
// **密码只存在于 SQL 文本里，绝不进 argv**（`--password`/`-p` 会进 ps，
// `mysql -e "<SQL>"` 同样会让含密码的 SQL 出现在 ps 里，两条都不能用）。
// 子进程环境仍只带当前实例的 MYSQL_PWD，供客户端完成连接认证。

// runStdin 用 stdin 执行 SQL 批处理，返回 stdout。
func (c *Client) runStdin(instance InstanceConfig, statements string) ([]byte, error) {
	password, err := instancePassword(instance)
	if err != nil {
		return nil, opErrorf(pluginapi.E_INVALID, "%s", err.Error())
	}
	ctx, cancel := context.WithTimeout(context.Background(), c.timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, c.bin, c.stdinArgs(instance)...)
	cmd.Env = buildCommandEnv(password)
	cmd.Stdin = strings.NewReader(statements)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return nil, timeoutError(c.timeout)
		}
		return nil, c.execError(err, password, stderr.String())
	}
	return stdout.Bytes(), nil
}

// stdinArgs 构造「从 stdin 读 SQL」的客户端参数：没有 -e，也没有 -p。
func (c *Client) stdinArgs(instance InstanceConfig) []string {
	return []string{
		"--no-defaults", "--batch", "--skip-column-names", "--raw",
		"--default-character-set=utf8mb4", "--connect-timeout=5",
		"-h", instance.Host,
		"-P", strconv.Itoa(instance.Port),
		"-u", instance.User,
	}
}

// scalarInt 执行单值查询并解析为 int64。
func (c *Client) scalarInt(instance InstanceConfig, sql string) (int64, error) {
	stdout, err := c.runStdin(instance, sql)
	if err != nil {
		return 0, err
	}
	rows, err := parseTSV(stdout, false)
	if err != nil {
		return 0, opErrorf(pluginapi.E_INTERNAL, "解析 mysql 输出失败: %v", err)
	}
	if len(rows) != 1 || len(rows[0]) != 1 {
		return 0, opErrorf(pluginapi.E_UPSTREAM, "单值查询返回了意外行数")
	}
	value, err := strconv.ParseInt(strings.TrimSpace(rows[0][0]), 10, 64)
	if err != nil {
		return 0, opErrorf(pluginapi.E_UPSTREAM, "无法解析查询结果 %q", rows[0][0])
	}
	return value, nil
}

// scalarString 执行单值查询并原样返回文本。
func (c *Client) scalarString(instance InstanceConfig, sql string) (string, error) {
	stdout, err := c.runStdin(instance, sql)
	if err != nil {
		return "", err
	}
	rows, err := parseTSV(stdout, false)
	if err != nil {
		return "", opErrorf(pluginapi.E_INTERNAL, "解析 mysql 输出失败: %v", err)
	}
	if len(rows) != 1 || len(rows[0]) != 1 {
		return "", opErrorf(pluginapi.E_UPSTREAM, "单值查询返回了意外行数")
	}
	return rows[0][0], nil
}

// backslashEscapes 报告当前会话是否启用反斜杠转义（默认开启；NO_BACKSLASH_ESCAPES 关闭）。
// 密码字面量是否需要把 \ 写成 \\ 完全由它决定——写死任何一种都会在另一种模式下
// 改掉密码原文。
func (c *Client) backslashEscapes(instance InstanceConfig) (bool, error) {
	mode, err := c.scalarString(instance, "SELECT @@SESSION.sql_mode")
	if err != nil {
		return true, err
	}
	return !strings.Contains(strings.ToUpper(mode), "NO_BACKSLASH_ESCAPES"), nil
}

// CreateDatabase 建库；返回 created=false 表示库此前已存在（IF NOT EXISTS 不报错）。
func (c *Client) CreateDatabase(instance InstanceConfig, name, charset, collation string) (bool, error) {
	if err := ValidateDatabaseName(name); err != nil {
		return false, opErrorf(pluginapi.E_INVALID, "%s", err.Error())
	}
	if charset != "" {
		if err := ValidateCharset(charset); err != nil {
			return false, opErrorf(pluginapi.E_INVALID, "%s", err.Error())
		}
	}
	if collation != "" {
		if err := ValidateCollation(collation); err != nil {
			return false, opErrorf(pluginapi.E_INVALID, "%s", err.Error())
		}
	}
	// name 已过 [a-zA-Z0-9_] 白名单，作为字符串字面量不存在注入面。
	count, err := c.scalarInt(instance,
		"SELECT COUNT(*) FROM information_schema.SCHEMATA WHERE SCHEMA_NAME = '"+name+"'")
	if err != nil {
		return false, err
	}
	if err := c.execSQL(instance, createDatabaseSQL(name, charset, collation)); err != nil {
		return false, err
	}
	return count == 0, nil
}

// DropDatabase 删库；库不存在返回 E_NOTFOUND（而不是静默成功）。
func (c *Client) DropDatabase(instance InstanceConfig, name string) (bool, error) {
	if err := ValidateDatabaseName(name); err != nil {
		return false, opErrorf(pluginapi.E_INVALID, "%s", err.Error())
	}
	count, err := c.scalarInt(instance,
		"SELECT COUNT(*) FROM information_schema.SCHEMATA WHERE SCHEMA_NAME = '"+name+"'")
	if err != nil {
		return false, err
	}
	if count == 0 {
		return false, opErrorf(pluginapi.E_NOTFOUND, "数据库 %q 不存在", name)
	}
	if err := c.execSQL(instance, dropDatabaseSQL(name)); err != nil {
		return false, err
	}
	return true, nil
}

// CreateUser 建用户；已存在时 created=false 且**不修改密码**（改密需要单独方法）。
func (c *Client) CreateUser(instance InstanceConfig, user, host, password string) (bool, error) {
	if err := ValidateAccountUser(user); err != nil {
		return false, opErrorf(pluginapi.E_INVALID, "%s", err.Error())
	}
	if err := ValidateAccountHost(host); err != nil {
		return false, opErrorf(pluginapi.E_INVALID, "%s", err.Error())
	}
	if err := ValidateAccountPassword(password); err != nil {
		return false, opErrorf(pluginapi.E_INVALID, "%s", err.Error())
	}
	count, err := c.scalarInt(instance,
		"SELECT COUNT(*) FROM mysql.user WHERE User = '"+user+"' AND Host = '"+host+"'")
	if err != nil {
		return false, err
	}
	if count > 0 {
		return false, nil
	}
	escapes, err := c.backslashEscapes(instance)
	if err != nil {
		return false, err
	}
	if err := c.execSQL(instance, createUserSQL(user, host, password, escapes)); err != nil {
		return false, err
	}
	return true, nil
}

// DropUser 删用户；用户不存在返回 E_NOTFOUND。
func (c *Client) DropUser(instance InstanceConfig, user, host string) (bool, error) {
	if err := ValidateAccountUser(user); err != nil {
		return false, opErrorf(pluginapi.E_INVALID, "%s", err.Error())
	}
	if err := ValidateAccountHost(host); err != nil {
		return false, opErrorf(pluginapi.E_INVALID, "%s", err.Error())
	}
	count, err := c.scalarInt(instance,
		"SELECT COUNT(*) FROM mysql.user WHERE User = '"+user+"' AND Host = '"+host+"'")
	if err != nil {
		return false, err
	}
	if count == 0 {
		return false, opErrorf(pluginapi.E_NOTFOUND, "用户 %q@%q 不存在", user, host)
	}
	if err := c.execSQL(instance, dropUserSQL(user, host)); err != nil {
		return false, err
	}
	return true, nil
}

// Grant 授权：库名过白名单、权限过白名单，权限以逗号拼接（白名单不含逗号/空格）。
func (c *Client) Grant(instance InstanceConfig, user, host, database string, privileges []string) error {
	if err := ValidateAccountUser(user); err != nil {
		return opErrorf(pluginapi.E_INVALID, "%s", err.Error())
	}
	if err := ValidateAccountHost(host); err != nil {
		return opErrorf(pluginapi.E_INVALID, "%s", err.Error())
	}
	if err := ValidateDatabaseName(database); err != nil {
		return opErrorf(pluginapi.E_INVALID, "%s", err.Error())
	}
	normalized, perr := normalizePrivileges(privileges)
	if perr != nil {
		return opErrorf(perr.Code, "%s", perr.Message)
	}
	return c.execSQL(instance, grantSQL(user, host, database, normalized))
}

// execSQL 执行一批 DDL/DCL；无结果集，错误已脱敏。
func (c *Client) execSQL(instance InstanceConfig, statements string) error {
	_, err := c.runStdin(instance, statements)
	return err
}

// SQL 构造（纯函数，便于单测断言「标识符有反引号、权限来自白名单」）

// createDatabaseSQL 构造建库语句；charset/collation 为空时不写对应子句。
func createDatabaseSQL(name, charset, collation string) string {
	sql := "CREATE DATABASE IF NOT EXISTS " + tickIdentifier(name)
	if charset != "" {
		sql += " CHARACTER SET " + charset
	}
	if collation != "" {
		sql += " COLLATE " + collation
	}
	return sql + ";"
}

// dropDatabaseSQL 构造删库语句。
func dropDatabaseSQL(name string) string {
	return "DROP DATABASE " + tickIdentifier(name) + ";"
}

// createUserSQL 构造建用户语句；密码为空时创建无密码账号（与 MySQL 默认语义一致）。
func createUserSQL(user, host, password string, backslashEscapes bool) string {
	sql := "CREATE USER " + accountLiteral(user, host)
	if password != "" {
		sql += " IDENTIFIED BY " + stringLiteral(password, backslashEscapes)
	}
	return sql + ";"
}

// dropUserSQL 构造删用户语句。
func dropUserSQL(user, host string) string {
	return "DROP USER " + accountLiteral(user, host) + ";"
}

// grantSQL 构造授权语句；privileges 必须已过白名单且去重。
func grantSQL(user, host, database string, privileges []string) string {
	return "GRANT " + strings.Join(privileges, ",") + " ON " + tickIdentifier(database) +
		".* TO " + accountLiteral(user, host) + ";"
}
