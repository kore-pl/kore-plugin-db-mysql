package boundary

import (
	"encoding/json"
	"testing"

	"github.com/pengtang1024/kore-plugin-db-mysql/internal/mysqlop"
	"github.com/pengtang1024/kore/pkg/pluginapi"
)

// TestResourceExhaustionQueryLineOverLimit 资源上限防护（单行 1 MiB）：
// 假客户端通过真实管道输出一条 2 MiB 的单行结果，插件必须终止客户端并返回
// 结构化 E_INTERNAL，而不是把内存打爆或静默截断。
func TestResourceExhaustionQueryLineOverLimit(t *testing.T) {
	cfg := boundaryConfig(writeScript(t, t.TempDir(), "mysql", hugeLineScript), "", "")

	_, perr := mysqlop.Call("db-mysql.query",
		json.RawMessage(`{"instance":"primary","sql":"SELECT 1"}`), cfg, nil)
	requireCode(t, perr, pluginapi.E_INTERNAL, "1 MiB")
}
