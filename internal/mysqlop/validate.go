package mysqlop

import (
	"regexp"
	"strings"
	"time"

	"github.com/pengtang1024/kore/pkg/pluginapi"
)

// maxLimit 是 mysql.slowQueries 单次返回的最大条数。
const maxLimit = 1000

// maxThresholdMS 是慢查询阈值上限（24 小时，毫秒）。
const maxThresholdMS = 24 * 60 * 60 * 1000

// instanceNameRE 是实例名的合法形状：小写字母/数字开头，仅含小写字母、数字、连字符与点。
var instanceNameRE = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]{0,63}$`)

// ValidateInstance 校验实例名：空串由调用方按语义决定是否允许（列出全部）。
// 非空时拒绝非法字符、超长、路径穿越样式与 ".." 段；只做字符串校验，不访问系统。
func ValidateInstance(name string) *pluginapi.Error {
	if name == "" {
		return nil
	}
	if strings.ContainsRune(name, 0) {
		return invalid("实例名含 NUL 字节")
	}
	if strings.ContainsAny(name, "/\\") {
		return invalid("实例名不得含路径分隔符")
	}
	if !instanceNameRE.MatchString(name) {
		return invalid("实例名非法：须以小写字母/数字开头，仅含小写字母、数字、. 与 -")
	}
	if strings.Contains(name, "..") {
		return invalid("实例名含 .. 段，禁止穿越")
	}
	return nil
}

// ValidateFilter 校验慢查询过滤参数的边界。
func ValidateFilter(f Filter) *pluginapi.Error {
	if perr := ValidateInstance(f.Instance); perr != nil {
		return perr
	}
	if f.ThresholdMS < 0 || f.ThresholdMS > maxThresholdMS {
		return invalid("threshold_ms 超出 [0, 86400000] 范围")
	}
	if f.Limit < 0 || f.Limit > maxLimit {
		return invalid("limit 超出 [0, 1000] 范围")
	}
	if f.Window != "" {
		if _, err := time.ParseDuration(f.Window); err != nil {
			return invalid("window 不是合法时长（如 5m / 1h）")
		}
	}
	return nil
}
