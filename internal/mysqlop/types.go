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

// InstanceConfig 是一条实例配置。密码可以来自配置契约（Password，仅默认实例）
// 或环境变量名（PasswordEnv）；两者都不会进入 argv 或任何响应。
type InstanceConfig struct {
	Name        string `json:"name"`
	Host        string `json:"host"`
	Port        int    `json:"port"`
	User        string `json:"user"`
	PasswordEnv string `json:"password_env"`
	// Password 是配置契约直接保存的密码（仅默认实例使用）；json:"-" 保证它
	// 不会出现在任何响应里，也不接受 KORE_MYSQL_INSTANCES 的 password 字段。
	Password string `json:"-"`
}

// InstanceStatus 是 db-mysql.instances 的单实例结果。Error 为空串表示探测成功。
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
	// Bin / DumpBin 是 mysql 与 mysqldump 客户端路径（缺省走 PATH）。
	Bin     string
	DumpBin string
	Timeout time.Duration
	// IncludeSystem 是 db-mysql.databases 未显式传 include_system 时的默认值。
	IncludeSystem bool
	// Admin 表示插件的 info.json 已声明 system:admin：L2 写操作的前置门控。
	Admin bool
	// ExportDir 是 import/export 的文件白名单根（KORE_DB_EXPORT_DIR，缺省 <state>/exports）。
	ExportDir string
	// Err 是配置解析错误；非 nil 时所有业务方法返回 E_INVALID，但插件仍可启动。
	Err error
}

// Executor 是数据库操作执行面：真实实现是本机 mysql/mysqldump 客户端，测试可注入故障。
//
// L2 写操作的方法层负责「校验 + 安全边界」（confirm / 系统库 / root / 白名单 / 路径），
// 执行面只负责把已通过校验的参数变成真实 SQL / 子进程调用。
type Executor interface {
	Instances(instances []InstanceConfig) ([]InstanceStatus, error)
	Databases(instance InstanceConfig, includeSystem bool) ([]string, error)
	Query(instance InstanceConfig, sql string, limit int) (QueryResult, error)
	Status(instance InstanceConfig) (StatusResult, error)

	CreateDatabase(instance InstanceConfig, name, charset, collation string) (bool, error)
	DropDatabase(instance InstanceConfig, name string) (bool, error)
	CreateUser(instance InstanceConfig, user, host, password string) (bool, error)
	DropUser(instance InstanceConfig, user, host string) (bool, error)
	Grant(instance InstanceConfig, user, host, database string, privileges []string) error
	Export(instance InstanceConfig, database string, tables []string, filePath string) (int64, error)
	Import(instance InstanceConfig, database, filePath string) error
}

// InstancesResult 是 db-mysql.instances 的结构化结果。
type InstancesResult struct {
	Instances []InstanceStatus `json:"instances"`
	Count     int              `json:"count"`
}

// DatabasesResult 是 db-mysql.databases 的结构化结果。
type DatabasesResult struct {
	Instance  string   `json:"instance"`
	Databases []string `json:"databases"`
	Count     int      `json:"count"`
}

// QueryResult 是 db-mysql.query 的结构化结果。
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

// StatusResult 是 db-mysql.status 的结构化结果。
type StatusResult struct {
	Instance         string `json:"instance"`
	Version          string `json:"version"`
	MaxConnections   int64  `json:"max_connections"`
	ThreadsConnected int64  `json:"threads_connected"`
	UptimeS          int64  `json:"uptime_s"`
	Queries          int64  `json:"queries"`
	SlowQueries      int64  `json:"slow_queries"`
}

// instancesParams 是 db-mysql.instances 的显式入参形状（instance 可选）。
type instancesParams struct {
	Instance string `json:"instance"`
}

// databasesParams 是 db-mysql.databases 的显式入参形状。
type databasesParams struct {
	Instance      string `json:"instance"`
	IncludeSystem *bool  `json:"include_system"`
}

// queryParams 是 db-mysql.query 的显式入参形状。
type queryParams struct {
	Instance string `json:"instance"`
	SQL      string `json:"sql"`
	Limit    int    `json:"limit"`
}

// statusParams 是 db-mysql.status 的显式入参形状。
type statusParams struct {
	Instance string `json:"instance"`
}

// createDatabaseParams 是 db-mysql.createDatabase 的入参形状。
type createDatabaseParams struct {
	Instance  string `json:"instance"`
	Name      string `json:"name"`
	Charset   string `json:"charset"`
	Collation string `json:"collation"`
}

// dropDatabaseParams 是 db-mysql.dropDatabase 的入参形状；Confirm 必须为 true。
type dropDatabaseParams struct {
	Instance string `json:"instance"`
	Name     string `json:"name"`
	Confirm  bool   `json:"confirm"`
}

// createUserParams 是 db-mysql.createUser 的入参形状。
// Grants 是可选便捷授权，每项形如 "<database>:SELECT,INSERT"。
type createUserParams struct {
	Instance string   `json:"instance"`
	User     string   `json:"user"`
	Host     string   `json:"host"`
	Password string   `json:"password"`
	Grants   []string `json:"grants"`
}

// dropUserParams 是 db-mysql.dropUser 的入参形状；Confirm 必须为 true。
type dropUserParams struct {
	Instance string `json:"instance"`
	User     string `json:"user"`
	Host     string `json:"host"`
	Confirm  bool   `json:"confirm"`
}

// grantParams 是 db-mysql.grant 的入参形状。
type grantParams struct {
	Instance   string   `json:"instance"`
	User       string   `json:"user"`
	Host       string   `json:"host"`
	Database   string   `json:"database"`
	Privileges []string `json:"privileges"`
}

// exportParams 是 db-mysql.export 的入参形状。
type exportParams struct {
	Instance string   `json:"instance"`
	Database string   `json:"database"`
	Tables   []string `json:"tables"`
	FilePath string   `json:"file_path"`
}

// importParams 是 db-mysql.import 的入参形状。
type importParams struct {
	Instance string `json:"instance"`
	Database string `json:"database"`
	FilePath string `json:"file_path"`
}

// CreateDatabaseResult 是 db-mysql.createDatabase 的结果。
type CreateDatabaseResult struct {
	Created bool   `json:"created"`
	Name    string `json:"name"`
}

// DropDatabaseResult 是 db-mysql.dropDatabase 的结果。
type DropDatabaseResult struct {
	Dropped bool   `json:"dropped"`
	Name    string `json:"name"`
}

// CreateUserResult 是 db-mysql.createUser 的结果；**不含密码**。
type CreateUserResult struct {
	Created bool   `json:"created"`
	User    string `json:"user"`
	Host    string `json:"host"`
}

// DropUserResult 是 db-mysql.dropUser 的结果。
type DropUserResult struct {
	Dropped bool `json:"dropped"`
}

// GrantResult 是 db-mysql.grant 的结果。
type GrantResult struct {
	Granted    bool     `json:"granted"`
	Privileges []string `json:"privileges"`
}

// ExportResult 是 db-mysql.export 的结果。
type ExportResult struct {
	Database  string   `json:"database"`
	FilePath  string   `json:"file_path"`
	SizeBytes int64    `json:"size_bytes"`
	Tables    []string `json:"tables"`
}

// ImportResult 是 db-mysql.import 的结果。
type ImportResult struct {
	Database  string `json:"database"`
	FilePath  string `json:"file_path"`
	SizeBytes int64  `json:"size_bytes"`
}
