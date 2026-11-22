package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pengtang1024/kore-plugin-db-mysql/internal/mysqlop"
	"github.com/pengtang1024/kore-plugin-db-mysql/internal/plugincfg"
	"github.com/pengtang1024/kore/pkg/pluginapi"
)

// clearMySQLEnv 清掉默认实例相关环境变量，避免宿主机环境让用例不稳定。
func clearMySQLEnv(t *testing.T) {
	t.Helper()
	for _, key := range []string{EnvHost, EnvPort, EnvUser, EnvPasswordEnv, EnvIncludeSystem,
		mysqlop.EnvInstances, mysqlop.EnvConfig, mysqlop.EnvRoot} {
		t.Setenv(key, "")
	}
}

// dispatchOnce 构造请求并调用 dispatch，返回响应。
func dispatchOnce(t *testing.T, cfg *config, method, params string) *pluginapi.Response {
	t.Helper()
	request := &pluginapi.Request{
		Type: pluginapi.TypeRequest, ID: "id", ReqID: "r",
		Method: method, Params: json.RawMessage(params),
	}
	response, shutdown := dispatch(request, cfg, testLogger())
	if shutdown {
		t.Fatalf("%s 不应触发 shutdown", method)
	}
	return response
}

// TestResolveValuesPriority 覆盖 file > env > default 三种来源。
func TestResolveValuesPriority(t *testing.T) {
	clearMySQLEnv(t)
	dir := t.TempDir()
	store := plugincfg.NewStore(dir)

	values, source, err := resolveValues(store)
	if err != nil {
		t.Fatalf("默认值解析失败: %v", err)
	}
	if source != plugincfg.SourceDefault || values.Host != defaultHost || values.Port != defaultPort ||
		values.User != defaultUser || values.PasswordEnv != defaultPasswordEnv {
		t.Fatalf("默认值异常: source=%s values=%+v", source, values)
	}

	t.Setenv(EnvHost, "env-host")
	t.Setenv(EnvPort, "3307")
	t.Setenv(EnvUser, "env-user")
	t.Setenv(EnvPasswordEnv, "ENV_PASSWORD")
	t.Setenv(EnvIncludeSystem, "true")
	values, source, err = resolveValues(store)
	if err != nil {
		t.Fatalf("环境变量解析失败: %v", err)
	}
	if source != plugincfg.SourceEnv || values.Host != "env-host" || values.Port != 3307 ||
		values.User != "env-user" || values.PasswordEnv != "ENV_PASSWORD" || !values.DatabasesIncludeSystem {
		t.Fatalf("环境变量未生效: source=%s values=%+v", source, values)
	}

	if err := store.Write([]byte(`{"host":"file-host","port":3308,"user":"file-user","password_env":"FILE_PASSWORD","databases_include_system":false}`)); err != nil {
		t.Fatalf("写配置文件失败: %v", err)
	}
	values, source, err = resolveValues(store)
	if err != nil {
		t.Fatalf("配置文件解析失败: %v", err)
	}
	if source != plugincfg.SourceFile || values.Host != "file-host" || values.Port != 3308 ||
		values.User != "file-user" || values.PasswordEnv != "FILE_PASSWORD" {
		t.Fatalf("file 未覆盖 env: source=%s values=%+v", source, values)
	}
}

// TestBuildInstancesConfigWinsDefault 默认实例由 config 提供，旧路径的 default 被覆盖。
func TestBuildInstancesConfigWinsDefault(t *testing.T) {
	values := defaultValues()
	values.Host, values.Port, values.User, values.Password = "db.local", 3307, "app", "s3cr3t"
	env := []mysqlop.InstanceConfig{
		{Name: "default", Host: "env-default", Port: 3999},
		{Name: "primary", Host: "primary.local", Port: 3308},
	}
	instances := buildInstances(values, true, env)
	if len(instances) != 2 {
		t.Fatalf("实例数=%d，期望 2: %+v", len(instances), instances)
	}
	if instances[0].Name != "default" || instances[0].Host != "db.local" || instances[0].Password != "s3cr3t" {
		t.Fatalf("默认实例异常: %+v", instances[0])
	}
	if instances[1].Name != "primary" {
		t.Fatalf("旧多实例未保留: %+v", instances[1])
	}
	onlyEnv := buildInstances(values, false, env)
	if len(onlyEnv) != 2 || onlyEnv[0].Name != "default" || onlyEnv[0].Host != "env-default" {
		t.Fatalf("未声明默认实例时不应覆盖旧路径: %+v", onlyEnv)
	}
}

// TestConfigGetMasksPassword 敏感密码只回 "***"。
func TestConfigGetMasksPassword(t *testing.T) {
	cfg := config{id: "db-mysql", values: defaultValues(), source: plugincfg.SourceFile}
	cfg.values.Password = "s3cr3t"
	response := dispatchOnce(t, &cfg, "db-mysql.config.get", `{}`)
	if response.Error != nil {
		t.Fatalf("config.get 失败: %v", response.Error)
	}
	result := string(response.Result)
	if strings.Contains(result, "s3cr3t") {
		t.Fatalf("config.get 泄漏明文密码: %s", result)
	}
	for _, want := range []string{`"key":"host"`, `"key":"port"`, `"key":"user"`, `"key":"password"`,
		`"key":"password_env"`, `"key":"databases_include_system"`, `"password":"***"`, `"source":"file"`} {
		if !strings.Contains(result, want) {
			t.Fatalf("config.get 结果未含 %s: %s", want, result)
		}
	}
}

// TestConfigSetHotReload 保存后同一进程实例表立即更新、文件 0600、密码不泄漏。
func TestConfigSetHotReload(t *testing.T) {
	clearMySQLEnv(t)
	cfg := testConfig()
	cfg.store = plugincfg.NewStore(t.TempDir())
	cfg.values = defaultValues()
	cfg.source = plugincfg.SourceDefault
	cfg.mysql = mysqlop.Config{Bin: "mysql"}

	params := `{"values":{"host":"127.0.0.1","port":13306,"user":"app","password":"s3cr3t","password_env":"KORE_MYSQL_PASSWORD","databases_include_system":true}}`
	response := dispatchOnce(t, &cfg, "db-mysql.config.set", params)
	if response.Error != nil {
		t.Fatalf("config.set 失败: %v", response.Error)
	}
	if !strings.Contains(string(response.Result), `"saved":true`) ||
		!strings.Contains(string(response.Result), `"applied":true`) ||
		!strings.Contains(string(response.Result), `"restart_required":false`) {
		t.Fatalf("config.set 结果异常: %s", response.Result)
	}
	if cfg.source != plugincfg.SourceFile {
		t.Fatalf("保存后 source=%q，期望 file", cfg.source)
	}
	if len(cfg.mysql.Instances) != 1 || cfg.mysql.Instances[0].Port != 13306 ||
		cfg.mysql.Instances[0].User != "app" || cfg.mysql.Instances[0].Password != "s3cr3t" {
		t.Fatalf("内存实例表未热更新: %+v", cfg.mysql.Instances)
	}
	if !cfg.mysql.IncludeSystem {
		t.Fatal("databases_include_system 未热生效")
	}
	path := filepath.Join(cfg.store.Path())
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("配置文件未落盘: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("配置文件权限=%v，期望 0600", info.Mode().Perm())
	}

	// 再保存一次：密码传 "***" 必须保留旧明文，不能把占位写成密码。
	response = dispatchOnce(t, &cfg, "db-mysql.config.set", `{"values":{"password":"***","port":13307}}`)
	if response.Error != nil {
		t.Fatalf("第二次 config.set 失败: %v", response.Error)
	}
	if cfg.values.Password != "s3cr3t" {
		t.Fatalf("*** 覆盖了已存密码: %q", cfg.values.Password)
	}
	if len(cfg.mysql.Instances) != 1 || cfg.mysql.Instances[0].Port != 13307 ||
		cfg.mysql.Instances[0].Password != "s3cr3t" {
		t.Fatalf("第二次保存实例表异常: %+v", cfg.mysql.Instances)
	}
}

// TestConfigSetReject 非法值返回 E_INVALID 且不落盘、不改内存。
func TestConfigSetReject(t *testing.T) {
	clearMySQLEnv(t)
	cfg := testConfig()
	cfg.store = plugincfg.NewStore(t.TempDir())
	cfg.values = defaultValues()
	cfg.source = plugincfg.SourceDefault

	for _, params := range []string{
		`{"values":{"port":70000}}`,
		`{"values":{"host":"-bad"}}`,
		`{"values":{"user":"-root"}}`,
		`{"values":{"password_env":"1BAD"}}`,
		`{"values":{"unknown_field":1}}`,
		`{"values":[]}`,
	} {
		response := dispatchOnce(t, &cfg, "db-mysql.config.set", params)
		if response.Error == nil || response.Error.Code != pluginapi.E_INVALID {
			t.Fatalf("config.set(%s) 错误=%v，期望 E_INVALID", params, response.Error)
		}
	}
	if cfg.source != plugincfg.SourceDefault || cfg.values.Port != defaultPort {
		t.Fatalf("非法保存改动了内存: source=%s values=%+v", cfg.source, cfg.values)
	}
	if _, err := os.Stat(cfg.store.Path()); !os.IsNotExist(err) {
		t.Fatalf("非法保存不应落盘: %v", err)
	}
}
