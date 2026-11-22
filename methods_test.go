package main

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/pengtang1024/kore/pkg/pluginapi"
)

// TestDispatchMethods 只读方法经 dispatch 返回结构化结果（无实例配置时 instances 为空列表）。
func TestDispatchMethods(t *testing.T) {
	req := &pluginapi.Request{
		Type: pluginapi.TypeRequest, ID: "id", ReqID: "r",
		Method: "mysql.instances", Params: json.RawMessage(`{}`),
	}
	resp, shutdown := dispatch(req, testConfig(), testLogger())
	if shutdown {
		t.Fatal("mysql.instances 不应触发 shutdown")
	}
	if resp.Error != nil {
		t.Fatalf("mysql.instances 意外错误: %v", resp.Error)
	}
	if !strings.Contains(string(resp.Result), `"count":0`) {
		t.Fatalf("mysql.instances 结果=%s", resp.Result)
	}
}

// TestDispatchReject 非法实例名、写 SQL、超限 limit 与未知方法经 dispatch 返回协议错误。
func TestDispatchReject(t *testing.T) {
	cases := []struct {
		method string
		params string
		code   string
	}{
		{"mysql.instances", `{"instance":"../etc"}`, pluginapi.E_INVALID},
		{"mysql.databases", `{}`, pluginapi.E_INVALID},
		{"mysql.query", `{"instance":"primary","sql":"INSERT INTO t VALUES (1)"}`, pluginapi.E_INVALID},
		{"mysql.query", `{"instance":"primary","sql":"SELECT 1","limit":5000}`, pluginapi.E_INVALID},
		{"mysql.status", `{"instance":"missing"}`, pluginapi.E_NOTFOUND},
		{"mysql.exec", `{}`, pluginapi.E_NOTFOUND},
		{"mysql.slowQueries", `{"instance":"primary"}`, pluginapi.E_NOTFOUND},
	}
	for _, c := range cases {
		req := &pluginapi.Request{
			Type: pluginapi.TypeRequest, ID: "id", ReqID: "r",
			Method: c.method, Params: json.RawMessage(c.params),
		}
		resp, _ := dispatch(req, testConfig(), testLogger())
		if resp.Error == nil || resp.Error.Code != c.code {
			t.Fatalf("%s %s 错误=%v，期望 %q", c.method, c.params, resp.Error, c.code)
		}
		if resp.Result != nil {
			t.Fatalf("%s 出错时不应带结果", c.method)
		}
	}
}

// TestDispatchConfigInvalid 实例配置非法时 MySQL 方法返回 E_INVALID，ping 仍可用。
func TestDispatchConfigInvalid(t *testing.T) {
	cfg := testConfig()
	cfg.mysql.Err = &configError{msg: "实例配置非法"}
	req := &pluginapi.Request{
		Type: pluginapi.TypeRequest, ID: "id", ReqID: "r",
		Method: "mysql.instances", Params: json.RawMessage(`{}`),
	}
	resp, _ := dispatch(req, cfg, testLogger())
	if resp.Error == nil || resp.Error.Code != pluginapi.E_INVALID {
		t.Fatalf("错误=%v，期望 E_INVALID", resp.Error)
	}
}

// configError 是测试用的配置错误。
type configError struct{ msg string }

func (e *configError) Error() string { return e.msg }

// TestPingResult kore.ping 返回 ready 状态。
func TestPingResult(t *testing.T) {
	req := &pluginapi.Request{Type: pluginapi.TypeRequest, ID: "id", ReqID: "r", Method: "kore.ping"}
	resp, _ := dispatch(req, testConfig(), testLogger())
	if !strings.Contains(string(resp.Result), `"status":"ready"`) {
		t.Fatalf("ping 结果=%s", resp.Result)
	}
}
