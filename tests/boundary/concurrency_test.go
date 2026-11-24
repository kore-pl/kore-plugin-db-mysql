package boundary

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pengtang1024/kore-plugin-db-mysql/internal/mysqlop"
	"github.com/pengtang1024/kore/pkg/pluginapi"
)

// 并发用例使用真实并发（多 goroutine 同时走 mysqlop.Call → os/exec 子进程），
// 配合 `go test -race` 断言无数据竞争；同时逐次校验结果内容，避免「只跑不验」。
// 假客户端是无状态脚本，因此任何跨调用串值都只能来自插件内部的共享可变状态。

// TestConcurrentQueriesSameInstance 并发查询同一实例（同一连接池语义）：
// 每次查询带唯一 marker，返回值必须与自己的 marker 一致，禁止串值。
func TestConcurrentQueriesSameInstance(t *testing.T) {
	t.Parallel()
	cfg := boundaryConfig(fakeMySQL(t, 0), "", filepath.Join(t.TempDir(), "exports"))

	const workers, rounds = 16, 8
	var wg sync.WaitGroup
	errs := make(chan error, workers*rounds)
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			for i := 0; i < rounds; i++ {
				marker := fmt.Sprintf("g%02d-i%02d", worker, i)
				params := fmt.Sprintf(`{"instance":"primary","sql":%q}`, "SELECT '"+marker+"' AS marker")
				raw, perr := mysqlop.Call("db-mysql.query", json.RawMessage(params), cfg, nil)
				if perr != nil {
					errs <- fmt.Errorf("%s 查询失败: %v", marker, perr)
					continue
				}
				var out struct {
					Rows []struct {
						Marker string `json:"marker"`
					} `json:"rows"`
					RowCount  int  `json:"row_count"`
					Truncated bool `json:"truncated"`
				}
				if err := json.Unmarshal(raw, &out); err != nil {
					errs <- fmt.Errorf("%s 结果解析失败: %v", marker, err)
					continue
				}
				if len(out.Rows) != 1 || out.RowCount != 1 || out.Truncated {
					errs <- fmt.Errorf("%s 结果形状错误: %s", marker, raw)
					continue
				}
				if out.Rows[0].Marker != marker {
					errs <- fmt.Errorf("%s 被串值成 %q（共享状态污染）", marker, out.Rows[0].Marker)
				}
			}
		}(w)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
}

// TestConcurrentCreateDropDatabase 并发建库与并发删库。
// 建库阶段 COUNT=0（不存在）、删库阶段 COUNT=1（存在），两类结果分别断言。
func TestConcurrentCreateDropDatabase(t *testing.T) {
	t.Parallel()

	const workers, rounds = 8, 4
	t.Run("create", func(t *testing.T) {
		cfg := boundaryConfig(fakeMySQL(t, 0), "", "")
		runConcurrent(t, workers, rounds, func(worker, round int) error {
			name := fmt.Sprintf("kore_b_%02d_%02d", worker, round)
			params := fmt.Sprintf(`{"instance":"primary","name":%q}`, name)
			raw, perr := mysqlop.Call("db-mysql.createDatabase", json.RawMessage(params), cfg, nil)
			if perr != nil {
				return fmt.Errorf("建库 %s 失败: %v", name, perr)
			}
			var out struct {
				Created bool   `json:"created"`
				Name    string `json:"name"`
			}
			if err := json.Unmarshal(raw, &out); err != nil {
				return fmt.Errorf("建库 %s 结果解析失败: %v", name, err)
			}
			if !out.Created || out.Name != name {
				return fmt.Errorf("建库 %s 结果错误: %s", name, raw)
			}
			return nil
		})
	})

	t.Run("drop", func(t *testing.T) {
		cfg := boundaryConfig(fakeMySQL(t, 1), "", "")
		runConcurrent(t, workers, rounds, func(worker, round int) error {
			name := fmt.Sprintf("kore_b_%02d_%02d", worker, round)
			params := fmt.Sprintf(`{"instance":"primary","name":%q,"confirm":true}`, name)
			raw, perr := mysqlop.Call("db-mysql.dropDatabase", json.RawMessage(params), cfg, nil)
			if perr != nil {
				return fmt.Errorf("删库 %s 失败: %v", name, perr)
			}
			var out struct {
				Dropped bool   `json:"dropped"`
				Name    string `json:"name"`
			}
			if err := json.Unmarshal(raw, &out); err != nil {
				return fmt.Errorf("删库 %s 结果解析失败: %v", name, err)
			}
			if !out.Dropped || out.Name != name {
				return fmt.Errorf("删库 %s 结果错误: %s", name, raw)
			}
			return nil
		})
	})
}

// TestConcurrentChangePassword 并发改密：每个 goroutine 用独立账号与独立密码，
// 断言全部成功且响应/错误文本中都不出现密码明文。
func TestConcurrentChangePassword(t *testing.T) {
	t.Parallel()
	cfg := boundaryConfig(fakeMySQL(t, 1), "", "")

	const workers, rounds = 8, 3
	type outcome struct {
		password string
		raw      string
		err      string
	}
	outcomes := make(chan outcome, workers*rounds)
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			for i := 0; i < rounds; i++ {
				user := fmt.Sprintf("u%02d", worker)
				password := fmt.Sprintf("Str0ng-Pass-%02d-%d", worker, i)
				params := fmt.Sprintf(
					`{"instance":"primary","user":%q,"host":"localhost","password":%q}`,
					user, password)
				var raw json.RawMessage
				var perr *pluginapi.Error
				// 与 runConcurrent 同口径：只对宿主 fork/exec 瞬时失败退避重试，
				// 断言强度不变（真失败照样进 result.err 判 FAIL）。
				callErr := runOne(func() error {
					raw, perr = mysqlop.Call("db-mysql.changePassword", json.RawMessage(params), cfg, nil)
					if perr != nil {
						return fmt.Errorf("%s", perr.Message)
					}
					return nil
				})
				result := outcome{password: password, raw: string(raw)}
				if callErr != nil {
					result.err = callErr.Error()
				}
				outcomes <- result
			}
		}(w)
	}
	wg.Wait()
	close(outcomes)

	for result := range outcomes {
		if result.err != "" {
			t.Fatalf("并发改密失败: %s", result.err)
		}
		var out struct {
			User    string `json:"user"`
			Host    string `json:"host"`
			Changed bool   `json:"changed"`
		}
		if err := json.Unmarshal([]byte(result.raw), &out); err != nil {
			t.Fatalf("改密结果解析失败: %v（%s）", err, result.raw)
		}
		if !out.Changed {
			t.Fatalf("改密结果 changed=false: %s", result.raw)
		}
		if strings.Contains(result.raw, result.password) {
			t.Fatalf("改密结果泄漏了密码明文: %s", result.raw)
		}
	}
}

// TestConcurrentInstanceProbes 并发探测多实例：验证 Client.Instances 的固定 worker
// 池在并发调用下结果不串位（每个实例必须拿到自己的名字与版本）。
func TestConcurrentInstanceProbes(t *testing.T) {
	t.Parallel()
	cfg := boundaryConfig(fakeMySQL(t, 0), "", "")
	cfg.Instances = cfg.Instances[:0]
	for i := 0; i < 8; i++ {
		cfg.Instances = append(cfg.Instances, mysqlop.InstanceConfig{
			Name: fmt.Sprintf("inst%02d", i),
			Host: "127.0.0.1",
			Port: 3306 + i,
			User: "root",
		})
	}

	runConcurrent(t, 6, 3, func(int, int) error {
		raw, perr := mysqlop.Call("db-mysql.instances", json.RawMessage(`{}`), cfg, nil)
		if perr != nil {
			return fmt.Errorf("探测失败: %v", perr)
		}
		var out struct {
			Instances []struct {
				Name      string `json:"name"`
				Reachable bool   `json:"reachable"`
				Version   string `json:"version"`
			} `json:"instances"`
			Count int `json:"count"`
		}
		if err := json.Unmarshal(raw, &out); err != nil {
			return fmt.Errorf("探测结果解析失败: %v", err)
		}
		if out.Count != len(cfg.Instances) || len(out.Instances) != len(cfg.Instances) {
			return fmt.Errorf("实例数错误: count=%d len=%d", out.Count, len(out.Instances))
		}
		for i, instance := range out.Instances {
			if instance.Name != cfg.Instances[i].Name {
				return fmt.Errorf("实例串位: 第 %d 位=%q，期望 %q", i, instance.Name, cfg.Instances[i].Name)
			}
			if !instance.Reachable || instance.Version != "8.0.99-fake" {
				return fmt.Errorf("实例 %s 探测结果错误: %+v", instance.Name, instance)
			}
		}
		return nil
	})
}

// forkGate 限制「同时进入真实 fork/exec」的调用数。
//
// 为什么需要：本文件的并发用例各自 6–16 个 goroutine，每个 goroutine 都真实
// os/exec 一个假客户端，假客户端本身还会 fork shell 内建（tr / cat / sed）。
// 4 个用例都用 t.Parallel()，叠加后瞬时进程创建可达数百；宿主 fork 预算被占满时
// 立即返回 EAGAIN，插件如实报 E_UPSTREAM「本地进程创建失败」，用例却把它误判成
// 「并发建库/删库有 Bug」——2026-11-24 第 15 段那次偶发 FAIL 的真身就是它
// （原文：建库 kore_b_07_00 失败: E_UPSTREAM: mysql 客户端失败: mysql 客户端无错误输出；
//
//	多实例并发压测可稳定复现 1/24，见提交说明）。
//
// 预算按本机并行度相对推导（不写死绝对数字，正对 4aae07a 的教训：绝对 fd/进程预算
// 在 -race 与不同宿主继承环境下是错的）。只限**峰值**，不减少总操作数，断言强度不变。
var forkGate = make(chan struct{}, forkBudget())

// forkBudget 返回并发 fork 峰值上限：相对 GOMAXPROCS，夹在 [4, 8]。
func forkBudget() int {
	n := runtime.GOMAXPROCS(0)
	if n > 8 {
		n = 8
	}
	if n < 4 {
		n = 4
	}
	return n
}

// forkRetryAttempts 是对「宿主 fork 预算瞬时不足」的退避重试次数。
// 只对这种错误重试：EAGAIN 是瞬时的，一批 goroutine 退出后即可用；
// 被测代码的真实失败（参数错、上游报错、结果不对）会在首次就返回，不被掩盖。
const forkRetryAttempts = 5

// isLocalForkFailure 判断错误是否来自宿主 fork/exec 资源瞬时不足。
// 插件侧把这类失败标注为「本地进程创建失败」并带出 errno（internal/mysqlop/client.go），
// 与「上游 MySQL 报错」可区分。
func isLocalForkFailure(err error) bool {
	return err != nil && strings.Contains(err.Error(), "本地进程创建失败")
}

// runOne 在 forkGate 预算内执行 fn；仅当失败是宿主 fork 预算瞬时不足时退避重试。
// 其他任何错误立即返回——不放宽断言，真失败照样判 FAIL。
func runOne(fn func() error) error {
	var err error
	for attempt := 0; attempt < forkRetryAttempts; attempt++ {
		err = func() error {
			forkGate <- struct{}{}
			defer func() { <-forkGate }()
			return fn()
		}()
		if !isLocalForkFailure(err) {
			return err
		}
		time.Sleep(time.Duration(attempt+1) * 10 * time.Millisecond)
	}
	return err
}

// runConcurrent 并发执行 fn(worker, round)，汇总所有错误后统一报告。
func runConcurrent(t *testing.T, workers, rounds int, fn func(worker, round int) error) {
	t.Helper()
	var wg sync.WaitGroup
	errs := make(chan error, workers*rounds)
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			for i := 0; i < rounds; i++ {
				if err := runOne(func() error { return fn(worker, i) }); err != nil {
					errs <- err
				}
			}
		}(w)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
}
