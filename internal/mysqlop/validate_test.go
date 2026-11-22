package mysqlop

import (
	"strings"
	"testing"
)

// TestValidateInstance 合法/非法实例名边界。
func TestValidateInstance(t *testing.T) {
	valid := []string{"primary", "db-01", "replica.prod", "a"}
	for _, name := range valid {
		if err := ValidateInstance(name); err != nil {
			t.Fatalf("ValidateInstance(%q) 意外拒绝: %v", name, err)
		}
	}
	invalidNames := []string{"", "Upper", "-lead", "../etc", "a/b", "a\\b", "a..b", strings.Repeat("x", 65), "has space"}
	for _, name := range invalidNames {
		if err := ValidateInstance(name); err == nil {
			t.Fatalf("ValidateInstance(%q) 未拒绝", name)
		}
	}
}

// TestSQLWhitelistAllows 只读语句（含结尾分号、注释、字面量里的分号/关键字）放行。
func TestSQLWhitelistAllows(t *testing.T) {
	allowed := []string{
		"SELECT 1",
		"  select * from t where id = 1  ",
		"SELECT 1;",
		"SELECT 1 ;  ",
		"SHOW DATABASES",
		"show status",
		"DESCRIBE users",
		"DESC users",
		"EXPLAIN SELECT 1",
		"EXPLAIN ANALYZE SELECT 1",
		"SELECT ';' AS semi, 'DROP TABLE x' AS s",
		"SELECT 1 -- 末尾注释",
		"SELECT /* 普通注释 */ 1",
		"SELECT 1 # hash 注释",
		"SELECT `select` FROM `table`",
		"SELECT 'it''s' AS quoted",
	}
	for _, sql := range allowed {
		if perr := ValidateReadOnlySQL(sql); perr != nil {
			t.Fatalf("ValidateReadOnlySQL(%q) 意外拒绝: %v", sql, perr)
		}
	}
}

// TestSQLWhitelistRejects 写语句、多语句、客户端命令与外泄通道全部拒绝。
func TestSQLWhitelistRejects(t *testing.T) {
	rejected := []string{
		"",
		"   ",
		"INSERT INTO t VALUES (1)",
		"insert into t values (1)",
		"UPDATE t SET a = 1",
		"DELETE FROM t",
		"DROP DATABASE mysql",
		"TRUNCATE TABLE t",
		"CREATE TABLE t (a int)",
		"ALTER TABLE t ADD c int",
		"REPLACE INTO t VALUES (1)",
		"CALL p()",
		"SET @a = 1",
		"USE mysql",
		"SELECT 1; SELECT 2",
		"SELECT 1;DROP TABLE t",
		"SELECT 1;;",
		"SELECT 1; /* x */ SELECT 2",
		"SELECT 1 INTO OUTFILE '/tmp/x'",
		"SELECT 1 INTO DUMPFILE '/tmp/x'",
		"SELECT * FROM t FOR UPDATE",
		"SELECT * FROM t FOR SHARE",
		"SELECT * FROM t LOCK IN SHARE MODE",
		"SELECT SLEEP(10)",
		"SELECT BENCHMARK(1000000, MD5('x'))",
		"SELECT LOAD_FILE('/etc/passwd')",
		"SELECT GET_LOCK('a', 1)",
		"SELECT /*! 1 */",
		"SELECT 1\\g",
		"SELECT 1\\!id",
		"EXPLAIN ANALYZE DELETE FROM t",
		"SELECT 'unterminated",
		"/* 未闭合注释 SELECT 1",
	}
	for _, sql := range rejected {
		perr := ValidateReadOnlySQL(sql)
		if perr == nil {
			t.Fatalf("ValidateReadOnlySQL(%q) 未拒绝", sql)
		}
		if perr.Code != "E_INVALID" {
			t.Fatalf("ValidateReadOnlySQL(%q) 错误码=%q，期望 E_INVALID", sql, perr.Code)
		}
	}
}

// TestSQLWhitelistRejectsNUL SQL 中的 NUL 字节直接拒绝。
func TestSQLWhitelistRejectsNUL(t *testing.T) {
	if perr := ValidateReadOnlySQL("SELECT 1\x00"); perr == nil {
		t.Fatal("含 NUL 的 SQL 未拒绝")
	}
}

// TestSQLWhitelistTooLong 超长 SQL 拒绝。
func TestSQLWhitelistTooLong(t *testing.T) {
	sql := "SELECT '" + strings.Repeat("a", maxSQLBytes) + "'"
	if perr := ValidateReadOnlySQL(sql); perr == nil {
		t.Fatal("超长 SQL 未拒绝")
	}
}
