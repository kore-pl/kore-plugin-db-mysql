package boundary

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pengtang1024/kore-plugin-db-mysql/internal/mysqlop"
	"github.com/pengtang1024/kore/pkg/pluginapi"
)

// writeLocalFailureBin 造一个能过 LookPath、但在 fork/exec 阶段必失败的客户端：
// 0755 普通文件 + 无 shebang → 内核 execve 返回 ENOEXEC（"exec format error"）。
//
// 这一条命中与压测现场（ETXTBSY/EAGAIN）相同的归因判据
// `*os.PathError{Op:"fork/exec"}`；用 ENOEXEC 是因为它能确定性复现，
// 不需要制造宿主 fork 预算紧张。
func writeLocalFailureBin(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "mysql")
	if err := os.WriteFile(path, []byte("this is not an executable format\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestInstanceProbeLocalFailureReturnsError 是「同一归因缺陷」的运维视角验收：
//
// 宿主进程创建失败时，db-mysql.instances **必须返回错误**，而不是回一张
// `{reachable:false, error:"本地进程创建失败…"}` 的表——后者等于告诉运维
// 「实例不可达」，排查方向会被带去网络 / 防火墙 / 端口，而真凶是宿主 fork 预算。
func TestInstanceProbeLocalFailureReturnsError(t *testing.T) {
	t.Parallel()

	cfg := boundaryConfig(writeLocalFailureBin(t), "", "")
	cfg.Instances = []mysqlop.InstanceConfig{
		{Name: "primary", Host: "127.0.0.1", Port: 3306, User: "root"},
		{Name: "replica", Host: "127.0.0.1", Port: 3307, User: "root"},
	}

	raw, perr := mysqlop.Call("db-mysql.instances", json.RawMessage(`{}`), cfg, nil)
	if perr == nil {
		t.Fatalf("本地进程创建失败未返回错误，降级负载=%s", raw)
	}
	if perr.Code != pluginapi.E_UPSTREAM {
		t.Fatalf("错误码=%q，期望 E_UPSTREAM（可重试语义不变）: %s", perr.Code, perr.Message)
	}
	if !strings.Contains(perr.Message, "本地进程创建失败") {
		t.Fatalf("错误文案无法区分宿主与网络: %q", perr.Message)
	}
	if raw != nil {
		t.Fatalf("错误路径不应返回 per-instance 负载（会被读成实例不可达）: %s", raw)
	}
}

// TestInstanceProbeUnreachableKeepsFalse 是语义守护：真的连不上仍按 per-instance
// `reachable:false` 表达（修复不能把网络故障也升级成整体错误）。
func TestInstanceProbeUnreachableKeepsFalse(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	bin := filepath.Join(dir, "mysql")
	script := "#!/bin/sh\nprintf '%s\\n' \"ERROR 2003 (HY000): Can't connect to MySQL server on '127.0.0.1:3306' (111)\" >&2\nexit 1\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := boundaryConfig(bin, "", "")
	cfg.Timeout = 10 * time.Second

	raw, perr := mysqlop.Call("db-mysql.instances", json.RawMessage(`{}`), cfg, nil)
	if perr != nil {
		t.Fatalf("网络不可达不应升级成整体错误: %s", perr.Message)
	}
	var out struct {
		Instances []struct {
			Name      string `json:"name"`
			Reachable bool   `json:"reachable"`
			Error     string `json:"error"`
		} `json:"instances"`
		Count int `json:"count"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("探测结果解析失败: %v", err)
	}
	if out.Count != 1 || len(out.Instances) != 1 {
		t.Fatalf("实例数错误: count=%d len=%d", out.Count, len(out.Instances))
	}
	if out.Instances[0].Reachable {
		t.Fatalf("连不上却标记可达: %+v", out.Instances[0])
	}
	if !strings.Contains(out.Instances[0].Error, "Can't connect to MySQL server") {
		t.Fatalf("错误原文丢失: %q", out.Instances[0].Error)
	}
	if strings.Contains(out.Instances[0].Error, "非网络不可达") {
		t.Fatalf("网络不可达被误标成未知归因: %q", out.Instances[0].Error)
	}
}
