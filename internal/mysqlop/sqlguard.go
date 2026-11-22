package mysqlop

import (
	"errors"
	"regexp"
	"strings"

	"github.com/pengtang1024/kore/pkg/pluginapi"
)

// maxSQLBytes 限制单条 SQL 长度，避免把超大参数塞进 argv / 客户端。
const maxSQLBytes = 64 * 1024

// 只读语句白名单：语句首关键字，大小写不敏感。
var readOnlyLeadingKeywords = map[string]struct{}{
	"SELECT":   {},
	"SHOW":     {},
	"DESCRIBE": {},
	"DESC":     {},
	"EXPLAIN":  {},
}

// forbiddenSQL 是白名单之外的写/阻塞/外泄通道，按「去字符串与注释后的文本」匹配。
var forbiddenSQL = []struct {
	re  *regexp.Regexp
	msg string
}{
	{regexp.MustCompile(`\bINTO\s+(OUTFILE|DUMPFILE)\b`), "禁止 SELECT ... INTO OUTFILE/DUMPFILE（写文件）"},
	{regexp.MustCompile(`\bFOR\s+UPDATE\b`), "禁止加锁读 FOR UPDATE"},
	{regexp.MustCompile(`\bFOR\s+SHARE\b`), "禁止加锁读 FOR SHARE"},
	{regexp.MustCompile(`\bLOCK\s+IN\s+SHARE\s+MODE\b`), "禁止加锁读 LOCK IN SHARE MODE"},
	{regexp.MustCompile(`\b(SLEEP|BENCHMARK)\s*\(`), "禁止 SLEEP/BENCHMARK 阻塞函数"},
	{regexp.MustCompile(`\b(LOAD_FILE|GET_LOCK|RELEASE_LOCK|IS_FREE_LOCK|IS_USED_LOCK)\s*\(`), "禁止文件读取/命名锁函数"},
	{regexp.MustCompile(`\bPROCEDURE\s+ANALYSE\b`), "禁止 PROCEDURE ANALYSE"},
}

// explainAnalyzeRE 是允许的 EXPLAIN ANALYZE 形态：内层必须是 SELECT，绝不能是写语句。
var explainAnalyzeRE = regexp.MustCompile(`^EXPLAIN\s+ANALYZE\s+(FORMAT\s*=\s*[A-Z]+\s+)?SELECT\b`)

// ValidateReadOnlySQL 执行只读白名单校验：只允许单条 SELECT/SHOW/DESCRIBE/EXPLAIN。
//
// 防的是「mysql 客户端 -e 会执行分号分隔的多条语句」这一点：语句内部出现任何
// 分号（字符串/注释内的不算）一律拒绝；同时拒绝客户端反斜杠命令（\g / \! 等）、
// 可执行注释 /*! ... */、写文件、加锁读与阻塞函数。
func ValidateReadOnlySQL(sql string) *pluginapi.Error {
	if strings.TrimSpace(sql) == "" {
		return invalid("sql 不得为空")
	}
	if len(sql) > maxSQLBytes {
		return invalid("sql 超过 64 KiB 上限")
	}
	if strings.ContainsRune(sql, 0) {
		return invalid("sql 含 NUL 字节")
	}
	sanitized, err := sanitizeSQL(sql)
	if err != nil {
		return invalid("sql 非法: " + err.Error())
	}
	statement := strings.TrimSpace(sanitized)
	if statement == "" {
		return invalid("sql 不得为空")
	}
	// 允许且仅允许一个结尾分号；其余分号都视为多语句。
	if strings.HasSuffix(statement, ";") {
		statement = strings.TrimSpace(strings.TrimSuffix(statement, ";"))
	}
	if strings.Contains(statement, ";") {
		return invalid("只允许单条只读语句，禁止多条语句（分号只能出现在末尾）")
	}
	keyword := firstKeyword(statement)
	if _, ok := readOnlyLeadingKeywords[keyword]; !ok {
		return invalid("只读白名单拒绝: 语句必须以 SELECT/SHOW/DESCRIBE/EXPLAIN 开头")
	}
	upper := strings.ToUpper(statement)
	if keyword == "EXPLAIN" {
		if regexp.MustCompile(`\bANALYZE\b`).MatchString(upper) && !explainAnalyzeRE.MatchString(upper) {
			return invalid("只读白名单拒绝: EXPLAIN ANALYZE 只允许分析 SELECT")
		}
	}
	for _, f := range forbiddenSQL {
		if f.re.MatchString(upper) {
			return invalid("只读白名单拒绝: " + f.msg)
		}
	}
	return nil
}

// sanitizeSQL 去掉字符串字面量、标识符引用与注释，返回仅剩 SQL 结构的文本。
// 字符串/注释内容替换为空格，避免把字面量里的关键字或分号误判为真实语句。
func sanitizeSQL(sql string) (string, error) {
	var b strings.Builder
	b.Grow(len(sql))
	for i := 0; i < len(sql); {
		c := sql[i]
		switch {
		case c == '\'' || c == '"' || c == '`':
			next, err := skipQuoted(sql, i, c)
			if err != nil {
				return "", err
			}
			i = next
			b.WriteByte(' ')
		case c == '#':
			for i < len(sql) && sql[i] != '\n' {
				i++
			}
			b.WriteByte(' ')
		case c == '-' && i+1 < len(sql) && sql[i+1] == '-' && isCommentDashEnd(sql, i+2):
			for i < len(sql) && sql[i] != '\n' {
				i++
			}
			b.WriteByte(' ')
		case c == '/' && i+1 < len(sql) && sql[i+1] == '*':
			if i+2 < len(sql) && sql[i+2] == '!' {
				return "", errors.New("禁止可执行注释 /*! ... */")
			}
			end := strings.Index(sql[i+2:], "*/")
			if end < 0 {
				return "", errors.New("块注释未闭合")
			}
			i += 2 + end + 2
			b.WriteByte(' ')
		case c == '\\':
			return "", errors.New("禁止反斜杠客户端命令（如 \\g、\\!）")
		default:
			b.WriteByte(c)
			i++
		}
	}
	return b.String(), nil
}

// skipQuoted 跳过一段引号内容（处理反斜杠转义与成对引号），返回结束位置。
func skipQuoted(sql string, start int, quote byte) (int, error) {
	for i := start + 1; i < len(sql); i++ {
		switch sql[i] {
		case '\\':
			if quote != '`' {
				i++ // 跳过被转义字符
			}
		case quote:
			if i+1 < len(sql) && sql[i+1] == quote {
				i++ // 成对引号表示一个字面引号
				continue
			}
			return i + 1, nil
		}
	}
	return 0, errors.New("引号未闭合")
}

// isCommentDashEnd 判断 "--" 之后是否为 MySQL 认可的注释起始（空白或行尾）。
func isCommentDashEnd(sql string, i int) bool {
	if i >= len(sql) {
		return true
	}
	switch sql[i] {
	case ' ', '\t', '\r', '\n':
		return true
	default:
		return false
	}
}

// firstKeyword 取语句的首个 ASCII 字母串并大写。
func firstKeyword(s string) string {
	s = strings.TrimLeft(s, " \t\r\n")
	i := 0
	for i < len(s) {
		c := s[i]
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') {
			i++
			continue
		}
		break
	}
	return strings.ToUpper(s[:i])
}
