package mysqlop

import (
	"fmt"
	"regexp"
	"strings"
)

// L2 写操作的标识符白名单：库名 / 用户名 / 表名只允许 ASCII 字母、数字与下划线。
//
// 双保险：白名单先挡住引号、反引号、分号、空白与路径字符；通过校验的值在拼 SQL 时
// 再统一用反引号包裹（保留字如 order、select 也能安全作为标识符）。**用户输入永远
// 不会以「带引号的字符串」形式进入 SQL**——需要字符串字面量的只有账号名/主机/密码，
// 账号与主机同样先过白名单，密码单独做字面量转义。
var (
	databaseNameRE = regexp.MustCompile(`^[a-zA-Z0-9_]{1,64}$`)
	accountUserRE  = regexp.MustCompile(`^[a-zA-Z0-9_]{1,32}$`)
	accountHostRE  = regexp.MustCompile(`^[a-zA-Z0-9_.:%-]{1,255}$`)
	tableNameRE    = regexp.MustCompile(`^[a-zA-Z0-9_]{1,64}$`)
	charsetRE      = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]{0,63}$`)
	collationRE    = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]{0,63}$`)
)

const (
	// maxPasswordBytes 与配置契约的密码上限一致，避免异常大字段进入 SQL。
	maxPasswordBytes = 1024
	// maxTableCount 限制 mysqldump 的表名个数，防止 argv 膨胀。
	maxTableCount = 1024
)

// ValidateDatabaseName 校验库名（建库/删库/授权/导出的库名共用）。
func ValidateDatabaseName(name string) error {
	if name == "" {
		return fmt.Errorf("数据库名不得为空")
	}
	if !databaseNameRE.MatchString(name) {
		return fmt.Errorf("数据库名 %q 非法：只允许字母、数字与下划线，长度 1-64", name)
	}
	return nil
}

// ValidateAccountUser 校验 MySQL 账号名（注意与「实例连接用户名」不同）。
func ValidateAccountUser(user string) error {
	if user == "" {
		return fmt.Errorf("user 不得为空")
	}
	if !accountUserRE.MatchString(user) {
		return fmt.Errorf("user %q 非法：只允许字母、数字与下划线，长度 1-32", user)
	}
	return nil
}

// ValidateAccountHost 校验 MySQL 账号 host 段（默认 localhost，允许 % 通配）。
func ValidateAccountHost(host string) error {
	if host == "" {
		return fmt.Errorf("host 不得为空")
	}
	if !accountHostRE.MatchString(host) {
		return fmt.Errorf("host %q 非法：只允许字母、数字与 . _ : %% -，长度 1-255", host)
	}
	return nil
}

// ValidateTableName 校验导出时显式指定的表名。
func ValidateTableName(name string) error {
	if name == "" {
		return fmt.Errorf("表名不得为空")
	}
	if !tableNameRE.MatchString(name) {
		return fmt.Errorf("表名 %q 非法：只允许字母、数字与下划线，长度 1-64", name)
	}
	return nil
}

// ValidateCharset 校验建库字符集；未知字符集交给服务端报错（E_UPSTREAM），
// 这里只保证它不能是 SQL 片段。
func ValidateCharset(charset string) error {
	if !charsetRE.MatchString(charset) {
		return fmt.Errorf("charset %q 非法：须以字母开头，仅含字母、数字与下划线，长度 ≤64", charset)
	}
	return nil
}

// ValidateCollation 校验建库排序规则，规则同 ValidateCharset。
func ValidateCollation(collation string) error {
	if !collationRE.MatchString(collation) {
		return fmt.Errorf("collation %q 非法：须以字母开头，仅含字母、数字与下划线，长度 ≤64", collation)
	}
	return nil
}

// ValidateAccountPassword 校验建用户密码：拒绝 NUL 与超长，其余字符一律允许
// （包含引号、反斜杠），由 escapeSQLString 负责安全编码。
func ValidateAccountPassword(password string) error {
	if len(password) > maxPasswordBytes {
		return fmt.Errorf("密码超过 %d 字节上限", maxPasswordBytes)
	}
	if strings.ContainsRune(password, 0) {
		return fmt.Errorf("密码含 NUL 字节，无法写入 SQL 字面量")
	}
	return nil
}

// ValidateTableList 校验导出表名列表：去重、上限 1024，保持输入顺序。
func ValidateTableList(tables []string) ([]string, error) {
	if len(tables) > maxTableCount {
		return nil, fmt.Errorf("tables 数量 %d 超过上限 %d", len(tables), maxTableCount)
	}
	seen := make(map[string]struct{}, len(tables))
	out := make([]string, 0, len(tables))
	for _, table := range tables {
		if err := ValidateTableName(table); err != nil {
			return nil, err
		}
		if _, dup := seen[table]; dup {
			continue
		}
		seen[table] = struct{}{}
		out = append(out, table)
	}
	return out, nil
}

// SQL 标识符包裹

// tickIdentifier 用反引号包裹**已经通过白名单校验**的标识符。
// 调用方必须先调用对应 Validate*；本函数不再做校验，避免出现「校验旁路」。
func tickIdentifier(name string) string { return "`" + name + "`" }

// accountLiteral 把已校验的 user@host 编码为 'user'@'host' 字面量。
// 两个字段的白名单都排除了单引号与反斜杠，因此不会逃出字面量。
func accountLiteral(user, host string) string {
	return "'" + user + "'@'" + host + "'"
}

// escapeSQLString 把任意文本编码进 MySQL 单引号字面量（不包含外层引号）。
//
// 单引号一律写成两个单引号：标准 SQL 写法，在 NO_BACKSLASH_ESCAPES 下同样成立。
// 反斜杠在默认 sql_mode 下是转义符，必须写成两个；NO_BACKSLASH_ESCAPES 模式下
// 反斜杠是普通字符，保持原样——所以是否需要转义反斜杠由服务端 sql_mode 决定，
// 由调用方查回后传入。
func escapeSQLString(value string, backslashEscapes bool) string {
	var b strings.Builder
	b.Grow(len(value) + 8)
	for i := 0; i < len(value); i++ {
		switch c := value[i]; {
		case c == '\'':
			b.WriteString("''")
		case c == '\\' && backslashEscapes:
			b.WriteString(`\\`)
		default:
			b.WriteByte(c)
		}
	}
	return b.String()
}

// stringLiteral 编码为带外层单引号的 SQL 字符串字面量。
func stringLiteral(value string, backslashEscapes bool) string {
	return "'" + escapeSQLString(value, backslashEscapes) + "'"
}
