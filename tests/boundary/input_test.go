package boundary

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/pengtang1024/kore-plugin-db-mysql/internal/mysqlop"
	"github.com/pengtang1024/kore/pkg/pluginapi"
)

// TestBoundaryMalformedInput 汇总异常输入的代表性用例（从既有单元测试的 11 类攻击面
// 里取代表，走 mysqlop.Call 的公开方法面重放）：
//
//   - SQL 注入 / 多语句 / 写语句进只读入口
//   - 非法库名（反引号、分号）、超长标识符（>64）
//   - 用户/授权参数注入、ALL PRIVILEGES
//   - 超长 SQL、NUL 字节、params 非对象、limit 越界、未知方法/实例
//
// 每个用例都断言「精确错误码 + 关键提示 + 未触达执行面」：校验必须发生在执行面之前。
func TestBoundaryMalformedInput(t *testing.T) {
	longIdentifier := strings.Repeat("a", 65)
	longSQL := "SELECT '" + strings.Repeat("a", 64<<10) + "'"
	cases := []struct {
		name   string
		method string
		params string
		code   string
		wants  []string
	}{
		{
			name:   "createDatabase_sql_injection",
			method: "db-mysql.createDatabase",
			params: fmt.Sprintf(`{"instance":"primary","name":%q}`, "kore; DROP DATABASE kore"),
			code:   pluginapi.E_INVALID,
			wants:  []string{"数据库名"},
		},
		{
			name:   "createDatabase_backtick_identifier",
			method: "db-mysql.createDatabase",
			params: fmt.Sprintf(`{"instance":"primary","name":%q}`, "bad`name"),
			code:   pluginapi.E_INVALID,
			wants:  []string{"数据库名"},
		},
		{
			name:   "createDatabase_overlong_identifier",
			method: "db-mysql.createDatabase",
			params: fmt.Sprintf(`{"instance":"primary","name":%q}`, longIdentifier),
			code:   pluginapi.E_INVALID,
			wants:  []string{"数据库名"},
		},
		{
			name:   "dropDatabase_system_database",
			method: "db-mysql.dropDatabase",
			params: `{"instance":"primary","name":"mysql","confirm":true}`,
			code:   pluginapi.E_UNAUTHORIZED,
			wants:  []string{"系统库"},
		},
		{
			name:   "createUser_quote_injection",
			method: "db-mysql.createUser",
			params: `{"instance":"primary","user":"root'--","host":"localhost"}`,
			code:   pluginapi.E_INVALID,
			wants:  []string{"user"},
		},
		{
			name:   "grant_all_privileges",
			method: "db-mysql.grant",
			params: `{"instance":"primary","user":"app","database":"kore","privileges":["ALL PRIVILEGES"]}`,
			code:   pluginapi.E_INVALID,
			wants:  []string{"ALL PRIVILEGES"},
		},
		{
			name:   "grant_database_injection",
			method: "db-mysql.grant",
			params: `{"instance":"primary","user":"app","database":"kore; DROP DATABASE kore","privileges":["SELECT"]}`,
			code:   pluginapi.E_INVALID,
			wants:  []string{"数据库名"},
		},
		{
			name:   "changePassword_identifier_injection",
			method: "db-mysql.changePassword",
			params: `{"instance":"primary","user":"app` + "`" + `;--","password":"Str0ngPass!"}`,
			code:   pluginapi.E_INVALID,
			wants:  []string{"user"},
		},
		{
			name:   "query_write_statement",
			method: "db-mysql.query",
			params: `{"instance":"primary","sql":"DELETE FROM kore"}`,
			code:   pluginapi.E_INVALID,
			wants:  []string{"只读白名单"},
		},
		{
			name:   "query_multi_statement",
			method: "db-mysql.query",
			params: `{"instance":"primary","sql":"SELECT 1; DROP TABLE kore"}`,
			code:   pluginapi.E_INVALID,
			wants:  []string{"多条语句"},
		},
		{
			name:   "query_nul_byte",
			method: "db-mysql.query",
			params: "{\"instance\":\"primary\",\"sql\":\"SELECT 1\\u0000\"}",
			code:   pluginapi.E_INVALID,
			wants:  []string{"NUL"},
		},
		{
			name:   "query_overlong_sql",
			method: "db-mysql.query",
			params: fmt.Sprintf(`{"instance":"primary","sql":%q}`, longSQL),
			code:   pluginapi.E_INVALID,
			wants:  []string{"64 KiB"},
		},
		{
			name:   "query_params_not_object",
			method: "db-mysql.query",
			params: `["primary"]`,
			code:   pluginapi.E_INVALID,
			wants:  []string{"JSON 对象"},
		},
		{
			name:   "query_params_truncated_json",
			method: "db-mysql.query",
			params: `{"instance":`,
			code:   pluginapi.E_INVALID,
			wants:  []string{"解析失败"},
		},
		{
			name:   "query_limit_out_of_range",
			method: "db-mysql.query",
			params: `{"instance":"primary","sql":"SELECT 1","limit":1001}`,
			code:   pluginapi.E_INVALID,
			wants:  []string{"limit"},
		},
		{
			name:   "unknown_method",
			method: "db-mysql.nonexistent",
			params: `{}`,
			code:   pluginapi.E_NOTFOUND,
			wants:  []string{"未知方法"},
		},
		{
			name:   "unknown_instance",
			method: "db-mysql.query",
			params: `{"instance":"ghost","sql":"SELECT 1"}`,
			code:   pluginapi.E_NOTFOUND,
			wants:  []string{"未配置实例"},
		},
	}

	cfg := boundaryConfig("/bin/false", "/bin/false", t.TempDir())
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			exec := &tripwireExecutor{}
			result, perr := mysqlop.Call(tc.method, json.RawMessage(tc.params), cfg, exec)
			requireCode(t, perr, tc.code, tc.wants...)
			if result != nil {
				t.Fatalf("出错时不应带结果: %s", result)
			}
			if exec.calls != 0 {
				t.Fatalf("校验失败仍在执行面调用了 %d 次，期望 0 次", exec.calls)
			}
		})
	}
}

// TestBoundaryIdentifierBoundaries 覆盖标识符长度边界：64 合法通过校验、65 拒绝。
// 边界值必须两侧都测，只测拒绝无法证明上限值本身就是允许的。
func TestBoundaryIdentifierBoundaries(t *testing.T) {
	exec := &tripwireExecutor{}
	// 64 字符是合法上限：校验通过后落到执行面（tripwire 会返回错误），说明未在参数层被拒。
	okParams := fmt.Sprintf(`{"instance":"primary","name":%q}`, strings.Repeat("a", 64))
	_, perr := mysqlop.Call("db-mysql.createDatabase", json.RawMessage(okParams), boundaryConfig("/bin/false", "", ""), exec)
	if perr == nil || perr.Code != pluginapi.E_INTERNAL {
		t.Fatalf("64 字符标识符应通过参数校验并落到执行面（期望 tripwire 的 E_INTERNAL），实际=%v", perr)
	}
	if exec.calls != 1 {
		t.Fatalf("64 字符标识符应触达执行面 1 次，实际=%d", exec.calls)
	}
}
