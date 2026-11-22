package mysqlop

import (
	"encoding/json"

	"github.com/pengtang1024/kore/pkg/pluginapi"
)

// callStatus 处理 mysql.status：SHOW STATUS 关键指标 + SHOW VARIABLES 版本/连接上限。
func callStatus(raw json.RawMessage, cfg Config, exec Executor) (json.RawMessage, *pluginapi.Error) {
	var p statusParams
	if perr := decodeParams(raw, &p); perr != nil {
		return nil, perr
	}
	instance, perr := findInstance(cfg, p.Instance)
	if perr != nil {
		return nil, perr
	}
	result, err := exec.Status(instance)
	if err != nil {
		return nil, classify(err)
	}
	return encode(result)
}
