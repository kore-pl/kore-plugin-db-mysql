// Package fault 对 db-mysql 插件做四类故障注入，验证方法级错误返回。
// 每个用例向 mysqlop.Call 的执行面注入一种故障，断言方法返回的结构化错误码。
package fault

import (
	"encoding/json"
	"testing"

	"github.com/pengtang1024/kore-plugin-db-mysql/internal/mysqlop"
	"github.com/pengtang1024/kore/pkg/pluginapi"
)

// testConfig 是带一个实例的插件配置。
var testConfig = mysqlop.Config{
	Bin:     "mysql",
	Timeout: 0,
	Instances: []mysqlop.InstanceConfig{
		{Name: "primary", Host: "127.0.0.1", Port: 3306, User: "root"},
	},
}

// faultExecutor 按注入类别让每个方法都失败，模拟四类运行时故障。
type faultExecutor struct {
	kind mysqlop.FaultKind
}

func (f faultExecutor) Instances(instances []mysqlop.InstanceConfig) ([]mysqlop.InstanceStatus, error) {
	return nil, mysqlop.NewFaultError(f.kind, "instances 执行面故障")
}

func (f faultExecutor) Databases(instance mysqlop.InstanceConfig, includeSystem bool) ([]string, error) {
	return nil, mysqlop.NewFaultError(f.kind, "databases 执行面故障")
}

func (f faultExecutor) Query(instance mysqlop.InstanceConfig, sql string, limit int) (mysqlop.QueryResult, error) {
	return mysqlop.QueryResult{}, mysqlop.NewFaultError(f.kind, "query 执行面故障")
}

func (f faultExecutor) Status(instance mysqlop.InstanceConfig) (mysqlop.StatusResult, error) {
	return mysqlop.StatusResult{}, mysqlop.NewFaultError(f.kind, "status 执行面故障")
}

// okExecutor 是无故障执行面，用于 sanity 反证。
type okExecutor struct{}

func (okExecutor) Instances(instances []mysqlop.InstanceConfig) ([]mysqlop.InstanceStatus, error) {
	return []mysqlop.InstanceStatus{}, nil
}

func (okExecutor) Databases(instance mysqlop.InstanceConfig, includeSystem bool) ([]string, error) {
	return []string{}, nil
}

func (okExecutor) Query(instance mysqlop.InstanceConfig, sql string, limit int) (mysqlop.QueryResult, error) {
	return mysqlop.QueryResult{Columns: []string{}, Rows: []json.RawMessage{}}, nil
}

func (okExecutor) Status(instance mysqlop.InstanceConfig) (mysqlop.StatusResult, error) {
	return mysqlop.StatusResult{Instance: instance.Name, Version: "8.0.0"}, nil
}

// methods 是四类方法及其合法参数。
var methods = []struct {
	name   string
	method string
	params string
}{
	{"instances", "mysql.instances", `{}`},
	{"databases", "mysql.databases", `{"instance":"primary"}`},
	{"query", "mysql.query", `{"instance":"primary","sql":"SELECT 1"}`},
	{"status", "mysql.status", `{"instance":"primary"}`},
}

// assertFault 对每个方法注入故障，断言返回 nil 结果 + 预期错误码与类别关键词。
func assertFault(t *testing.T, kind mysqlop.FaultKind, wantCode, wantHint string) {
	t.Helper()
	exec := faultExecutor{kind: kind}
	for _, m := range methods {
		t.Run(m.name, func(t *testing.T) {
			result, perr := mysqlop.Call(m.method, json.RawMessage(m.params), testConfig, exec)
			if perr == nil {
				t.Fatalf("%s 注入 %s 后未返回错误（结果=%s）", m.method, kind, result)
			}
			if result != nil {
				t.Fatalf("%s 出错时不应带结果: %s", m.method, result)
			}
			if perr.Code != wantCode {
				t.Fatalf("%s 错误码=%q，期望 %q（message=%q）", m.method, perr.Code, wantCode, perr.Message)
			}
			if !contains(perr.Message, wantHint) {
				t.Fatalf("%s 错误信息 %q 未含类别关键词 %q", m.method, perr.Message, wantHint)
			}
		})
	}
}

// contains 是 strings.Contains 的本地封装。
func contains(s, sub string) bool {
	if len(sub) > len(s) {
		return false
	}
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

// sanityNoFault 验证无故障时四方法正常返回，证明故障断言不是"永远失败"的假通过。
func sanityNoFault(t *testing.T) {
	t.Helper()
	for _, m := range methods {
		result, perr := mysqlop.Call(m.method, json.RawMessage(m.params), testConfig, okExecutor{})
		if perr != nil {
			t.Fatalf("无故障时 %s 意外返回错误: %v", m.method, perr)
		}
		if len(result) == 0 {
			t.Fatalf("无故障时 %s 返回空结果", m.method)
		}
	}
}

// verifyErrorContract 断言错误体是标准协议结构。
func verifyErrorContract(t *testing.T, perr *pluginapi.Error) {
	t.Helper()
	if perr == nil {
		t.Fatal("错误体为 nil")
	}
	if perr.Code == "" || perr.Message == "" {
		t.Fatalf("错误体字段不完整: %+v", perr)
	}
}
