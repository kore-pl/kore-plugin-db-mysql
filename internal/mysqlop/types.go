// Package mysqlop 实现 db-mysql 插件的方法面：实例配置解析、只读 SQL 白名单、
// 通过本机 mysql 客户端（os/exec）执行查询并结构化结果。
//
// 依赖边界（G-17）：只依赖 kore/pkg/pluginapi 与 Go 标准库，不引入任何
// MySQL driver、不内置 mysql 客户端二进制；客户端由宿主机 PATH 提供，
// 可用 KORE_MYSQL_BIN 指到绝对路径。
package mysqlop

import (
	"encoding/json"
	"fmt"
	"time"
)

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

// InstanceConfig 是一条实例配置。密码只以环境变量名（PasswordEnv）出现，
// 结构体本身不携带明文密码，也不允许密码进入 argv 或返回值。
type InstanceConfig struct {
	Name        string `json:"name"`
	Host        string `json:"host"`
	Port        int    `json:"port"`
	User        string `json:"user"`
	PasswordEnv string `json:"password_env"`
}

// InstanceStatus 是 mysql.instances 的单实例结果。Error 为空串表示探测成功。
type InstanceStatus struct {
	Name      string `json:"name"`
	Host      string `json:"host"`
	Port      int    `json:"port"`
	Reachable bool   `json:"reachable"`
	Version   string `json:"version"`
	Error     string `json:"error"`
}

// Config 是插件运行期 MySQL 配置。
type Config struct {
	Instances []InstanceConfig
	Bin       string
	Timeout   time.Duration
	// Err 是配置解析错误；非 nil 时所有业务方法返回 E_INVALID，但插件仍可启动。
	Err error
}

// Executor 是数据库操作执行面：真实实现是本机 mysql 客户端，测试可注入故障。
type Executor interface {
	Instances(instances []InstanceConfig) ([]InstanceStatus, error)
	Databases(instance InstanceConfig, includeSystem bool) ([]string, error)
	Query(instance InstanceConfig, sql string, limit int) (QueryResult, error)
	Status(instance InstanceConfig) (StatusResult, error)
}

// InstancesResult 是 mysql.instances 的结构化结果。
type InstancesResult struct {
	Instances []InstanceStatus `json:"instances"`
	Count     int              `json:"count"`
}

// DatabasesResult 是 mysql.databases 的结构化结果。
type DatabasesResult struct {
	Instance  string   `json:"instance"`
	Databases []string `json:"databases"`
	Count     int      `json:"count"`
}

// QueryResult 是 mysql.query 的结构化结果。
//
// Rows 的每个元素是已编码的 JSON 对象（列名 → 值），值由客户端文本推断为
// null / 数字 / 字符串；行内容动态，因此在解析收口处直接产出 json.RawMessage，
// 不向对外 schema 泄漏 map[string]any（G-24）。
type QueryResult struct {
	Columns   []string          `json:"columns"`
	Rows      []json.RawMessage `json:"rows"`
	RowCount  int               `json:"row_count"`
	Truncated bool              `json:"truncated"`
}

// StatusResult 是 mysql.status 的结构化结果。
type StatusResult struct {
	Instance         string `json:"instance"`
	Version          string `json:"version"`
	MaxConnections   int64  `json:"max_connections"`
	ThreadsConnected int64  `json:"threads_connected"`
	UptimeS          int64  `json:"uptime_s"`
	Queries          int64  `json:"queries"`
	SlowQueries      int64  `json:"slow_queries"`
}

// instancesParams 是 mysql.instances 的显式入参形状（instance 可选）。
type instancesParams struct {
	Instance string `json:"instance"`
}

// databasesParams 是 mysql.databases 的显式入参形状。
type databasesParams struct {
	Instance      string `json:"instance"`
	IncludeSystem bool   `json:"include_system"`
}

// queryParams 是 mysql.query 的显式入参形状。
type queryParams struct {
	Instance string `json:"instance"`
	SQL      string `json:"sql"`
	Limit    int    `json:"limit"`
}

// statusParams 是 mysql.status 的显式入参形状。
type statusParams struct {
	Instance string `json:"instance"`
}
