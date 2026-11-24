package mysqlop

import (
	"encoding/json"

	"github.com/pengtang1024/kore/pkg/pluginapi"
)

// maxExecRows 限制写语句可能返回的结果集行数（防御性；写语句通常无结果集）。
const maxExecRows = 100

// execParams 是 db-mysql.exec 的显式入参形状。
// confirm 为 true 表示调用方已完成一次确认；ddl 级语句还要求 confirm_text 等于
// 语句首两个关键字（如 "DROP TABLE"），由 ValidateWriteSQL 校验。
type execParams struct {
	Instance    string `json:"instance"`
	SQL         string `json:"sql"`
	Confirm     bool   `json:"confirm"`
	ConfirmText string `json:"confirm_text"`
}

// ExecResult 是 db-mysql.exec 的结构化结果。
type ExecResult struct {
	Instance string            `json:"instance"`
	Kind     string            `json:"kind"`
	Executed bool              `json:"executed"`
	Columns  []string          `json:"columns"`
	Rows     []json.RawMessage `json:"rows"`
	RowCount int               `json:"row_count"`
}

// callExec 处理 db-mysql.exec：两级确认 → 写入白名单 → 执行 → 结构化结果。
//
// 为什么单独开方法而不是放开 db-mysql.query：方法级 capability 决定授权面。
// query 声明 system:read，把写语句塞进去等于给「只读操作员」发 DROP 权限；
// exec 声明 system:admin，写能力只对管理员开放，内核 invoke 审计（audit.invoke_all）
// 可覆盖其全量调用，拒绝路径由 capability 审计 fail-closed 记录。
func callExec(raw json.RawMessage, cfg Config, exec Executor) (json.RawMessage, *pluginapi.Error) {
	var p execParams
	if perr := decodeParams(raw, &p); perr != nil {
		return nil, perr
	}
	kind, perr := ValidateWriteSQL(p.SQL, p.Confirm, p.ConfirmText)
	if perr != nil {
		return nil, perr
	}
	instance, perr := findInstance(cfg, p.Instance)
	if perr != nil {
		return nil, perr
	}
	result, err := exec.Query(instance, p.SQL, maxExecRows)
	if err != nil {
		return nil, classify(err)
	}
	out := ExecResult{
		Instance: p.Instance,
		Kind:     kind,
		Executed: true,
		Columns:  result.Columns,
		Rows:     result.Rows,
	}
	if out.Columns == nil {
		out.Columns = []string{}
	}
	if out.Rows == nil {
		out.Rows = []json.RawMessage{}
	}
	out.RowCount = len(out.Rows)
	return encode(out)
}
