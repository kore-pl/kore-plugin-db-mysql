package mysqlop

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/pengtang1024/kore/pkg/pluginapi"
)

// 慢查询分析的数据来源：performance_schema.events_statements_summary_by_digest。
//
// 为什么不用 slow_query_log 文件：
//   - 读文件需要 FILE 权限，且日志路径由服务端 log_output/long_query_time 决定，
//     跨主机/容器不可移植；
//   - 插件以低权运行、不直连宿主文件系统，读写文件会超出插件沙箱；
//   - performance_schema 是 SQL 可查的聚合视图（按 digest 归并），已经包含执行次数、
//     平均/最大/总耗时与扫描行数，正好是 DBA 排慢查询要的字段。
const (
	slowlogSource       = "performance_schema"
	defaultSlowLogLimit = 20
	maxSlowLogLimit     = 100
	// maxSlowLogMS 是 min_ms 上限（1 小时），避免无意义的超大值。
	maxSlowLogMS = 3600000
	// slowLogHint 说明数据来源与前置条件（失败时也随错误提示给出）。
	slowLogHint = "数据来自 performance_schema.events_statements_summary_by_digest：需 performance_schema=ON 且连接账号对 performance_schema 有 SELECT 权限；本方法不读 slow_query_log 文件（FILE 权限 + 宿主日志路径，跨主机不可移植）"
)

// slowlogParams 是 db-mysql.slowlog 的显式入参形状。
type slowlogParams struct {
	Instance string `json:"instance"`
	// Schema 限定只统计某个库；空表示全部库。
	Schema string `json:"schema,omitempty"`
	// MinMS 是耗时阈值（毫秒），只返回总耗时 ≥ min_ms 的 digest；0 表示不设阈值。
	MinMS int `json:"min_ms,omitempty"`
	// Limit 是返回条数（按总耗时倒序），1..100，缺省 20。
	Limit int `json:"limit,omitempty"`
}

// SlowQueryItem 是一条按 digest 聚合的慢查询。
type SlowQueryItem struct {
	Schema          string  `json:"schema"`
	Digest          string  `json:"digest"`
	SQLText         string  `json:"sql_text"`
	Executions      int64   `json:"executions"`
	TotalMS         float64 `json:"total_ms"`
	AvgMS           float64 `json:"avg_ms"`
	MaxMS           float64 `json:"max_ms"`
	RowsExaminedAvg float64 `json:"rows_examined_avg"`
	RowsSentAvg     float64 `json:"rows_sent_avg"`
	FirstSeen       string  `json:"first_seen"`
	LastSeen        string  `json:"last_seen"`
}

// SlowLogResult 是 db-mysql.slowlog 的结构化结果。
type SlowLogResult struct {
	Instance string          `json:"instance"`
	Source   string          `json:"source"`
	Schema   string          `json:"schema,omitempty"`
	MinMS    int             `json:"min_ms"`
	Limit    int             `json:"limit"`
	Count    int             `json:"count"`
	Queries  []SlowQueryItem `json:"queries"`
	Warnings []string        `json:"warnings,omitempty"`
	Hint     string          `json:"hint"`
}

// callSlowlog 处理 db-mysql.slowlog：校验参数 → 生成只读聚合 SQL → 结构化结果。
// 只读：SQL 固定为 SELECT，不接受调用方传 SQL，也不写任何服务端状态。
func callSlowlog(raw json.RawMessage, cfg Config, exec Executor) (json.RawMessage, *pluginapi.Error) {
	var p slowlogParams
	if perr := decodeParams(raw, &p); perr != nil {
		return nil, perr
	}
	instance, perr := findInstance(cfg, p.Instance)
	if perr != nil {
		return nil, perr
	}
	schema := strings.TrimSpace(p.Schema)
	if schema != "" {
		if err := ValidateDatabaseName(schema); err != nil {
			return nil, invalid(err.Error())
		}
	}
	if p.MinMS < 0 || p.MinMS > maxSlowLogMS {
		return nil, invalid(fmt.Sprintf("min_ms 必须在 0–%d 之间", maxSlowLogMS))
	}
	limit := p.Limit
	switch {
	case limit == 0:
		limit = defaultSlowLogLimit
	case limit < 0 || limit > maxSlowLogLimit:
		return nil, invalid(fmt.Sprintf("limit 必须在 0–%d 之间", maxSlowLogLimit))
	}
	result, err := exec.Query(instance, buildSlowQuerySQL(schema, p.MinMS, limit), limit)
	if err != nil {
		return nil, classify(err)
	}
	queries := make([]SlowQueryItem, 0, len(result.Rows))
	warnings := make([]string, 0)
	for index, row := range result.Rows {
		item, decodeErr := decodeSlowQueryRow(row)
		if decodeErr != nil {
			warnings = append(warnings, fmt.Sprintf("第 %d 行无法解析，已跳过: %v", index+1, decodeErr))
			continue
		}
		queries = append(queries, item)
	}
	return encode(SlowLogResult{
		Instance: instance.Name,
		Source:   slowlogSource,
		Schema:   schema,
		MinMS:    p.MinMS,
		Limit:    limit,
		Count:    len(queries),
		Queries:  queries,
		Warnings: warnings,
		Hint:     slowLogHint,
	})
}

// buildSlowQuerySQL 生成只读聚合 SQL；schema 已过标识符白名单，min_ms/limit 已校验为整数。
func buildSlowQuerySQL(schema string, minMS, limit int) string {
	var b strings.Builder
	b.WriteString("SELECT SCHEMA_NAME AS schema_name, DIGEST AS digest, DIGEST_TEXT AS sql_text, ")
	b.WriteString("COUNT_STAR AS executions, ")
	b.WriteString("ROUND(SUM_TIMER_WAIT / 1000000000, 3) AS total_ms, ")
	b.WriteString("ROUND(AVG_TIMER_WAIT / 1000000000, 3) AS avg_ms, ")
	b.WriteString("ROUND(MAX_TIMER_WAIT / 1000000000, 3) AS max_ms, ")
	b.WriteString("ROUND(SUM_ROWS_EXAMINED / COUNT_STAR, 1) AS rows_examined_avg, ")
	b.WriteString("ROUND(SUM_ROWS_SENT / COUNT_STAR, 1) AS rows_sent_avg, ")
	b.WriteString("FIRST_SEEN AS first_seen, LAST_SEEN AS last_seen ")
	b.WriteString("FROM performance_schema.events_statements_summary_by_digest ")
	b.WriteString("WHERE SCHEMA_NAME IS NOT NULL")
	if schema != "" {
		// schema 已经过 ValidateDatabaseName（仅字母/数字/下划线），再按字符串字面量编码。
		fmt.Fprintf(&b, " AND SCHEMA_NAME = %s", stringLiteral(schema, true))
	}
	fmt.Fprintf(&b, " AND SUM_TIMER_WAIT / 1000000000 >= %d", minMS)
	b.WriteString(" ORDER BY SUM_TIMER_WAIT DESC")
	fmt.Fprintf(&b, " LIMIT %d", limit)
	return b.String()
}

// slowRow 是执行面行 JSON 的解码形状：可为 NULL 的文本列用指针承接。
type slowRow struct {
	SchemaName      *string `json:"schema_name"`
	Digest          *string `json:"digest"`
	SQLText         *string `json:"sql_text"`
	Executions      int64   `json:"executions"`
	TotalMS         float64 `json:"total_ms"`
	AvgMS           float64 `json:"avg_ms"`
	MaxMS           float64 `json:"max_ms"`
	RowsExaminedAvg float64 `json:"rows_examined_avg"`
	RowsSentAvg     float64 `json:"rows_sent_avg"`
	FirstSeen       *string `json:"first_seen"`
	LastSeen        *string `json:"last_seen"`
}

// decodeSlowQueryRow 把一行 JSON 转成对外结构；NULL 文本归一为空串。
func decodeSlowQueryRow(raw json.RawMessage) (SlowQueryItem, error) {
	var row slowRow
	if err := json.Unmarshal(raw, &row); err != nil {
		return SlowQueryItem{}, err
	}
	if row.SchemaName == nil && row.Digest == nil {
		return SlowQueryItem{}, fmt.Errorf("缺少 schema_name/digest")
	}
	item := SlowQueryItem{
		Executions:      row.Executions,
		TotalMS:         row.TotalMS,
		AvgMS:           row.AvgMS,
		MaxMS:           row.MaxMS,
		RowsExaminedAvg: row.RowsExaminedAvg,
		RowsSentAvg:     row.RowsSentAvg,
	}
	if row.SchemaName != nil {
		item.Schema = *row.SchemaName
	}
	if row.Digest != nil {
		item.Digest = *row.Digest
	}
	if row.SQLText != nil {
		item.SQLText = *row.SQLText
	}
	if row.FirstSeen != nil {
		item.FirstSeen = *row.FirstSeen
	}
	if row.LastSeen != nil {
		item.LastSeen = *row.LastSeen
	}
	return item, nil
}
