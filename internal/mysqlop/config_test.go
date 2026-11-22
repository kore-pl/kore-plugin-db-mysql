package mysqlop

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestParseInstancesDefaults 合法数组 + 缺省 port/user 补齐。
func TestParseInstancesDefaults(t *testing.T) {
	list, err := ParseInstances([]byte(`[
		{"name":"primary","host":"127.0.0.1"},
		{"name":"replica","host":"db.internal","port":3307,"user":"reader","password_env":"REPLICA_PASSWORD"}
	]`))
	if err != nil {
		t.Fatalf("ParseInstances 失败: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("实例数=%d", len(list))
	}
	if list[0].Port != 3306 || list[0].User != "root" || list[0].PasswordEnv != "" {
		t.Fatalf("缺省值未补齐: %+v", list[0])
	}
	if list[1].Port != 3307 || list[1].User != "reader" || list[1].PasswordEnv != "REPLICA_PASSWORD" {
		t.Fatalf("显式值被改写: %+v", list[1])
	}
}

// TestParseInstancesEmpty 空数组/空输入返回空列表。
func TestParseInstancesEmpty(t *testing.T) {
	for _, raw := range []string{`[]`, ``, `   `} {
		list, err := ParseInstances([]byte(raw))
		if err != nil || len(list) != 0 {
			t.Fatalf("ParseInstances(%q)=(%v,%v)，期望空列表", raw, list, err)
		}
	}
}

// TestParseInstancesRejects 非法配置逐项拒绝。
func TestParseInstancesRejects(t *testing.T) {
	cases := map[string]string{
		"非法 JSON":         `[{`,
		"非数组":             `{"name":"primary"}`,
		"重复实例名":           `[{"name":"a","host":"h"},{"name":"a","host":"h"}]`,
		"空实例名":            `[{"name":"","host":"h"}]`,
		"实例名大写":           `[{"name":"Primary","host":"h"}]`,
		"空 host":          `[{"name":"a","host":""}]`,
		"host 选项注入":       `[{"name":"a","host":"-h"}]`,
		"host 含空格":        `[{"name":"a","host":"a b"}]`,
		"host 含分号":        `[{"name":"a","host":"a;b"}]`,
		"user 选项注入":       `[{"name":"a","host":"h","user":"-root"}]`,
		"user 含空格":        `[{"name":"a","host":"h","user":"a b"}]`,
		"port 越界":         `[{"name":"a","host":"h","port":70000}]`,
		"port 负数":         `[{"name":"a","host":"h","port":-1}]`,
		"password_env 非法": `[{"name":"a","host":"h","password_env":"1BAD"}]`,
		"password_env 横线": `[{"name":"a","host":"h","password_env":"A-B"}]`,
		"未知字段 password":   `[{"name":"a","host":"h","password":"secret"}]`,
		"JSON 尾部多余":       `[] []`,
	}
	for name, raw := range cases {
		if _, err := ParseInstances([]byte(raw)); err == nil {
			t.Fatalf("%s 未拒绝: %s", name, raw)
		}
	}
}

// TestParseInstancesLimit 实例数量上限。
func TestParseInstancesLimit(t *testing.T) {
	items := make([]string, 0, maxInstances+1)
	for i := 0; i <= maxInstances; i++ {
		items = append(items, fmt.Sprintf(`{"name":"n%02d","host":"h"}`, i))
	}
	if _, err := ParseInstances([]byte("[" + strings.Join(items, ",") + "]")); err == nil {
		t.Fatal("超量实例未拒绝")
	}
}

// TestLoadConfigEnv 环境变量优先于配置文件。
func TestLoadConfigEnv(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "instances.json")
	if err := os.WriteFile(file, []byte(`[{"name":"from-file","host":"file-host"}]`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(EnvRoot, dir)
	t.Setenv(EnvConfig, file)
	t.Setenv(EnvInstances, `[{"name":"from-env","host":"env-host"}]`)
	t.Setenv(EnvTimeout, "3s")
	t.Setenv(EnvBin, "/usr/bin/mysql")
	cfg, err := LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig 失败: %v", err)
	}
	if len(cfg.Instances) != 1 || cfg.Instances[0].Name != "from-env" {
		t.Fatalf("未使用环境变量配置: %+v", cfg.Instances)
	}
	if cfg.Timeout.String() != "3s" || cfg.Bin != "/usr/bin/mysql" {
		t.Fatalf("timeout/bin 未生效: %s/%s", cfg.Timeout, cfg.Bin)
	}
}

// TestLoadConfigFile 无环境变量时读 KORE_MYSQL_CONFIG 文件。
func TestLoadConfigFile(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "instances.json")
	if err := os.WriteFile(file, []byte(`[{"name":"from-file","host":"file-host","port":3307}]`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(EnvRoot, dir)
	t.Setenv(EnvConfig, file)
	t.Setenv(EnvInstances, "")
	cfg, err := LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig 失败: %v", err)
	}
	if len(cfg.Instances) != 1 || cfg.Instances[0].Port != 3307 {
		t.Fatalf("未读取配置文件: %+v", cfg.Instances)
	}
}

// TestLoadConfigMissingFile 三处都没有配置时返回空列表，不报错。
func TestLoadConfigMissingFile(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(EnvRoot, dir)
	t.Setenv(EnvConfig, "")
	t.Setenv(EnvInstances, "")
	t.Setenv(EnvTimeout, "")
	cfg, err := LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig 失败: %v", err)
	}
	if len(cfg.Instances) != 0 {
		t.Fatalf("期望空实例列表: %+v", cfg.Instances)
	}
	if cfg.Bin != defaultBin || cfg.Timeout != defaultTimeout {
		t.Fatalf("缺省 bin/timeout 错: %+v", cfg)
	}
}

// TestLoadConfigBadTimeout 非法超时拒绝。
func TestLoadConfigBadTimeout(t *testing.T) {
	t.Setenv(EnvRoot, t.TempDir())
	t.Setenv(EnvInstances, `[]`)
	for _, raw := range []string{"abc", "0s", "-1s", "10m"} {
		t.Setenv(EnvTimeout, raw)
		if _, err := LoadConfig(); err == nil {
			t.Fatalf("非法超时 %q 未拒绝", raw)
		}
	}
}
