package mysqlop

import (
	"fmt"
	"strings"
)

// 本文件是代码层参数绑定：SQL 模板只含 `?` 占位符，绑定值在单点（stringLiteral）
// 按服务端 sql_mode 转义后填入。
//
// 边界说明：本仓执行面是本机 mysql 客户端（os/exec），没有 driver，无法使用服务端
// 预处理协议（COM_STMT_PREPARE）；因此「参数化」落在代码层——调用点不再拼接用户值，
// 模板可被单测断言「不含密码」，绑定值只经 stdin 传给本机客户端，绝不进 argv。
// 模板中除占位符外不得出现 `?`（当前唯一使用方 changePasswordStatement 满足）。

// renderBoundStatement 把 values 依次绑定到模板中的 `?` 占位符。
// 占位符数量与绑定值数量必须严格一致，否则返回错误（拒绝半绑定）。
func renderBoundStatement(template string, values []string, backslashEscapes bool) (string, error) {
	var builder strings.Builder
	builder.Grow(len(template) + 64)
	arg := 0
	for i := 0; i < len(template); i++ {
		if template[i] != '?' {
			builder.WriteByte(template[i])
			continue
		}
		if arg >= len(values) {
			return "", fmt.Errorf("占位符多于绑定值：模板有 %d 个 ?，只给了 %d 个值", strings.Count(template, "?"), len(values))
		}
		builder.WriteString(stringLiteral(values[arg], backslashEscapes))
		arg++
	}
	if arg != len(values) {
		return "", fmt.Errorf("绑定值多于占位符：模板有 %d 个 ?，给了 %d 个值", arg, len(values))
	}
	return builder.String(), nil
}

// changePasswordStatement 返回改密语句模板：账号标识符已过白名单并反引号/字面量包裹，
// **密码不在模板里**，由 `?` 占位符在 renderBoundStatement 单点绑定。
func changePasswordStatement(user, host string) string {
	return "ALTER USER " + accountLiteral(user, host) + " IDENTIFIED BY ?;"
}
