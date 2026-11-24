//go:build linux

package boundary

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/pengtang1024/kore-plugin-db-mysql/internal/mysqlop"
	"github.com/pengtang1024/kore/pkg/pluginapi"
)

// TestResourceExhaustionExportFileSizeLimit 资源耗尽（真实内核注入）：
// 用 setrlimit(RLIMIT_FSIZE)（即 ulimit -f）把导出子进程的单文件写入上限压到 4 KiB，
// 假 mysqldump 向真实导出文件写 256 KiB 时被内核以 SIGXFSZ 终止，插件必须：
//  1. 返回结构化 E_UPSTREAM（不是崩溃/挂起）；
//  2. 删除半成品导出文件。
//
// 这是真实 OS 资源限制注入（非进程内旗标）：先用同一条导出路径跑控制组证明成功，
// 再注入限额证明失败，失败原因只可能是限额。
func TestResourceExhaustionExportFileSizeLimit(t *testing.T) {
	dir := t.TempDir()
	cfg := boundaryConfig(
		fakeMySQL(t, 0),
		writeScript(t, dir, "mysqldump", hugeDumpScript),
		filepath.Join(dir, "exports"),
	)

	// 控制组：未注入限额时导出 256 KiB 成功。
	raw, perr := mysqlop.Call("db-mysql.export",
		json.RawMessage(`{"instance":"primary","database":"kore_perm","file_path":"control.sql"}`), cfg, nil)
	if perr != nil {
		t.Fatalf("控制组导出失败（脚本或路径问题）: %v", perr)
	}
	var control struct {
		SizeBytes int64 `json:"size_bytes"`
	}
	decodeResult(t, raw, &control)
	if control.SizeBytes != 256<<10 {
		t.Fatalf("控制组导出大小=%d，期望 %d", control.SizeBytes, 256<<10)
	}

	var original syscall.Rlimit
	if err := syscall.Getrlimit(syscall.RLIMIT_FSIZE, &original); err != nil {
		t.Fatalf("读取 RLIMIT_FSIZE 失败: %v", err)
	}
	limited := syscall.Rlimit{Cur: 4096, Max: original.Max}
	if err := syscall.Setrlimit(syscall.RLIMIT_FSIZE, &limited); err != nil {
		t.Fatalf("注入 RLIMIT_FSIZE=4096 失败: %v", err)
	}
	defer func() {
		if err := syscall.Setrlimit(syscall.RLIMIT_FSIZE, &original); err != nil {
			t.Errorf("还原 RLIMIT_FSIZE 失败: %v", err)
		}
	}()

	_, perr = mysqlop.Call("db-mysql.export",
		json.RawMessage(`{"instance":"primary","database":"kore_perm","file_path":"limited.sql"}`), cfg, nil)
	if perr == nil {
		t.Fatal("RLIMIT_FSIZE=4096 下导出未失败，资源注入无效")
	}
	if perr.Code != pluginapi.E_UPSTREAM {
		t.Fatalf("错误码=%q，期望 E_UPSTREAM（message=%q）", perr.Code, perr.Message)
	}
	if perr.Message == "" {
		t.Fatal("失败信息为空：无法定位是客户端被资源限制终止")
	}
	target := filepath.Join(cfg.ExportDir, "limited.sql")
	if _, err := os.Stat(target); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("失败后残留半成品导出文件 %s（stat err=%v）", target, err)
	}
}
