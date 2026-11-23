package mysqlop

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/pengtang1024/kore/pkg/pluginapi"
)

// fakeExecutor 记录调用参数并返回固定结果；用于断言校验发生在执行面之前。
type fakeExecutor struct {
	statuses      []InstanceStatus
	databases     []string
	queryResult   QueryResult
	statusResult  StatusResult
	err           error
	instancesCall int
	databasesCall int
	queryCall     int
	statusCall    int
	lastSQL       string
	lastLimit     int
	lastInclude   bool
	lastInstances []InstanceConfig

	// L2 写操作：记录最后一次调用参数，便于断言「校验失败不触达执行面」与参数形状。
	adminCalls     int
	created        bool
	dropped        bool
	exportedSize   int64
	lastDatabase   string
	lastUser       string
	lastHost       string
	lastPassword   string
	lastCharset    string
	lastCollation  string
	lastPrivileges []string
	lastTables     []string
	lastFilePath   string
}

func (f *fakeExecutor) Instances(instances []InstanceConfig) ([]InstanceStatus, error) {
	f.instancesCall++
	f.lastInstances = instances
	return f.statuses, f.err
}

func (f *fakeExecutor) Databases(instance InstanceConfig, includeSystem bool) ([]string, error) {
	f.databasesCall++
	f.lastInclude = includeSystem
	return f.databases, f.err
}

func (f *fakeExecutor) Query(instance InstanceConfig, sql string, limit int) (QueryResult, error) {
	f.queryCall++
	f.lastSQL = sql
	f.lastLimit = limit
	return f.queryResult, f.err
}

func (f *fakeExecutor) Status(instance InstanceConfig) (StatusResult, error) {
	f.statusCall++
	return f.statusResult, f.err
}

func (f *fakeExecutor) CreateDatabase(instance InstanceConfig, name, charset, collation string) (bool, error) {
	f.adminCalls++
	f.lastDatabase, f.lastCharset, f.lastCollation = name, charset, collation
	return f.created, f.err
}

func (f *fakeExecutor) DropDatabase(instance InstanceConfig, name string) (bool, error) {
	f.adminCalls++
	f.lastDatabase = name
	return f.dropped, f.err
}

func (f *fakeExecutor) CreateUser(instance InstanceConfig, user, host, password string) (bool, error) {
	f.adminCalls++
	f.lastUser, f.lastHost, f.lastPassword = user, host, password
	return f.created, f.err
}

func (f *fakeExecutor) ChangePassword(instance InstanceConfig, user, host, password string) (bool, error) {
	f.adminCalls++
	f.lastUser, f.lastHost, f.lastPassword = user, host, password
	return f.created, f.err
}

func (f *fakeExecutor) DropUser(instance InstanceConfig, user, host string) (bool, error) {
	f.adminCalls++
	f.lastUser, f.lastHost = user, host
	return f.dropped, f.err
}

func (f *fakeExecutor) Grant(instance InstanceConfig, user, host, database string, privileges []string) error {
	f.adminCalls++
	f.lastUser, f.lastHost, f.lastDatabase, f.lastPrivileges = user, host, database, privileges
	return f.err
}

func (f *fakeExecutor) Export(instance InstanceConfig, database string, tables []string, filePath string) (int64, error) {
	f.adminCalls++
	f.lastDatabase, f.lastTables, f.lastFilePath = database, tables, filePath
	return f.exportedSize, f.err
}

func (f *fakeExecutor) Import(instance InstanceConfig, database, filePath string) error {
	f.adminCalls++
	f.lastDatabase, f.lastFilePath = database, filePath
	return f.err
}

// callTestConfig 是带一个实例的测试配置。
func callTestConfig() Config {
	return Config{
		Bin:     "mysql",
		Timeout: 5 * time.Second,
		Instances: []InstanceConfig{
			{Name: "primary", Host: "127.0.0.1", Port: 3306, User: "root"},
		},
	}
}

// TestBusinessVerb 方法名归一化：只接受 db-mysql.<verb>，域不符或裸动词一律拒绝。
func TestBusinessVerb(t *testing.T) {
	for _, verb := range []string{
		"instances", "databases", "query", "status",
		"createDatabase", "dropDatabase", "createUser", "changePassword", "dropUser", "grant", "export", "import", "slowlog",
	} {
		method := "db-mysql." + verb
		got, ok := businessVerb(method)
		if !ok || got != verb {
			t.Fatalf("businessVerb(%q)=(%q,%v)，期望 (%q,true)", method, got, ok, verb)
		}
	}
	rejected := []string{
		"db-mysql.exec",          // 未知动词
		"db-mysql.slowQueries",   // 未知动词
		"db-mysql.instances.foo", // 多一段
		"mysql.instances",        // 旧域（协议不一致）
		"instances",              // 裸动词（v0.1.0 起不再兼容）
		"db-mysql.",              // 空动词
	}
	for _, method := range rejected {
		if _, ok := businessVerb(method); ok {
			t.Fatalf("businessVerb(%q) 不应被接受", method)
		}
	}
}

// TestCallInstancesEmptyConfig 无实例配置时返回空列表，不执行任何命令。
func TestCallInstancesEmptyConfig(t *testing.T) {
	result, perr := Call("db-mysql.instances", json.RawMessage(`{}`), Config{}, nil)
	if perr != nil {
		t.Fatalf("db-mysql.instances 错误: %v", perr)
	}
	var decoded InstancesResult
	if err := json.Unmarshal(result, &decoded); err != nil {
		t.Fatalf("结果解析失败: %v (%s)", err, result)
	}
	if decoded.Count != 0 || len(decoded.Instances) != 0 {
		t.Fatalf("结果=%s，期望空列表", result)
	}
}

// TestCallInstancesResult 实例状态透传为结构化结果。
func TestCallInstancesResult(t *testing.T) {
	exec := &fakeExecutor{statuses: []InstanceStatus{
		{Name: "primary", Host: "127.0.0.1", Port: 3306, Reachable: true, Version: "8.4.0"},
	}}
	result, perr := Call("db-mysql.instances", json.RawMessage(`{"instance":"primary"}`), callTestConfig(), exec)
	if perr != nil {
		t.Fatalf("db-mysql.instances 错误: %v", perr)
	}
	if exec.instancesCall != 1 || len(exec.lastInstances) != 1 || exec.lastInstances[0].Name != "primary" {
		t.Fatalf("执行面入参错: calls=%d instances=%+v", exec.instancesCall, exec.lastInstances)
	}
	var decoded InstancesResult
	if err := json.Unmarshal(result, &decoded); err != nil {
		t.Fatalf("结果解析失败: %v", err)
	}
	if decoded.Count != 1 || !decoded.Instances[0].Reachable || decoded.Instances[0].Version != "8.4.0" {
		t.Fatalf("结果=%s", result)
	}
	if strings.Contains(string(result), "pass") || strings.Contains(string(result), "secret") {
		t.Fatalf("结果疑似泄漏密码: %s", result)
	}
}

// TestCallInstancesUnknown 过滤不存在的实例返回 E_NOTFOUND。
func TestCallInstancesUnknown(t *testing.T) {
	_, perr := Call("db-mysql.instances", json.RawMessage(`{"instance":"nope"}`), callTestConfig(), &fakeExecutor{})
	if perr == nil || perr.Code != pluginapi.E_NOTFOUND {
		t.Fatalf("错误=%v，期望 E_NOTFOUND", perr)
	}
}

// TestCallDatabases db-mysql.databases 透传 include_system 并返回库列表。
func TestCallDatabases(t *testing.T) {
	exec := &fakeExecutor{databases: []string{"app", "logs"}}
	result, perr := Call("db-mysql.databases", json.RawMessage(`{"instance":"primary","include_system":true}`), callTestConfig(), exec)
	if perr != nil {
		t.Fatalf("db-mysql.databases 错误: %v", perr)
	}
	if !exec.lastInclude {
		t.Fatal("include_system 未透传")
	}
	var decoded DatabasesResult
	if err := json.Unmarshal(result, &decoded); err != nil {
		t.Fatalf("结果解析失败: %v", err)
	}
	if decoded.Instance != "primary" || decoded.Count != 2 || decoded.Databases[0] != "app" {
		t.Fatalf("结果=%s", result)
	}
}

// TestCallQueryDefaults db-mysql.query 缺省 limit=100，SQL 原样交给执行面。
func TestCallQueryDefaults(t *testing.T) {
	exec := &fakeExecutor{queryResult: QueryResult{
		Columns: []string{"n"},
		Rows:    []json.RawMessage{json.RawMessage(`{"n":1}`)},
	}}
	result, perr := Call("db-mysql.query", json.RawMessage(`{"instance":"primary","sql":"SELECT 1 AS n"}`), callTestConfig(), exec)
	if perr != nil {
		t.Fatalf("db-mysql.query 错误: %v", perr)
	}
	if exec.lastLimit != defaultQueryLimit || exec.lastSQL != "SELECT 1 AS n" {
		t.Fatalf("执行面入参错: limit=%d sql=%q", exec.lastLimit, exec.lastSQL)
	}
	var decoded QueryResult
	if err := json.Unmarshal(result, &decoded); err != nil {
		t.Fatalf("结果解析失败: %v", err)
	}
	if decoded.RowCount != 1 || string(decoded.Rows[0]) != `{"n":1}` || decoded.Truncated {
		t.Fatalf("结果=%s", result)
	}
}

// TestCallQueryLimitBounds limit 越界拒绝且不触达执行面。
func TestCallQueryLimitBounds(t *testing.T) {
	for _, limit := range []int{-1, maxQueryLimit + 1} {
		exec := &fakeExecutor{}
		_, perr := Call("db-mysql.query", json.RawMessage(`{"instance":"primary","sql":"SELECT 1","limit":`+itoa(limit)+`}`), callTestConfig(), exec)
		if perr == nil || perr.Code != pluginapi.E_INVALID {
			t.Fatalf("limit=%d 错误=%v，期望 E_INVALID", limit, perr)
		}
		if exec.queryCall != 0 {
			t.Fatalf("limit=%d 越界仍触达执行面", limit)
		}
	}
}

// TestCallQueryWriteRejected 写操作/多语句在触达数据库前返回 E_INVALID。
func TestCallQueryWriteRejected(t *testing.T) {
	rejected := []string{
		"INSERT INTO t VALUES (1)",
		"UPDATE t SET a = 1",
		"DELETE FROM t",
		"DROP DATABASE mysql",
		"TRUNCATE t",
		"SELECT 1; DROP TABLE t",
	}
	for _, sql := range rejected {
		exec := &fakeExecutor{}
		params, _ := json.Marshal(map[string]any{"instance": "primary", "sql": sql})
		_, perr := Call("db-mysql.query", params, callTestConfig(), exec)
		if perr == nil || perr.Code != pluginapi.E_INVALID {
			t.Fatalf("sql=%q 错误=%v，期望 E_INVALID", sql, perr)
		}
		if exec.queryCall != 0 {
			t.Fatalf("sql=%q 被拒后仍触达执行面", sql)
		}
	}
}

// TestCallQueryValidation 缺参、实例不存在、实例名非法。
func TestCallQueryValidation(t *testing.T) {
	cases := []struct {
		params string
		code   string
	}{
		{`{"instance":"primary"}`, pluginapi.E_INVALID},
		{`{"instance":"primary","sql":"   "}`, pluginapi.E_INVALID},
		{`{"sql":"SELECT 1"}`, pluginapi.E_INVALID},
		{`{"instance":"../etc","sql":"SELECT 1"}`, pluginapi.E_INVALID},
		{`{"instance":"nope","sql":"SELECT 1"}`, pluginapi.E_NOTFOUND},
		{`{"instance":"primary","sql":"SELECT 1","limit":"x"}`, pluginapi.E_INVALID},
	}
	for _, c := range cases {
		_, perr := Call("db-mysql.query", json.RawMessage(c.params), callTestConfig(), &fakeExecutor{})
		if perr == nil || perr.Code != c.code {
			t.Fatalf("params=%s 错误=%v，期望 %s", c.params, perr, c.code)
		}
	}
}

// TestCallStatus db-mysql.status 透传结构化指标。
func TestCallStatus(t *testing.T) {
	exec := &fakeExecutor{statusResult: StatusResult{
		Instance: "primary", Version: "8.4.0", MaxConnections: 151,
		ThreadsConnected: 2, UptimeS: 100, Queries: 42, SlowQueries: 0,
	}}
	result, perr := Call("db-mysql.status", json.RawMessage(`{"instance":"primary"}`), callTestConfig(), exec)
	if perr != nil {
		t.Fatalf("db-mysql.status 错误: %v", perr)
	}
	if exec.statusCall != 1 {
		t.Fatalf("status 调用次数=%d", exec.statusCall)
	}
	var decoded StatusResult
	if err := json.Unmarshal(result, &decoded); err != nil {
		t.Fatalf("结果解析失败: %v", err)
	}
	if decoded.Version != "8.4.0" || decoded.MaxConnections != 151 || decoded.Queries != 42 {
		t.Fatalf("结果=%s", result)
	}
}

// TestCallUnknownMethod 未知方法返回 E_NOTFOUND。
func TestCallUnknownMethod(t *testing.T) {
	_, perr := Call("db-mysql.exec", json.RawMessage(`{}`), callTestConfig(), &fakeExecutor{})
	if perr == nil || perr.Code != pluginapi.E_NOTFOUND {
		t.Fatalf("未知方法错误=%v，期望 E_NOTFOUND", perr)
	}
}

// TestCallOpErrorPassthrough 执行面 OpError 的错误码原样透传。
func TestCallOpErrorPassthrough(t *testing.T) {
	exec := &fakeExecutor{err: opErrorf(pluginapi.E_UPSTREAM, "连接被拒绝")}
	_, perr := Call("db-mysql.status", json.RawMessage(`{"instance":"primary"}`), callTestConfig(), exec)
	if perr == nil || perr.Code != pluginapi.E_UPSTREAM || perr.Message != "连接被拒绝" {
		t.Fatalf("错误=%v，期望 E_UPSTREAM 透传", perr)
	}
}

// itoa 避免在测试里为一个小转换引入 strconv。
func itoa(v int) string {
	data, _ := json.Marshal(v)
	return string(data)
}
