// Package mysqlop 实现 db-mysql 插件的方法面：参数校验、实例名约束与结构化结果。
// 本包只做逻辑校验与执行面调用，不真连数据库（骨架口径）。
package mysqlop

import "fmt"

// FaultKind 标识一次故障注入的类别。
type FaultKind string

const (
	FaultNone    FaultKind = ""
	FaultKill    FaultKind = "kill"
	FaultTimeout FaultKind = "timeout"
	FaultDisk    FaultKind = "disk"
	FaultCgroup  FaultKind = "cgroup"
)

// FaultError 是执行面注入的故障错误，Call 将其映射为协议错误码。
type FaultError struct {
	Kind    FaultKind
	Message string
}

// Error 实现 error 接口，输出 "<类别>: <说明>"。
func (e *FaultError) Error() string { return fmt.Sprintf("%s: %s", e.Kind, e.Message) }

// NewFaultError 构造给定类别的故障错误。
func NewFaultError(kind FaultKind, message string) *FaultError {
	return &FaultError{Kind: kind, Message: message}
}

// Instance 是一个逻辑数据库实例的占位描述。
type Instance struct {
	Name   string `json:"name"`
	Engine string `json:"engine"`
	Status string `json:"status"`
	Port   int    `json:"port"`
}

// SlowQuery 是一条慢查询的占位描述。
type SlowQuery struct {
	Fingerprint  string `json:"fingerprint"`
	Samples      int    `json:"samples"`
	QueryTimeMS  int64  `json:"query_time_ms"`
	RowsExamined int64  `json:"rows_examined"`
}

// Filter 是 mysql.slowQueries 的显式查询参数形状。
type Filter struct {
	Instance    string `json:"instance"`
	ThresholdMS int    `json:"threshold_ms"`
	Limit       int    `json:"limit"`
	Window      string `json:"window"`
}

// InstancesResult 是 mysql.instances 的结构化结果。
type InstancesResult struct {
	Root      string     `json:"root"`
	Instances []Instance `json:"instances"`
	Count     int        `json:"count"`
}

// SlowQueriesResult 是 mysql.slowQueries 的结构化结果。
type SlowQueriesResult struct {
	Instance    string      `json:"instance"`
	ThresholdMS int         `json:"threshold_ms"`
	Limit       int         `json:"limit"`
	Window      string      `json:"window"`
	Queries     []SlowQuery `json:"queries"`
	Count       int         `json:"count"`
}

// Executor 是数据库操作的执行面；骨架实现为 noop，测试可注入故障执行器。
type Executor interface {
	ListInstances(root string) ([]Instance, error)
	SlowQueries(root string, filter Filter) ([]SlowQuery, error)
}

// instancesParams 是 mysql.instances 的显式入参形状（instance 可选，空串表示列出全部）。
type instancesParams struct {
	Instance string `json:"instance"`
}
