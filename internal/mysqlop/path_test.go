package mysqlop

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pengtang1024/kore/pkg/pluginapi"
)

// TestValidateExportPath 词法边界：相对/绝对都在根内、.. 与越界拒绝。
func TestValidateExportPath(t *testing.T) {
	root := "/var/lib/kore/db-mysql/exports"
	allowed := []string{"dump.sql", "sub/dump.sql", "/var/lib/kore/db-mysql/exports/abs.sql"}
	for _, raw := range allowed {
		got, perr := ValidateExportPath(root, raw)
		if perr != nil {
			t.Fatalf("ValidateExportPath(%q) 意外拒绝: %v", raw, perr)
		}
		if !strings.HasPrefix(got, root) {
			t.Fatalf("ValidateExportPath(%q)=%q，不在根目录内", raw, got)
		}
	}
	rejected := []string{
		"",
		"   ",
		"../etc/passwd",
		"sub/../../etc/passwd",
		"/etc/passwd",
		`a\b.sql`,
		"a\x00b.sql",
		" leading.sql",
		"trailing.sql ",
	}
	for _, raw := range rejected {
		if _, perr := ValidateExportPath(root, raw); perr == nil || perr.Code != pluginapi.E_INVALID {
			t.Fatalf("ValidateExportPath(%q) 未按 E_INVALID 拒绝: %v", raw, perr)
		}
	}
	if _, perr := ValidateExportPath("relative/dir", "dump.sql"); perr == nil || perr.Code != pluginapi.E_INTERNAL {
		t.Fatalf("相对根目录未按 E_INTERNAL 拒绝: %v", perr)
	}
}

// TestResolveExistingMissing 导入不存在的文件返回 E_INVALID 且不回显绝对根路径。
func TestResolveExistingMissing(t *testing.T) {
	root := t.TempDir()
	_, perr := ResolveExisting(root, "missing.sql")
	if perr == nil || perr.Code != pluginapi.E_INVALID || !strings.Contains(perr.Message, "不存在") {
		t.Fatalf("错误=%v，期望 E_INVALID 且说明不存在", perr)
	}
	if strings.Contains(perr.Message, root) {
		t.Fatalf("错误信息回显了导出根目录绝对路径: %q", perr.Message)
	}
}

// TestResolveTargetSymlinkEscape 父目录被换成指向根外的软链接时拒绝写入。
func TestResolveTargetSymlinkEscape(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "exports")
	outside := filepath.Join(base, "outside")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(outside, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "link")); err != nil {
		t.Skipf("当前文件系统不支持符号链接: %v", err)
	}
	_, perr := ResolveTarget(root, "link/escape.sql")
	if perr == nil || perr.Code != pluginapi.E_INVALID {
		t.Fatalf("软链接逃逸错误=%v，期望 E_INVALID", perr)
	}
	if _, perr := ResolveExisting(root, "link"); perr == nil || perr.Code != pluginapi.E_INVALID {
		t.Fatalf("软链接目录逃逸错误=%v，期望 E_INVALID", perr)
	}
	// 根内普通子目录仍可正常解析。
	target, perr := ResolveTarget(root, "sub/dump.sql")
	if perr != nil {
		t.Fatalf("根内路径意外拒绝: %v", perr)
	}
	if target != filepath.Join(root, "sub", "dump.sql") {
		t.Fatalf("解析结果=%q", target)
	}
}
