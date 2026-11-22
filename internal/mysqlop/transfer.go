package mysqlop

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/pengtang1024/kore/pkg/pluginapi"
)

// maxTransferBytes 是导入/导出单文件上限：256 MiB。超过即拒绝，避免把插件进程
// 与磁盘拖进不可控状态（导出超限会删除半成品文件）。
const maxTransferBytes int64 = 256 << 20

// callExport 处理 db-mysql.export：校验库名/表名/路径 → mysqldump 固定 argv → 大小上限。
func callExport(raw json.RawMessage, cfg Config, exec Executor) (json.RawMessage, *pluginapi.Error) {
	if perr := requireAdmin(cfg); perr != nil {
		return nil, perr
	}
	var p exportParams
	if perr := decodeParams(raw, &p); perr != nil {
		return nil, perr
	}
	if p.Database == "" {
		return nil, invalid("缺少必填参数 database")
	}
	if err := ValidateDatabaseName(p.Database); err != nil {
		return nil, invalid(err.Error())
	}
	tables, err := ValidateTableList(p.Tables)
	if err != nil {
		return nil, invalid(err.Error())
	}
	root, perr := exportRoot(cfg)
	if perr != nil {
		return nil, perr
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return nil, internalErr("创建导出目录失败: " + err.Error())
	}
	target, perr := ResolveTarget(root, p.FilePath)
	if perr != nil {
		return nil, perr
	}
	instance, perr := resolveInstance(cfg, p.Instance)
	if perr != nil {
		return nil, perr
	}
	size, err := exec.Export(instance, p.Database, tables, target)
	if err != nil {
		// 失败不留半份文件：mysqldump 可能已写了截断内容。
		_ = os.Remove(target)
		return nil, classify(err)
	}
	if size > maxTransferBytes {
		_ = os.Remove(target)
		return nil, invalid(fmt.Sprintf("导出文件 %d 字节超过 256 MiB 上限，已删除", size))
	}
	return encode(ExportResult{Database: p.Database, FilePath: target, SizeBytes: size, Tables: tables})
}

// callImport 处理 db-mysql.import：路径必须在导出目录内、普通文件、非空且 ≤256 MiB；
// 文件内容通过 stdin 交给 mysql 客户端（不经过 shell，也不拼命令行字符串）。
func callImport(raw json.RawMessage, cfg Config, exec Executor) (json.RawMessage, *pluginapi.Error) {
	if perr := requireAdmin(cfg); perr != nil {
		return nil, perr
	}
	var p importParams
	if perr := decodeParams(raw, &p); perr != nil {
		return nil, perr
	}
	if p.Database == "" {
		return nil, invalid("缺少必填参数 database")
	}
	if err := ValidateDatabaseName(p.Database); err != nil {
		return nil, invalid(err.Error())
	}
	root, perr := exportRoot(cfg)
	if perr != nil {
		return nil, perr
	}
	source, perr := ResolveExisting(root, p.FilePath)
	if perr != nil {
		return nil, perr
	}
	info, err := os.Stat(source)
	if err != nil {
		return nil, internalErr("读取导入文件信息失败: " + err.Error())
	}
	if info.Size() == 0 {
		return nil, invalid("导入文件为空: " + p.FilePath)
	}
	if info.Size() > maxTransferBytes {
		return nil, invalid(fmt.Sprintf("导入文件 %d 字节超过 256 MiB 上限", info.Size()))
	}
	instance, perr := resolveInstance(cfg, p.Instance)
	if perr != nil {
		return nil, perr
	}
	if err := exec.Import(instance, p.Database, source); err != nil {
		return nil, classify(err)
	}
	return encode(ImportResult{Database: p.Database, FilePath: source, SizeBytes: info.Size()})
}

// exportRoot 返回配置的导出目录；未配置时给出可操作的提示。
func exportRoot(cfg Config) (string, *pluginapi.Error) {
	root := strings.TrimSpace(cfg.ExportDir)
	if root == "" {
		return "", invalid("未配置导出目录：请设置 KORE_DB_EXPORT_DIR，或配置 KORE_STATE_DIR（默认 <state>/exports）")
	}
	return root, nil
}
