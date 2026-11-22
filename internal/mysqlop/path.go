package mysqlop

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/pengtang1024/kore/pkg/pluginapi"
)

// 导出目录白名单：file_path 只允许落在 KORE_DB_EXPORT_DIR（默认 <state>/exports）内。
//
// 与 kore-plugin-file 的 ResolveExisting / ResolveTarget 同源语义：
//  1. 词法层先做（NUL / 反斜杠 / .. 段 / 绝对路径归属 / Clean+Rel 越界）；
//  2. 再跟随符号链接解析并与真实根目录做归属比对，挡住「父目录被换成软链」的逃逸。
//
// 与 file 插件不同的一点：这里**接受绝对路径**，但它必须位于导出根目录内——导出
// 路径通常由前端拼好绝对路径回填，拒绝绝对路径会让「导出后再导入」多一次转换。
// 无论相对还是绝对，最终归属判定都只有一条：必须在真实导出根目录之下。

// ValidateExportPath 做纯词法校验，返回归一化后的绝对路径（不访问文件系统）。
func ValidateExportPath(root, raw string) (string, *pluginapi.Error) {
	if strings.TrimSpace(raw) == "" {
		return "", invalid("file_path 不得为空")
	}
	if raw != strings.TrimSpace(raw) {
		return "", invalid("file_path 不得含首尾空白")
	}
	if strings.ContainsRune(raw, 0) {
		return "", invalid("file_path 含 NUL 字节")
	}
	if strings.Contains(raw, "\\") {
		return "", invalid("file_path 含反斜杠")
	}
	rootClean := filepath.Clean(strings.TrimSpace(root))
	if rootClean == "" || !filepath.IsAbs(rootClean) {
		return "", internalErr("导出目录必须是绝对路径")
	}
	for _, segment := range strings.Split(raw, "/") {
		if segment == ".." {
			return "", invalid("file_path 含 .. 段，禁止穿越")
		}
	}
	var joined string
	if filepath.IsAbs(raw) {
		joined = filepath.Clean(raw)
	} else {
		joined = filepath.Join(rootClean, raw)
	}
	if !within(rootClean, joined) {
		return "", invalid("file_path 越出导出目录")
	}
	return joined, nil
}

// evalRealRoot 解析导出根目录的真实路径（跟随根自身的符号链接）。
func evalRealRoot(root string) (string, *pluginapi.Error) {
	clean := filepath.Clean(strings.TrimSpace(root))
	if clean == "" || !filepath.IsAbs(clean) {
		return "", internalErr("导出目录必须是绝对路径")
	}
	real, err := filepath.EvalSymlinks(clean)
	if err != nil {
		return "", invalid("导出目录不存在或不可访问，请先创建")
	}
	info, err := os.Stat(real)
	if err != nil || !info.IsDir() {
		return "", invalid("导出目录不可用")
	}
	return real, nil
}

// within 判定 p 是否等于 root 或位于 root 之下（基于 Rel，避免字符串前缀误判）。
func within(root, p string) bool {
	rel, err := filepath.Rel(root, p)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}

// ResolveExisting 校验并解析一个必须已存在的导出文件：跟随符号链接后二次校验归属。
func ResolveExisting(root, raw string) (string, *pluginapi.Error) {
	joined, perr := ValidateExportPath(root, raw)
	if perr != nil {
		return "", perr
	}
	realRoot, perr := evalRealRoot(root)
	if perr != nil {
		return "", perr
	}
	real, err := filepath.EvalSymlinks(joined)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return "", invalid("文件不存在: " + raw)
		}
		return "", internalErr("解析导入文件失败: " + err.Error())
	}
	if !within(realRoot, real) {
		return "", invalid("file_path 经符号链接解析后越出导出目录")
	}
	info, err := os.Stat(real)
	if err != nil {
		return "", internalErr("读取导入文件信息失败: " + err.Error())
	}
	if !info.Mode().IsRegular() {
		return "", invalid("file_path 必须是普通文件")
	}
	return real, nil
}

// ResolveTarget 校验可能尚不存在的导出目标：解析最近的已存在祖先并校验归属，
// 再把尚未创建的尾段拼回去；祖先链上任何符号链接逃逸都会被拒绝。
func ResolveTarget(root, raw string) (string, *pluginapi.Error) {
	joined, perr := ValidateExportPath(root, raw)
	if perr != nil {
		return "", perr
	}
	realRoot, perr := evalRealRoot(root)
	if perr != nil {
		return "", perr
	}
	current, tail := joined, ""
	for {
		real, err := filepath.EvalSymlinks(current)
		if err == nil {
			if !within(realRoot, real) {
				return "", invalid("file_path 经符号链接解析后越出导出目录")
			}
			return filepath.Join(real, tail), nil
		}
		if !errors.Is(err, fs.ErrNotExist) {
			return "", internalErr(fmt.Sprintf("解析导出路径失败: %v", err))
		}
		parent := filepath.Dir(current)
		if parent == current {
			return "", invalid("导出路径不存在")
		}
		tail = filepath.Join(filepath.Base(current), tail)
		current = parent
	}
}
