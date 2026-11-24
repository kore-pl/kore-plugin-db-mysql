package mysqlop

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestValidateWriteSQLAllow 合法写语句必须放行，并返回正确分级。
func TestValidateWriteSQLAllow(t *testing.T) {
	cases := []struct {
		name        string
		sql         string
		confirm     bool
		confirmText string
		wantKind    string
	}{
		{"update 带 confirm", "UPDATE users SET name='x' WHERE id=1", true, "", ExecKindData},
		{"insert 带 confirm", "INSERT INTO logs (msg) VALUES ('a;b')", true, "", ExecKindData},
		{"delete 带 confirm", "DELETE FROM logs WHERE id < 10;", true, "", ExecKindData},
		{"replace 带 confirm", "REPLACE INTO kv (k,v) VALUES ('a','b')", true, "", ExecKindData},
		{"drop table 手打短语", "DROP TABLE logs", true, "drop table", ExecKindDDL},
		{"drop table 短语容忍空白", "DROP   TABLE logs", true, "  DROP TABLE ", ExecKindDDL},
		{"alter table 手打短语", "ALTER TABLE users ADD COLUMN age INT", true, "ALTER TABLE", ExecKindDDL},
		{"truncate 手打短语", "TRUNCATE TABLE logs", true, "TRUNCATE TABLE", ExecKindDDL},
		{"create database 手打短语", "CREATE DATABASE kore_x", true, "CREATE DATABASE", ExecKindDDL},
		{"grant 手打短语", "GRANT SELECT ON kore_x.* TO 'u'@'%'", true, "GRANT SELECT", ExecKindDDL},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			kind, perr := ValidateWriteSQL(c.sql, c.confirm, c.confirmText)
			if perr != nil {
				t.Fatalf("期望放行，实际拒绝: %s", perr.Message)
			}
			if kind != c.wantKind {
				t.Fatalf("分级 = %q，期望 %q", kind, c.wantKind)
			}
		})
	}
}

// TestValidateWriteSQLReject 越权 / 未确认 / 多语句 / 侧信道必须 E_INVALID，且错误信息可执行。
func TestValidateWriteSQLReject(t *testing.T) {
	long := "UPDATE t SET a='" + strings.Repeat("x", maxSQLBytes) + "'"
	cases := []struct {
		name        string
		sql         string
		confirm     bool
		confirmText string
		wantHint    string
	}{
		{"空语句", "   ", true, "", "不得为空"},
		{"读语句走 query", "SELECT 1", true, "", "db-mysql.query"},
		{"with 走 query", "WITH x AS (SELECT 1) SELECT * FROM x", true, "", "db-mysql.query"},
		{"未确认", "DELETE FROM t", false, "", "confirm=true"},
		{"ddl 缺确认短语", "DROP TABLE t", true, "", "confirm_text"},
		{"ddl 确认短语不匹配", "DROP TABLE t", true, "DROP", "不匹配"},
		{"多语句", "DELETE FROM t; DELETE FROM u", true, "", "单条语句"},
		{"NUL 字节", "DELETE FROM t\x00", true, "", "NUL"},
		{"超 64KiB", long, true, "", "64 KiB"},
		{"反斜杠命令", `DELETE FROM t \g`, true, "", "反斜杠"},
		{"可执行注释", "DELETE FROM t /*! DROP TABLE u */", true, "", "可执行注释"},
		{"写文件", "UPDATE t SET a=1 INTO OUTFILE '/tmp/x'", true, "", "INTO OUTFILE"},
		{"非白名单前缀", "LOAD DATA INFILE '/etc/passwd' INTO TABLE t", true, "", "写入白名单拒绝"},
		{"空关键字", "'abc'", true, "", "不得为空"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, perr := ValidateWriteSQL(c.sql, c.confirm, c.confirmText)
			if perr == nil {
				t.Fatalf("期望拒绝，实际放行: %q", c.sql)
			}
			if perr.Code != "E_INVALID" {
				t.Fatalf("错误码 = %q，期望 E_INVALID", perr.Code)
			}
			if !strings.Contains(perr.Message, c.wantHint) {
				t.Fatalf("错误信息 %q 未包含 %q", perr.Message, c.wantHint)
			}
		})
	}
}

// TestCallExec 校验：校验失败不触达执行面；合法写语句带回分级与结果形状。
func TestCallExec(t *testing.T) {
	fake := &fakeExecutor{queryResult: QueryResult{}}
	cfg := Config{Instances: []InstanceConfig{{Name: "primary"}}}

	_, perr := callExec(json.RawMessage(`{"instance":"primary","sql":"UPDATE t SET a=1"}`), cfg, fake)
	if perr == nil {
		t.Fatal("未确认的写语句应被拒绝")
	}
	if fake.queryCall != 0 {
		t.Fatalf("拒绝路径不应触达执行面，实际调用 %d 次", fake.queryCall)
	}

	raw, perr := callExec(json.RawMessage(`{"instance":"primary","sql":"UPDATE t SET a=1","confirm":true}`), cfg, fake)
	if perr != nil {
		t.Fatalf("合法写语句应放行: %s", perr.Message)
	}
	var out ExecResult
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("结果解码失败: %v", err)
	}
	if out.Kind != ExecKindData || !out.Executed || out.Instance != "primary" {
		t.Fatalf("结果形状不符: %+v", out)
	}
	if fake.queryCall != 1 || fake.lastSQL != "UPDATE t SET a=1" {
		t.Fatalf("执行面调用不符: call=%d sql=%q", fake.queryCall, fake.lastSQL)
	}

	// ddl 语句：confirm=true 但缺 confirm_text 仍拒绝。
	_, perr = callExec(json.RawMessage(`{"instance":"primary","sql":"DROP TABLE t","confirm":true}`), cfg, fake)
	if perr == nil || !strings.Contains(perr.Message, "confirm_text") {
		t.Fatalf("ddl 缺确认短语应拒绝并给可执行提示，实际: %v", perr)
	}
}
