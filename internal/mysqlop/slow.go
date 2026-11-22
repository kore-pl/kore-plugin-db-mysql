package mysqlop

import (
	"encoding/json"

	"github.com/pengtang1024/kore/pkg/pluginapi"
)

// defaultThresholdMS 是 mysql.slowQueries 缺省的慢查询阈值（1 秒）。
const defaultThresholdMS = 1000

// defaultLimit 是 mysql.slowQueries 缺省的返回条数。
const defaultLimit = 20

// callSlowQueries 处理 mysql.slowQueries：校验查询参数后返回慢查询占位列表。
func callSlowQueries(raw json.RawMessage, root string, exec Executor) (json.RawMessage, *pluginapi.Error) {
	var f Filter
	if perr := decodeParams(raw, &f); perr != nil {
		return nil, perr
	}
	if perr := ValidateFilter(f); perr != nil {
		return nil, perr
	}
	applyDefaults(&f)
	items, err := exec.SlowQueries(root, f)
	if err != nil {
		return nil, classify(err)
	}
	if items == nil {
		items = []SlowQuery{}
	}
	return encode(SlowQueriesResult{
		Instance:    f.Instance,
		ThresholdMS: f.ThresholdMS,
		Limit:       f.Limit,
		Window:      f.Window,
		Queries:     items,
		Count:       len(items),
	})
}

// applyDefaults 把零值过滤参数填为缺省值，保证结构化结果稳定。
func applyDefaults(f *Filter) {
	if f.ThresholdMS == 0 {
		f.ThresholdMS = defaultThresholdMS
	}
	if f.Limit == 0 {
		f.Limit = defaultLimit
	}
	if f.Window == "" {
		f.Window = "1h"
	}
}
