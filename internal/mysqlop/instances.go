package mysqlop

import (
	"encoding/json"

	"github.com/pengtang1024/kore/pkg/pluginapi"
)

// callInstances 处理 mysql.instances：校验实例名后返回实例列表占位。
func callInstances(raw json.RawMessage, root string, exec Executor) (json.RawMessage, *pluginapi.Error) {
	var p instancesParams
	if perr := decodeParams(raw, &p); perr != nil {
		return nil, perr
	}
	if perr := ValidateInstance(p.Instance); perr != nil {
		return nil, perr
	}
	items, err := exec.ListInstances(root)
	if err != nil {
		return nil, classify(err)
	}
	if items == nil {
		items = []Instance{}
	}
	return encode(InstancesResult{Root: root, Instances: items, Count: len(items)})
}

// noopExecutor 是骨架默认执行面：不真连数据库，只返回空结果。
// 四类故障注入测试提供自己的 exec，让 Call 走错误映射路径。
type noopExecutor struct{}

func (noopExecutor) ListInstances(root string) ([]Instance, error) { return nil, nil }
func (noopExecutor) SlowQueries(root string, f Filter) ([]SlowQuery, error) {
	return nil, nil
}
