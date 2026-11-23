package mysqlop

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/pengtang1024/kore/pkg/pluginapi"
)

// slowlogRows 是两行典型的 performance_schema 聚合结果。
func slowlogRows(t *testing.T) []json.RawMessage {
	t.Helper()
	rows := []string{
		`{"schema_name":"shop","digest":"abc123","sql_text":"SELECT * FROM orders WHERE id = ?","executions":42,"total_ms":3210.5,"avg_ms":76.44,"max_ms":900.1,"rows_examined_avg":1200.5,"rows_sent_avg":1.0,"first_seen":"2026-11-20 10:00:00","last_seen":"2026-11-23 09:00:00"}`,
		`{"schema_name":"shop","digest":"def456","sql_text":"UPDATE inventory SET qty = ? WHERE sku = ?","executions":7,"total_ms":880.0,"avg_ms":125.7,"max_ms":400.0,"rows_examined_avg":5.0,"rows_sent_avg":0.0,"first_seen":"2026-11-22 08:00:00","last_seen":"2026-11-23 08:30:00"}`,
	}
	out := make([]json.RawMessage, 0, len(rows))
	for _, row := range rows {
		out = append(out, json.RawMessage(row))
	}
	return out
}

// TestSlowlogContract 校验入参 → 聚合 SQL → 结构化结果的完整契约。
func TestSlowlogContract(t *testing.T) {
	exec := &fakeExecutor{queryResult: QueryResult{Rows: slowlogRows(t)}}
	raw, perr := Call("db-mysql.slowlog",
		json.RawMessage(`{"instance":"primary","schema":"shop","min_ms":50,"limit":5}`), callTestConfig(), exec)
	if perr != nil {
		t.Fatalf("slowlog 失败: %+v", perr)
	}
	var got SlowLogResult
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("解析结果: %v (%s)", err, raw)
	}
	if got.Instance != "primary" || got.Source != "performance_schema" || got.Schema != "shop" ||
		got.MinMS != 50 || got.Limit != 5 || got.Count != 2 || len(got.Queries) != 2 {
		t.Fatalf("结果形状不对: %+v", got)
	}
	first := got.Queries[0]
	if first.Digest != "abc123" || first.Executions != 42 || first.TotalMS != 3210.5 ||
		first.AvgMS != 76.44 || first.MaxMS != 900.1 || first.RowsExaminedAvg != 1200.5 ||
		first.FirstSeen == "" || first.LastSeen == "" || !strings.Contains(first.SQLText, "orders") {
		t.Fatalf("第一条慢查询解析不对: %+v", first)
	}
	sql := exec.lastSQL
	for _, want := range []string{
		"performance_schema.events_statements_summary_by_digest",
		"COUNT_STAR AS executions",
		"ORDER BY SUM_TIMER_WAIT DESC",
		"SCHEMA_NAME = 'shop'",
		"SUM_TIMER_WAIT / 1000000000 >= 50",
		"LIMIT 5",
	} {
		if !strings.Contains(sql, want) {
			t.Fatalf("SQL 缺少 %q: %s", want, sql)
		}
	}
	if !strings.HasPrefix(strings.TrimSpace(sql), "SELECT ") {
		t.Fatalf("slowlog 必须是只读 SELECT: %s", sql)
	}
	if exec.lastLimit != 5 || exec.queryCall != 1 {
		t.Fatalf("limit/调用次数不对: limit=%d calls=%d", exec.lastLimit, exec.queryCall)
	}
}

// TestSlowlogDefaults 校验缺省 limit=20、min_ms=0、不限定 schema。
func TestSlowlogDefaults(t *testing.T) {
	exec := &fakeExecutor{}
	if _, perr := Call("db-mysql.slowlog", json.RawMessage(`{"instance":"primary"}`), callTestConfig(), exec); perr != nil {
		t.Fatalf("slowlog 失败: %+v", perr)
	}
	if exec.lastLimit != defaultSlowLogLimit {
		t.Fatalf("缺省 limit=%d，期望 %d", exec.lastLimit, defaultSlowLogLimit)
	}
	if strings.Contains(exec.lastSQL, "SCHEMA_NAME = '") {
		t.Fatalf("未传 schema 时不应带库过滤: %s", exec.lastSQL)
	}
	if !strings.Contains(exec.lastSQL, ">= 0") || !strings.Contains(exec.lastSQL, "LIMIT 20") {
		t.Fatalf("缺省 SQL 不对: %s", exec.lastSQL)
	}
}

// TestSlowlogValidation 校验参数与实例边界：校验失败不触达执行面。
func TestSlowlogValidation(t *testing.T) {
	cases := []struct {
		name   string
		params string
		code   string
	}{
		{"缺 instance", `{}`, pluginapi.E_INVALID},
		{"未知实例", `{"instance":"absent"}`, pluginapi.E_NOTFOUND},
		{"非法 schema", `{"instance":"primary","schema":"shop;drop"}`, pluginapi.E_INVALID},
		{"limit 越界", `{"instance":"primary","limit":101}`, pluginapi.E_INVALID},
		{"负 limit", `{"instance":"primary","limit":-1}`, pluginapi.E_INVALID},
		{"min_ms 越界", `{"instance":"primary","min_ms":3600001}`, pluginapi.E_INVALID},
		{"负 min_ms", `{"instance":"primary","min_ms":-1}`, pluginapi.E_INVALID},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			exec := &fakeExecutor{}
			_, perr := Call("db-mysql.slowlog", json.RawMessage(c.params), callTestConfig(), exec)
			if perr == nil || perr.Code != c.code {
				t.Fatalf("期望 %s，得到 %+v", c.code, perr)
			}
			if exec.queryCall != 0 {
				t.Fatalf("校验失败仍触达执行面（queryCall=%d）", exec.queryCall)
			}
		})
	}
}

// TestSlowlogSkipsMalformedRow 校验坏行只记 warning 并跳过，不失败整个调用。
func TestSlowlogSkipsMalformedRow(t *testing.T) {
	exec := &fakeExecutor{queryResult: QueryResult{Rows: []json.RawMessage{
		json.RawMessage(`{"schema_name":"shop","digest":"ok","executions":1,"total_ms":10}`),
		json.RawMessage(`not-json`),
		json.RawMessage(`{"executions":1}`), // 缺 schema/digest
	}}}
	raw, perr := Call("db-mysql.slowlog", json.RawMessage(`{"instance":"primary"}`), callTestConfig(), exec)
	if perr != nil {
		t.Fatalf("slowlog 失败: %+v", perr)
	}
	var got SlowLogResult
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("解析: %v", err)
	}
	if got.Count != 1 || len(got.Queries) != 1 || got.Queries[0].Digest != "ok" {
		t.Fatalf("应只保留 1 条有效行: %+v", got)
	}
	if len(got.Warnings) != 2 {
		t.Fatalf("应有 2 条 warning: %+v", got.Warnings)
	}
}

// TestSlowlogNullColumns 校验 NULL 文本列归一为空串（JSON null 不能解进 string）。
func TestSlowlogNullColumns(t *testing.T) {
	exec := &fakeExecutor{queryResult: QueryResult{Rows: []json.RawMessage{
		json.RawMessage(`{"schema_name":"shop","digest":"d","sql_text":null,"executions":2,"total_ms":1,"first_seen":null,"last_seen":null}`),
	}}}
	raw, perr := Call("db-mysql.slowlog", json.RawMessage(`{"instance":"primary"}`), callTestConfig(), exec)
	if perr != nil {
		t.Fatalf("slowlog 失败: %+v", perr)
	}
	var got SlowLogResult
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("解析: %v", err)
	}
	if got.Count != 1 || got.Queries[0].SQLText != "" || got.Queries[0].FirstSeen != "" {
		t.Fatalf("NULL 列应归一为空串: %+v", got.Queries)
	}
}

// TestSlowlogQueryError 校验执行面故障按四类映射（超时 → E_UPSTREAM）。
func TestSlowlogQueryError(t *testing.T) {
	exec := &fakeExecutor{err: NewFaultError(FaultTimeout, "slowlog 执行面故障")}
	_, perr := Call("db-mysql.slowlog", json.RawMessage(`{"instance":"primary"}`), callTestConfig(), exec)
	if perr == nil || perr.Code != pluginapi.E_UPSTREAM || !strings.Contains(perr.Message, "超时") {
		t.Fatalf("期望 E_UPSTREAM + 超时，得到 %+v", perr)
	}
}
