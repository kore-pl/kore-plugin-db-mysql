package mysqlop

import (
	"encoding/json"
	"os"
	"testing"
)

// capabilityTestExpected 是本仓 method_capabilities 的表驱动真源（新增方法必须同步）。
var capabilityTestExpected = map[string]string{
	"db-mysql.config.get":     "system:read",
	"db-mysql.instances":      "system:read",
	"db-mysql.databases":      "system:read",
	"db-mysql.query":          "system:read",
	"db-mysql.status":         "system:read",
	"db-mysql.slowlog":        "system:read",
	"db-mysql.config.set":     "system:admin",
	"db-mysql.createDatabase": "system:admin",
	"db-mysql.dropDatabase":   "system:admin",
	"db-mysql.createUser":     "system:admin",
	"db-mysql.changePassword": "system:admin",
	"db-mysql.dropUser":       "system:admin",
	"db-mysql.grant":          "system:admin",
	"db-mysql.export":         "system:admin",
	"db-mysql.import":         "system:admin",
}

var capabilityTestPrivileged = map[string]bool{
	"system:read": true, "system:admin": true,
	"net:read": true, "net:write": true,
	"proc:read": true, "proc:write": true,
}

// TestMethodCapabilitiesConsistency 是方法级能力一致性门禁：
// 声明表 == info.json == manifest.json，取值合法，且每个声明的方法都能被 businessVerb 分发。
func TestMethodCapabilitiesConsistency(t *testing.T) {
	var info struct {
		ID                 string            `json:"id"`
		Version            string            `json:"version"`
		Capabilities       []string          `json:"capabilities"`
		MethodCapabilities map[string]string `json:"method_capabilities"`
	}
	readJSON(t, "../../info.json", &info)
	var manifest struct {
		ID                 string            `json:"id"`
		Version            string            `json:"version"`
		Capabilities       []string          `json:"capabilities"`
		MethodCapabilities map[string]string `json:"method_capabilities"`
	}
	readJSON(t, "../../manifest.json", &manifest)

	if info.ID != "db-mysql" || manifest.ID != info.ID || manifest.Version != info.Version {
		t.Fatalf("info/manifest 基础字段不一致: info=%+v manifest=%+v", info, manifest)
	}
	if len(info.Capabilities) == 0 || info.Capabilities[0] != "system:admin" {
		t.Fatalf("插件级 capabilities 不符: %v", info.Capabilities)
	}
	if len(info.MethodCapabilities) != len(capabilityTestExpected) {
		t.Fatalf("method_capabilities 条数=%d，期望 %d", len(info.MethodCapabilities), len(capabilityTestExpected))
	}
	for method, want := range capabilityTestExpected {
		got, ok := info.MethodCapabilities[method]
		if !ok {
			t.Fatalf("method_capabilities 缺 %q", method)
		}
		if got != want {
			t.Fatalf("method_capabilities[%q]=%q，期望 %q", method, got, want)
		}
		if !capabilityTestPrivileged[got] {
			t.Fatalf("method_capabilities[%q]=%q 不是 6 项特权能力之一", method, got)
		}
		if method == "db-mysql.config.get" || method == "db-mysql.config.set" {
			continue // 配置契约由 main 包分发，不经 mysqlop.businessVerb
		}
		if verb, known := businessVerb(method); !known || verb != method[len("db-mysql."):] {
			t.Fatalf("声明了 %q 但 businessVerb 不认识", method)
		}
	}
	for method, got := range manifest.MethodCapabilities {
		if info.MethodCapabilities[method] != got {
			t.Fatalf("manifest.method_capabilities[%q]=%q 与 info.json=%q 不一致", method, got, info.MethodCapabilities[method])
		}
	}
}

// readJSON 读取并解析仓根 JSON。
func readJSON(t *testing.T, path string, dst any) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取 %s 失败: %v", path, err)
	}
	if err := json.Unmarshal(data, dst); err != nil {
		t.Fatalf("解析 %s 失败: %v", path, err)
	}
}
