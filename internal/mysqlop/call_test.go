package mysqlop

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/pengtang1024/kore/pkg/pluginapi"
)

// TestBusinessVerb 方法名归一化：mysql.<verb> 与裸 <verb> 都接受。
func TestBusinessVerb(t *testing.T) {
	cases := map[string]string{
		"mysql.instances": "instances", "mysql.slowQueries": "slowQueries",
		"instances": "instances", "slowQueries": "slowQueries",
	}
	for method, want := range cases {
		got, ok := businessVerb(method)
		if !ok || got != want {
			t.Fatalf("businessVerb(%q)=(%q,%v)，期望 (%q,true)", method, got, ok, want)
		}
	}
	if _, ok := businessVerb("mysql.exec"); ok {
		t.Fatal("mysql.exec 不应被接受")
	}
}

// TestCallResults 两方法正常路径返回结构化结果（骨架口径，不真连数据库）。
func TestCallResults(t *testing.T) {
	list, perr := Call("mysql.instances", json.RawMessage(`{"instance":"primary"}`), root, nil)
	if perr != nil || !strings.Contains(string(list), `"count":0`) {
		t.Fatalf("mysql.instances 结果=%s err=%v", list, perr)
	}
	slow, perr := Call("mysql.slowQueries", json.RawMessage(`{"instance":"primary"}`), root, nil)
	if perr != nil || !strings.Contains(string(slow), `"threshold_ms":1000`) {
		t.Fatalf("mysql.slowQueries 结果=%s err=%v", slow, perr)
	}
	if !strings.Contains(string(slow), `"window":"1h"`) || !strings.Contains(string(slow), `"limit":20`) {
		t.Fatalf("mysql.slowQueries 缺省值未生效: %s", slow)
	}
}

// TestCallParamValidation 非法参数形状、实例名非法、阈值越界均返回 E_INVALID。
func TestCallParamValidation(t *testing.T) {
	bad := []struct {
		method string
		params string
	}{
		{"mysql.instances", `[]`},
		{"mysql.slowQueries", `not-json`},
		{"mysql.instances", `{"instance":"../etc"}`},
		{"mysql.slowQueries", `{"instance":"primary","threshold_ms":99999999}`},
		{"mysql.slowQueries", `{"instance":"primary","limit":5000}`},
		{"mysql.slowQueries", `{"instance":"primary","window":"garbage"}`},
	}
	for _, c := range bad {
		_, perr := Call(c.method, json.RawMessage(c.params), root, nil)
		if perr == nil || perr.Code != pluginapi.E_INVALID {
			t.Fatalf("Call(%s,%s) 错误=%v，期望 E_INVALID", c.method, c.params, perr)
		}
	}
}

// TestCallUnknownMethod 未知方法返回 E_NOTFOUND。
func TestCallUnknownMethod(t *testing.T) {
	_, perr := Call("mysql.exec", json.RawMessage(`{}`), root, nil)
	if perr == nil || perr.Code != pluginapi.E_NOTFOUND {
		t.Fatalf("未知方法错误=%v，期望 E_NOTFOUND", perr)
	}
}

const root = "/var/lib/kore/db-mysql"
