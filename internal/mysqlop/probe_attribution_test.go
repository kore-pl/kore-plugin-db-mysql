package mysqlop

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pengtang1024/kore/pkg/pluginapi"
)

// writeLocalFailureBin 造一个**能过 LookPath、但在 fork/exec 阶段必失败**的客户端：
// 0755 的普通文件，没有 shebang（内核 execve 返回 ENOEXEC，"exec format error"）。
//
// 为什么不用"目录当 bin"：Instances 先做 exec.LookPath，目录会被 LookPath 挡下来
// 走 E_UNSUPPORTED，到不了 fork/exec。用 ENOEXEC 命中同一条归因判据
// （*os.PathError{Op:"fork/exec"}）；errno 与压测现场的 EAGAIN/ETXTBSY 不同，
// 但分类谓词相同，ETXTBSY/EAGAIN/EMFILE/ENOMEM 的 errno 覆盖见
// TestAttributeProbeErrorClassifies 的合成用例。
func writeLocalFailureBin(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "mysql")
	if err := os.WriteFile(path, []byte("this is not an executable format\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

// writeFailScript 造一个 stderr 固定、退出码 1 的假客户端（模拟客户端真跑起来的失败）。
func writeFailScript(t *testing.T, stderr string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "mysql")
	body := "#!/bin/sh\nprintf '%s\\n' " + shellQuote(stderr) + " >&2\nexit 1\n"
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

// shellQuote 用单引号包住文本，供假客户端脚本安全内嵌。
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// exitErrorOf 跑一个必失败的命令，拿到真实的 *exec.ExitError（不伪造类型）。
func exitErrorOf(t *testing.T) error {
	t.Helper()
	cmd := exec.Command("/bin/sh", "-c", "exit 1")
	if err := cmd.Run(); err == nil {
		t.Fatal("期望命令失败")
	} else {
		return err
	}
	return nil
}

// TestAttributeProbeErrorClassifies 是归因判据表的可执行版本（三类逐一断言）。
func TestAttributeProbeErrorClassifies(t *testing.T) {
	t.Run("本地进程创建失败→宿主归因", func(t *testing.T) {
		// EAGAIN / EMFILE / ENFILE / ENOMEM / ETXTBSY / EINTR 在 Go 侧都是
		// *os.PathError{Op:"fork/exec"}，判据只看 Op，因此这些 errno 走同一条分支。
		for _, errno := range []error{
			errors.New("resource temporarily unavailable"), // EAGAIN
			errors.New("too many open files"),              // EMFILE / ENFILE
			errors.New("cannot allocate memory"),           // ENOMEM
			errors.New("text file busy"),                   // ETXTBSY
			errors.New("interrupted system call"),          // EINTR
		} {
			op := opErrorf(pluginapi.E_UPSTREAM, "本地进程创建失败（bin）: %v", errno)
			op.cause = &os.PathError{Op: "fork/exec", Path: "/usr/bin/mysql", Err: errno}
			attribution, _ := attributeProbeError(op)
			if attribution != probeHostFailure {
				t.Fatalf("errno=%v 归因=%v，期望 probeHostFailure", errno, attribution)
			}
		}
	})

	t.Run("超时→不可达", func(t *testing.T) {
		attribution, text := attributeProbeError(timeoutError(0))
		if attribution != probeUnreachable {
			t.Fatalf("超时归因=%v，期望 probeUnreachable", attribution)
		}
		if !strings.Contains(text, "超时") || strings.Contains(text, probeUnknownPrefix) {
			t.Fatalf("超时文案=%q，期望保留原文且无未知归因前缀", text)
		}
		if !errors.Is(timeoutError(0), context.DeadlineExceeded) {
			t.Fatal("timeoutError 未暴露 context.DeadlineExceeded（结构化归因失效）")
		}
	})

	t.Run("连接层失败→不可达", func(t *testing.T) {
		for _, stderr := range []string{
			"ERROR 2002 (HY000): Can't connect to local MySQL server through socket '/run/mysqld/mysqld.sock' (2)",
			"ERROR 2003 (HY000): Can't connect to MySQL server on '127.0.0.1:3306' (111)",
			"ERROR 2005 (HY000): Unknown MySQL server host 'db.invalid' (2)",
			"ERROR 2013 (HY000): Lost connection to MySQL server at 'reading initial communication packet'",
		} {
			op := &OpError{Code: pluginapi.E_UPSTREAM, Message: "mysql 客户端失败: " + stderr, cause: exitErrorOf(t)}
			attribution, text := attributeProbeError(op)
			if attribution != probeUnreachable {
				t.Fatalf("stderr=%q 归因=%v，期望 probeUnreachable", stderr, attribution)
			}
			if strings.Contains(text, probeUnknownPrefix) {
				t.Fatalf("网络不可达不应带未知归因前缀: %q", text)
			}
		}
	})

	t.Run("鉴权与未知失败→未知归因", func(t *testing.T) {
		for _, stderr := range []string{
			"ERROR 1045 (28000): Access denied for user 'root'@'localhost' (using password: YES)",
			"ERROR 1044 (42000): Access denied for user 'app'@'localhost' to database 'secret'",
			"ERROR 1064 (42000): You have an error in your SQL syntax",
			"mysql 客户端无错误输出",
		} {
			op := &OpError{Code: pluginapi.E_UPSTREAM, Message: "mysql 客户端失败: " + stderr, cause: exitErrorOf(t)}
			attribution, text := attributeProbeError(op)
			if attribution != probeUnknown {
				t.Fatalf("stderr=%q 归因=%v，期望 probeUnknown", stderr, attribution)
			}
			if !strings.HasPrefix(text, probeUnknownPrefix) {
				t.Fatalf("未知归因文案缺少可区分前缀: %q", text)
			}
		}
	})
}

// TestInstancesLocalForkFailureReturnsError 是本次缺陷的正例：
// 本地进程创建失败必须**整体返回错误**，不能降级成某实例 Reachable:false。
func TestInstancesLocalForkFailureReturnsError(t *testing.T) {
	client := NewClient(Config{Bin: writeLocalFailureBin(t)})
	statuses, err := client.Instances([]InstanceConfig{
		{Name: "primary", Host: "127.0.0.1", Port: 3306, User: "root"},
		{Name: "replica", Host: "127.0.0.1", Port: 3307, User: "root"},
	})
	if err == nil {
		t.Fatalf("本地进程创建失败未返回错误，降级结果=%+v", statuses)
	}
	if statuses != nil {
		t.Fatalf("本地进程创建失败仍返回了 per-instance 结果（会被读成实例不可达）: %+v", statuses)
	}
	var op *OpError
	if !errors.As(err, &op) || op.Code != pluginapi.E_UPSTREAM {
		t.Fatalf("错误=%v，期望 E_UPSTREAM（可重试语义不变）", err)
	}
	if !strings.Contains(op.Message, "本地进程创建失败") {
		t.Fatalf("错误文案无法区分宿主与网络: %q", op.Message)
	}
	if !isLocalForkFailure(err) {
		t.Fatal("OpError 未暴露 *os.PathError，结构化归因链断了")
	}
}

// TestInstancesRealUnreachableStaysFalse 是语义守护：真的连不上仍走 per-instance
// Reachable:false（不因为上面的修复把网络故障也升级成整体错误）。
func TestInstancesRealUnreachableStaysFalse(t *testing.T) {
	stderr := "ERROR 2003 (HY000): Can't connect to MySQL server on '127.0.0.1:3306' (111)"
	client := NewClient(Config{Bin: writeFailScript(t, stderr)})
	statuses, err := client.Instances([]InstanceConfig{
		{Name: "primary", Host: "127.0.0.1", Port: 3306, User: "root"},
	})
	if err != nil {
		t.Fatalf("网络不可达不应升级成整体错误: %v", err)
	}
	if len(statuses) != 1 {
		t.Fatalf("状态数=%d，期望 1", len(statuses))
	}
	if statuses[0].Reachable {
		t.Fatalf("连不上却标记可达: %+v", statuses[0])
	}
	if !strings.Contains(statuses[0].Error, "Can't connect to MySQL server") {
		t.Fatalf("错误原文丢失: %q", statuses[0].Error)
	}
	if strings.Contains(statuses[0].Error, probeUnknownPrefix) {
		t.Fatalf("网络不可达被误标成未知归因: %q", statuses[0].Error)
	}
}

// TestInstancesAuthFailureLabelledNotNetwork 是第三类（未知归因）的行为契约：
// 鉴权失败证明链路是通的，保留 per-instance 结果但文案必须把排查方向指回凭据。
func TestInstancesAuthFailureLabelledNotNetwork(t *testing.T) {
	stderr := "ERROR 1045 (28000): Access denied for user 'root'@'localhost' (using password: YES)"
	client := NewClient(Config{Bin: writeFailScript(t, stderr)})
	statuses, err := client.Instances([]InstanceConfig{
		{Name: "primary", Host: "127.0.0.1", Port: 3306, User: "root"},
	})
	if err != nil {
		t.Fatalf("鉴权失败不应升级成整体错误: %v", err)
	}
	if len(statuses) != 1 || statuses[0].Reachable {
		t.Fatalf("鉴权失败应保留 per-instance 且不可达: %+v", statuses)
	}
	if !strings.HasPrefix(statuses[0].Error, probeUnknownPrefix) {
		t.Fatalf("鉴权失败缺少「非网络不可达」标签，运维会去查防火墙: %q", statuses[0].Error)
	}
	if !strings.Contains(statuses[0].Error, "Access denied") {
		t.Fatalf("错误原文丢失: %q", statuses[0].Error)
	}
}
