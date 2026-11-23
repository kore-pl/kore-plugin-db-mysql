package mysqlop

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// 实例配置来源（按优先级）：KORE_MYSQL_INSTANCES 环境变量 → KORE_MYSQL_CONFIG 文件
// → KORE_MYSQL_ROOT/instances.json。三处都没有时视为「未配置实例」，不报错。
const (
	// EnvInstances 直接携带 JSON 数组配置。
	EnvInstances = "KORE_MYSQL_INSTANCES"
	// EnvConfig 指向 JSON 配置文件路径。
	EnvConfig = "KORE_MYSQL_CONFIG"
	// EnvBin 覆盖 mysql 客户端路径（缺省 PATH 中的 mysql）。
	EnvBin = "KORE_MYSQL_BIN"
	// EnvTimeout 覆盖命令超时（Go 时长，如 15s）。
	EnvTimeout = "KORE_MYSQL_TIMEOUT"
	// EnvDumpBin 覆盖 mysqldump 客户端路径（缺省 PATH 中的 mysqldump）。
	EnvDumpBin = "KORE_MYSQLDUMP_BIN"
	// EnvExportDir 覆盖 import/export 的文件白名单根目录（必须是绝对路径）。
	EnvExportDir = "KORE_DB_EXPORT_DIR"
	// EnvRoot 是插件数据根目录，供缺省 instances.json 使用。
	EnvRoot = "KORE_MYSQL_ROOT"

	defaultBin = "mysql"
	// defaultDumpBin 是导出工具缺省值；与 mysql 客户端一样由宿主机提供。
	defaultDumpBin = "mysqldump"
	// DefaultExportDirName 是未配置 KORE_DB_EXPORT_DIR 时相对插件状态目录的子目录名。
	DefaultExportDirName = "exports"

	defaultTimeout = 15 * time.Second
	maxTimeout     = 2 * time.Minute
	maxInstances   = 64
)

// LoadConfig 读取旧多实例路径与客户端参数。配置缺失返回空实例列表；
// 配置非法返回错误，由调用方转成 E_INVALID，而不是让插件进程启动失败。
func LoadConfig() (Config, error) {
	bin, timeout, err := LoadClient()
	if err != nil {
		return Config{}, err
	}
	instances, err := LoadEnvInstances()
	if err != nil {
		return Config{}, err
	}
	return Config{Instances: instances, Bin: bin, Timeout: timeout}, nil
}

// LoadClient 读取 mysql 客户端路径与命令超时（KORE_MYSQL_BIN / KORE_MYSQL_TIMEOUT）。
func LoadClient() (string, time.Duration, error) {
	bin := strings.TrimSpace(os.Getenv(EnvBin))
	if bin == "" {
		bin = defaultBin
	}
	timeout := defaultTimeout
	if raw := strings.TrimSpace(os.Getenv(EnvTimeout)); raw != "" {
		parsed, err := time.ParseDuration(raw)
		if err != nil || parsed <= 0 || parsed > maxTimeout {
			return "", 0, fmt.Errorf("%s=%q 非法：须为 (0, %s] 的 Go 时长（如 15s）", EnvTimeout, raw, maxTimeout)
		}
		timeout = parsed
	}
	return bin, timeout, nil
}

// LoadDumpBin 读取 mysqldump 客户端路径（KORE_MYSQLDUMP_BIN，缺省 PATH 中的 mysqldump）。
func LoadDumpBin() string {
	if bin := strings.TrimSpace(os.Getenv(EnvDumpBin)); bin != "" {
		return bin
	}
	return defaultDumpBin
}

// ResolveExportDir 解析 import/export 白名单根：
// KORE_DB_EXPORT_DIR 优先；否则 <KORE_STATE_DIR>/exports；两者都没有时返回空串，
// 由方法层给出可操作的 E_INVALID（而不是写相对路径）。
func ResolveExportDir(stateDir string) string {
	if dir := strings.TrimSpace(os.Getenv(EnvExportDir)); dir != "" {
		return dir
	}
	if dir := strings.TrimSpace(stateDir); dir != "" {
		return filepath.Join(dir, DefaultExportDirName)
	}
	return ""
}

// LoadEnvInstances 读取 KORE_MYSQL_INSTANCES / KORE_MYSQL_CONFIG / <root>/instances.json
// 这条旧多实例路径；三处都没有时返回空列表。
func LoadEnvInstances() ([]InstanceConfig, error) {
	raw, source, err := instancesSource()
	if err != nil {
		return nil, err
	}
	instances, err := ParseInstances(raw)
	if err != nil {
		return nil, fmt.Errorf("解析实例配置（%s）: %w", source, err)
	}
	return instances, nil
}

// instancesSource 按优先级取出配置原文；三处都没有时返回空数组。
func instancesSource() ([]byte, string, error) {
	if raw := strings.TrimSpace(os.Getenv(EnvInstances)); raw != "" {
		return []byte(raw), EnvInstances, nil
	}
	if path := strings.TrimSpace(os.Getenv(EnvConfig)); path != "" {
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, EnvConfig, fmt.Errorf("读取 %s 失败: %w", EnvConfig, err)
		}
		return data, EnvConfig + "=" + path, nil
	}
	root := strings.TrimSpace(os.Getenv(EnvRoot))
	if root == "" {
		root = "/var/lib/kore/db-mysql"
	}
	path := filepath.Join(root, "instances.json")
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return []byte("[]"), "缺省配置（未配置实例）", nil
	}
	if err != nil {
		return nil, path, fmt.Errorf("读取实例配置文件失败: %w", err)
	}
	return data, path, nil
}

// ParseInstances 解析并校验实例配置 JSON 数组，补齐缺省值。
//
// 严格模式：未知字段直接报错，避免把 password 之类的明文误写进配置后被静默忽略。
// 校验后仍然只有 password_env（环境变量名），不保存任何密码值。
func ParseInstances(raw []byte) ([]InstanceConfig, error) {
	list := []InstanceConfig{}
	if len(bytes.TrimSpace(raw)) == 0 {
		return list, nil
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&list); err != nil {
		return nil, fmt.Errorf("实例配置必须是 JSON 数组: %w", err)
	}
	if err := ensureEOF(decoder); err != nil {
		return nil, err
	}
	if len(list) > maxInstances {
		return nil, fmt.Errorf("实例数量 %d 超过上限 %d", len(list), maxInstances)
	}
	seen := make(map[string]struct{}, len(list))
	for i := range list {
		if err := normalizeInstance(&list[i]); err != nil {
			return nil, fmt.Errorf("第 %d 条实例: %w", i+1, err)
		}
		if _, dup := seen[list[i].Name]; dup {
			return nil, fmt.Errorf("实例名 %q 重复", list[i].Name)
		}
		seen[list[i].Name] = struct{}{}
	}
	return list, nil
}

// ensureEOF 确认 JSON 数组后没有多余内容。
func ensureEOF(decoder *json.Decoder) error {
	var extra json.RawMessage
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return errors.New("实例配置 JSON 后存在多余内容")
		}
		return fmt.Errorf("实例配置 JSON 尾部非法: %w", err)
	}
	return nil
}

// normalizeInstance 校验单条实例配置并补齐缺省值（port=3306、user=root）。
func normalizeInstance(ic *InstanceConfig) error {
	if err := validateInstanceName(ic.Name); err != nil {
		return err
	}
	ic.Name = strings.TrimSpace(ic.Name)
	ic.Host = strings.TrimSpace(ic.Host)
	ic.User = strings.TrimSpace(ic.User)
	if ic.Port == 0 {
		ic.Port = 3306
	}
	if ic.User == "" {
		ic.User = "root"
	}
	if err := validateHost(ic.Host); err != nil {
		return err
	}
	if ic.Port < 1 || ic.Port > 65535 {
		return fmt.Errorf("实例 %q 的 port=%d 超出 [1, 65535]", ic.Name, ic.Port)
	}
	if err := validateUser(ic.User); err != nil {
		return fmt.Errorf("实例 %q 的 user 非法: %w", ic.Name, err)
	}
	if ic.PasswordEnv != "" {
		ic.PasswordEnv = strings.TrimSpace(ic.PasswordEnv)
		if !passwordEnvRE.MatchString(ic.PasswordEnv) {
			return fmt.Errorf("实例 %q 的 password_env=%q 非法：须为环境变量名（字母/下划线开头，仅含字母、数字、下划线）", ic.Name, ic.PasswordEnv)
		}
	}
	return nil
}

// instancePassword 优先使用配置契约直接保存的密码（仅默认实例），否则从实例声明的
// 环境变量读取；两者都没有时返回空串。返回的错误只包含变量名，不包含变量值。
const (
	// DefaultInstanceName 是配置契约描述的默认实例名（内核凭据注入按默认实例生效）。
	DefaultInstanceName = "default"
	// KernelPasswordEnv 是内核按 ADR-0026 注入的凭据环境变量；存在即优先于插件 config 口令。
	KernelPasswordEnv = "MYSQL_PWD"
	// CredentialSourceKernelEnv / PluginConfig / None 是对外诊断字段 credential_source 的三态。
	CredentialSourceKernelEnv    = "kernel_env"
	CredentialSourcePluginConfig = "plugin_config"
	CredentialSourceNone         = "none"
)

// readConfigPassword 是读取插件配置口令的唯一入口；单测可替换并计数，
// 用来断言「内核注入 env 存在时零次读取 config 口令」。
var readConfigPassword = func(ic InstanceConfig) string { return ic.Password }

// resolveCredential 返回连接口令与来源（ADR-0026 / §3.5 #8）：
//   - kernel_env：默认实例 + 内核注入的 MYSQL_PWD 存在 → 直接用，**不再读 config 口令**；
//   - plugin_config：env 缺失时回落插件 config（password 或 password_env），向后兼容；
//   - none：两者都没有（MySQL 无匿名认证，后续连接会得到服务端认证失败）。
//
// 内核注入只作用于默认实例：多实例的其它实例各有自己的 config 口令，避免把默认实例的
// 凭据误发给别的数据库。
func resolveCredential(ic InstanceConfig) (string, string) {
	if ic.Name == DefaultInstanceName {
		if password, ok := os.LookupEnv(KernelPasswordEnv); ok && password != "" {
			return password, CredentialSourceKernelEnv
		}
	}
	if password := readConfigPassword(ic); password != "" {
		return password, CredentialSourcePluginConfig
	}
	if ic.PasswordEnv != "" && ic.PasswordEnv != KernelPasswordEnv {
		if password, ok := os.LookupEnv(ic.PasswordEnv); ok && password != "" {
			return password, CredentialSourcePluginConfig
		}
	}
	return "", CredentialSourceNone
}

// instancePassword 沿用旧签名：返回口令，并在「实例显式声明了 password_env 却未设置」
// 时返回可操作错误；内核注入命中时不报错、也不读 config。
func instancePassword(ic InstanceConfig) (string, error) {
	password, source := resolveCredential(ic)
	if source == CredentialSourceNone && ic.PasswordEnv != "" && ic.PasswordEnv != KernelPasswordEnv {
		return "", fmt.Errorf("实例 %q 的密码环境变量 %s 未设置或为空", ic.Name, ic.PasswordEnv)
	}
	return password, nil
}
