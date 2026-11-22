package fault

import (
	"testing"

	"github.com/pengtang1024/kore-plugin-db-mysql/internal/mysqlop"
)

// TestDiskFullFault 注入磁盘满，期望两方法返回 E_INTERNAL。
func TestDiskFullFault(t *testing.T) {
	assertFault(t, mysqlop.FaultDisk, "E_INTERNAL", "磁盘满")
}
