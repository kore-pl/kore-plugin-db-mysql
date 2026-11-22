package mysqlop

import (
	"strings"
	"testing"

	"github.com/pengtang1024/kore/pkg/pluginapi"
)

// TestValidateInstance 合法/非法实例名边界。
func TestValidateInstance(t *testing.T) {
	valid := []string{"", "primary", "db-01", "replica.prod", "a"}
	for _, name := range valid {
		if perr := ValidateInstance(name); perr != nil {
			t.Fatalf("ValidateInstance(%q) 意外拒绝: %v", name, perr)
		}
	}
	invalidNames := []string{"Upper", "-lead", "../etc", "a/b", "a\\b", "a..b", strings.Repeat("x", 65), "has space"}
	for _, name := range invalidNames {
		if perr := ValidateInstance(name); perr == nil || perr.Code != pluginapi.E_INVALID {
			t.Fatalf("ValidateInstance(%q) 未拒绝", name)
		}
	}
}

// TestValidateFilter 慢查询过滤参数边界。
func TestValidateFilter(t *testing.T) {
	ok := Filter{Instance: "primary", ThresholdMS: 500, Limit: 10, Window: "5m"}
	if perr := ValidateFilter(ok); perr != nil {
		t.Fatalf("合法 Filter 被拒绝: %v", perr)
	}
	bad := []Filter{
		{Instance: "primary", ThresholdMS: -1},
		{Instance: "primary", ThresholdMS: maxThresholdMS + 1},
		{Instance: "primary", Limit: -1},
		{Instance: "primary", Limit: maxLimit + 1},
		{Instance: "primary", Window: "10x"},
	}
	for i, f := range bad {
		if perr := ValidateFilter(f); perr == nil || perr.Code != pluginapi.E_INVALID {
			t.Fatalf("bad Filter[%d]=%+v 未拒绝", i, f)
		}
	}
}
