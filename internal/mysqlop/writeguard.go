package mysqlop

import (
	"strings"

	"github.com/pengtang1024/kore/pkg/pluginapi"
)

// 写入分两级确认：
//   - data（INSERT/UPDATE/DELETE/REPLACE）：confirm=true 即可，属于日常增删改；
//   - ddl （DROP/TRUNCATE/ALTER/CREATE/RENAME/GRANT/REVOKE/FLUSH/SET/CALL 等）：
//     confirm=true 之外还必须手打首两个关键字（如 "DROP TABLE"），把不可回滚动作
//     从「点一下」抬到「想清楚再打一遍」。
const (
	// ExecKindData 表示数据行级写入（可回滚/可重放代价低）。
	ExecKindData = "data"
	// ExecKindDDL 表示结构/权限/会话级写入（不可回滚或影响面大）。
	ExecKindDDL = "ddl"
)

// dataWriteKeywords 是数据行级写语句的首关键字白名单。
var dataWriteKeywords = map[string]struct{}{
	"INSERT":  {},
	"UPDATE":  {},
	"DELETE":  {},
	"REPLACE": {},
}

// ddlWriteKeywords 是结构/权限/会话级语句的首关键字白名单。
var ddlWriteKeywords = map[string]struct{}{
	"DROP":     {},
	"TRUNCATE": {},
	"ALTER":    {},
	"CREATE":   {},
	"RENAME":   {},
	"GRANT":    {},
	"REVOKE":   {},
	"FLUSH":    {},
	"SET":      {},
	"ANALYZE":  {},
	"OPTIMIZE": {},
	"REPAIR":   {},
	"CALL":     {},
}

// ValidateWriteSQL 执行写入白名单 + 两级确认校验，返回语句分级（data / ddl）。
//
// 与 ValidateReadOnlySQL 共用同一套「结构清洗」纪律：字符串/注释内不算关键字、
// 只允许单条语句、拒绝 NUL 与反斜杠客户端命令、拒绝可执行注释与侧信道函数。
func ValidateWriteSQL(sql string, confirm bool, confirmText string) (string, *pluginapi.Error) {
	if strings.TrimSpace(sql) == "" {
		return "", invalid("sql 不得为空")
	}
	if len(sql) > maxSQLBytes {
		return "", invalid("sql 超过 64 KiB 上限")
	}
	if strings.ContainsRune(sql, 0) {
		return "", invalid("sql 含 NUL 字节")
	}
	sanitized, err := sanitizeSQL(sql)
	if err != nil {
		return "", invalid("sql 非法: " + err.Error())
	}
	statement := strings.TrimSpace(sanitized)
	if statement == "" {
		return "", invalid("sql 不得为空")
	}
	if strings.HasSuffix(statement, ";") {
		statement = strings.TrimSpace(strings.TrimSuffix(statement, ";"))
	}
	if strings.Contains(statement, ";") {
		return "", invalid("只允许单条语句，禁止多条语句（分号只能出现在末尾）")
	}

	keyword := firstKeyword(statement)
	kind := ""
	switch keyword {
	case "SELECT", "SHOW", "DESCRIBE", "DESC", "EXPLAIN", "WITH":
		return "", invalid("读语句请走 db-mysql.query（exec 只接受写语句）")
	}
	if _, ok := dataWriteKeywords[keyword]; ok {
		kind = ExecKindData
	} else if _, ok := ddlWriteKeywords[keyword]; ok {
		kind = ExecKindDDL
	} else {
		return "", invalid("写入白名单拒绝: 语句必须以 INSERT/UPDATE/DELETE/REPLACE 或 DROP/TRUNCATE/ALTER/CREATE/RENAME/GRANT/REVOKE 开头")
	}

	upper := strings.ToUpper(statement)
	for _, f := range forbiddenSQL {
		if f.re.MatchString(upper) {
			return "", invalid("写入白名单拒绝: " + f.msg)
		}
	}
	// LOAD DATA 会把宿主文件内容写进库，属于侧信道；首关键字白名单已挡，这里再点明原因。
	if strings.Contains(upper, "LOAD DATA") {
		return "", invalid("写入白名单拒绝: 禁止 LOAD DATA（读宿主文件）")
	}

	if !confirm {
		return "", invalid("写语句需要显式确认：请传 confirm=true")
	}
	if kind == ExecKindDDL {
		expected := confirmPhrase(statement)
		got := strings.TrimSpace(confirmText)
		if got == "" {
			return "", invalid(`危险语句需要手打确认短语：请把 confirm_text 设为 "` + expected + `"`)
		}
		if !strings.EqualFold(got, expected) {
			return "", invalid(`confirm_text 与语句不匹配，应填 "` + expected + `"`)
		}
	}
	return kind, nil
}

// confirmPhrase 取语句前两个关键字作为确认短语（大写、单空格），如 DROP TABLE / ALTER TABLE。
// 只有一个关键字时返回该关键字本身（如 SET）。
func confirmPhrase(statement string) string {
	fields := strings.Fields(statement)
	words := make([]string, 0, 2)
	for _, field := range fields {
		if !isAlphaWord(field) {
			break
		}
		words = append(words, strings.ToUpper(field))
		if len(words) == 2 {
			break
		}
	}
	if len(words) == 0 {
		return ""
	}
	return strings.Join(words, " ")
}

// isAlphaWord 判断 token 是否全为 ASCII 字母（用于确认短语，标识符/括号不参与）。
func isAlphaWord(token string) bool {
	if token == "" {
		return false
	}
	for i := 0; i < len(token); i++ {
		c := token[i]
		if (c < 'a' || c > 'z') && (c < 'A' || c > 'Z') {
			return false
		}
	}
	return true
}
