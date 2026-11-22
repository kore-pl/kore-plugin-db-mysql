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
	// EnvRoot 是插件数据根目录，供缺省 instances.json 使用。
	EnvRoot = "KORE_MYSQL_ROOT"

	defaultBin     = "mysql"
	defaultTimeout = 15 * time.Second
	maxTimeout     = 2 * time.Minute
	maxInstances   = 64
)

// LoadConfig 读取运行期配置。配置缺失返回空实例列表；配置非法返回错误，
// 由调用方转成 E_INVALID，而不是让插件进程启动失败。
func LoadConfig() (Config, error) {
	bin := strings.TrimSpace(os.Getenv(EnvBin))
	if bin == "" {
		bin = defaultBin
	}
	timeout := defaultTimeout
	if raw := strings.TrimSpace(os.Getenv(EnvTimeout)); raw != "" {
		parsed, err := time.ParseDuration(raw)
		if err != nil || parsed <= 0 || parsed > maxTimeout {
			return Config{}, fmt.Errorf("%s=%q 非法：须为 (0, %s] 的 Go 时长（如 15s）", EnvTimeout, raw, maxTimeout)
		}
		timeout = parsed
	}

	raw, source, err := instancesSource()
	if err != nil {
		return Config{}, err
	}
	instances, err := ParseInstances(raw)
	if err != nil {
		return Config{}, fmt.Errorf("解析实例配置（%s）: %w", source, err)
	}
	return Config{Instances: instances, Bin: bin, Timeout: timeout}, nil
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

// instancePassword 从实例声明的环境变量读取密码；未声明 password_env 时返回空串。
// 返回的错误只包含变量名，不包含变量值。
func instancePassword(ic InstanceConfig) (string, error) {
	if ic.PasswordEnv == "" {
		return "", nil
	}
	password, ok := os.LookupEnv(ic.PasswordEnv)
	if !ok || password == "" {
		return "", fmt.Errorf("实例 %q 的密码环境变量 %s 未设置或为空", ic.Name, ic.PasswordEnv)
	}
	return password, nil
}
