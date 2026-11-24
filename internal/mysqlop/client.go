package mysqlop

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/pengtang1024/kore/pkg/pluginapi"
)

// Client 通过本机 mysql 客户端执行数据库操作（os/exec，不引入 driver）。
type Client struct {
	bin     string
	dumpBin string
	timeout time.Duration
}

// NewClient 用配置构造执行器并补齐缺省值。
func NewClient(cfg Config) *Client {
	bin := strings.TrimSpace(cfg.Bin)
	if bin == "" {
		bin = defaultBin
	}
	dumpBin := strings.TrimSpace(cfg.DumpBin)
	if dumpBin == "" {
		dumpBin = defaultDumpBin
	}
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	return &Client{bin: bin, dumpBin: dumpBin, timeout: timeout}
}

// Instances 并行为每个实例做连通性探测（SELECT 1, VERSION()），单实例失败只写入
// 该实例的 error 字段，不影响其它实例。
func (c *Client) Instances(instances []InstanceConfig) ([]InstanceStatus, error) {
	if len(instances) == 0 {
		return []InstanceStatus{}, nil
	}
	if _, err := exec.LookPath(c.bin); err != nil {
		return nil, opErrorf(pluginapi.E_UNSUPPORTED,
			"未找到 mysql 客户端 %q：请安装 mysql/mariadb 客户端，或用 %s 指定绝对路径", c.bin, EnvBin)
	}
	statuses := make([]InstanceStatus, len(instances))
	// 固定 4 个 worker，避免实例多时 goroutine 线性膨胀（G-03 常驻资源预算）。
	workers := 4
	if len(instances) < workers {
		workers = len(instances)
	}
	jobs := make(chan int)
	var wg sync.WaitGroup
	var abortMu sync.Mutex
	var abortErr error
	wg.Add(workers)
	for w := 0; w < workers; w++ {
		go func() {
			defer wg.Done()
			for i := range jobs {
				status, perr := c.probe(instances[i])
				if perr != nil {
					// 非实例归因的失败（宿主进程创建失败）：整体返错，见 probe 的注释。
					abortMu.Lock()
					if abortErr == nil {
						abortErr = perr
					}
					abortMu.Unlock()
					continue
				}
				statuses[i] = status
			}
		}()
	}
	for i := range instances {
		jobs <- i
	}
	close(jobs)
	wg.Wait()
	if abortErr != nil {
		// 不返回部分结果：宿主进程创建失败时，其余实例的 false 同样不可信，
		// 返回半张表只会让运维按「实例不可达」排查网络。
		return nil, abortErr
	}
	return statuses, nil
}

// probe 做一次连通性探测并取回版本；错误文本不含密码。
//
// 返回值 error **只用于不能归因到该实例的失败**（本地进程创建失败）：
// 这类失败与目标实例无关，是宿主 fork/exec 预算问题，调用方必须整体返错，
// 不能降级成 Reachable:false——否则运维会去查网络/防火墙/端口，而真凶是宿主机。
// 可归因到实例的失败（真连不上 / 未知归因）写进 status.Error，不影响其它实例。
func (c *Client) probe(instance InstanceConfig) (InstanceStatus, error) {
	status := InstanceStatus{
		Name: instance.Name,
		Host: instance.Host,
		Port: instance.Port,
	}
	// 凭据来源只报「从哪来」，绝不回显口令本身（G-30）。
	_, status.CredentialSource = resolveCredential(instance)
	rows, err := c.run(instance, "SELECT 1, VERSION()", true, true)
	if err != nil {
		attribution, text := attributeProbeError(err)
		if attribution == probeHostFailure {
			return InstanceStatus{}, err
		}
		status.Error = text
		return status, nil
	}
	if len(rows) == 0 || len(rows[0]) < 2 {
		status.Error = probeUnknownPrefix + "连通性探测返回空结果"
		return status, nil
	}
	status.Reachable = true
	status.Version = rows[0][1]
	return status, nil
}

// Databases 执行 SHOW DATABASES，默认过滤四个系统库。
func (c *Client) Databases(instance InstanceConfig, includeSystem bool) ([]string, error) {
	rows, err := c.run(instance, "SHOW DATABASES", true, false)
	if err != nil {
		return nil, err
	}
	databases := make([]string, 0, len(rows))
	for _, row := range rows {
		if len(row) == 0 || row[0] == "" {
			continue
		}
		name := row[0]
		if !includeSystem {
			if _, system := systemDatabases[strings.ToLower(name)]; system {
				continue
			}
		}
		databases = append(databases, name)
	}
	return databases, nil
}

// statusQuery 一次取回 SHOW STATUS 关键指标与 SHOW VARIABLES 的版本/连接上限。
const statusQuery = "SHOW STATUS WHERE Variable_name IN ('Threads_connected','Uptime','Queries','Slow_queries'); " +
	"SHOW VARIABLES WHERE Variable_name IN ('version','max_connections')"

// Status 读取 SHOW STATUS / SHOW VARIABLES 的关键指标。
func (c *Client) Status(instance InstanceConfig) (StatusResult, error) {
	result := StatusResult{Instance: instance.Name}
	rows, err := c.run(instance, statusQuery, true, true)
	if err != nil {
		return StatusResult{}, err
	}
	for _, row := range rows {
		if len(row) < 2 {
			continue
		}
		key, value := strings.ToLower(row[0]), row[1]
		switch key {
		case "version":
			result.Version = value
		case "max_connections":
			if result.MaxConnections, err = parseStatusInt(key, value); err != nil {
				return StatusResult{}, err
			}
		case "threads_connected":
			if result.ThreadsConnected, err = parseStatusInt(key, value); err != nil {
				return StatusResult{}, err
			}
		case "uptime":
			if result.UptimeS, err = parseStatusInt(key, value); err != nil {
				return StatusResult{}, err
			}
		case "queries":
			if result.Queries, err = parseStatusInt(key, value); err != nil {
				return StatusResult{}, err
			}
		case "slow_queries":
			if result.SlowQueries, err = parseStatusInt(key, value); err != nil {
				return StatusResult{}, err
			}
		}
	}
	if result.Version == "" {
		return StatusResult{}, opErrorf(pluginapi.E_UPSTREAM, "SHOW VARIABLES 未返回 version")
	}
	return result, nil
}

// parseStatusInt 解析状态数值；失败按上游协议错误返回，避免把 0 当真实值。
func parseStatusInt(key, value string) (int64, error) {
	parsed, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
	if err != nil {
		return 0, opErrorf(pluginapi.E_UPSTREAM, "无法解析 SHOW 指标 %s=%q", key, value)
	}
	return parsed, nil
}

// Query 执行只读 SQL 并流式读取结果：最多 limit 行，超过即终止客户端并标记 truncated。
func (c *Client) Query(instance InstanceConfig, sql string, limit int) (QueryResult, error) {
	password, err := instancePassword(instance)
	if err != nil {
		return QueryResult{}, opErrorf(pluginapi.E_INVALID, "%s", err.Error())
	}
	ctx, cancel := context.WithTimeout(context.Background(), c.timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, c.bin, c.args(instance, sql, false, false)...)
	cmd.Env = buildCommandEnv(password)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return QueryResult{}, opErrorf(pluginapi.E_INTERNAL, "创建输出管道失败: %v", err)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return QueryResult{}, c.execError(err, password, "")
	}
	result, readErr := readQueryStream(stdout, limit, cancel)
	if readErr != nil {
		cancel() // 避免子进程阻塞在写管道
	}
	waitErr := cmd.Wait()
	if readErr != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return QueryResult{}, timeoutError(c.timeout)
		}
		return QueryResult{}, readErr
	}
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return QueryResult{}, timeoutError(c.timeout)
	}
	if waitErr != nil && !result.Truncated {
		return QueryResult{}, c.execError(waitErr, password, stderr.String())
	}
	return result, nil
}

// readQueryStream 读取 batch 输出：首行列名，之后每行一条记录；rows 上限为 limit。
// 达到 limit 后若还能读到下一行，或结果总字节超预算，就取消子进程并标记 truncated。
func readQueryStream(stdout io.Reader, limit int, cancel context.CancelFunc) (QueryResult, error) {
	reader := bufio.NewReaderSize(stdout, 64*1024)
	result := QueryResult{Columns: []string{}, Rows: []json.RawMessage{}}
	header, err := readBatchLine(reader, maxLineBytes)
	if errors.Is(err, io.EOF) {
		return result, nil
	}
	if err != nil {
		return result, err
	}
	result.Columns = normalizeColumns(strings.Split(header, "\t"))
	budget := maxResultBytes
	for len(result.Rows) < limit {
		line, err := readBatchLine(reader, maxLineBytes)
		if errors.Is(err, io.EOF) {
			result.RowCount = len(result.Rows)
			return result, nil
		}
		if err != nil {
			return result, err
		}
		row, err := buildRowFromLine(result.Columns, line)
		if err != nil {
			return result, err
		}
		if len(row) > budget {
			result.Truncated = true
			result.RowCount = len(result.Rows)
			cancel()
			return result, nil
		}
		budget -= len(row)
		result.Rows = append(result.Rows, row)
	}
	// 已读满 limit 行：再读一行判断是否还有更多（空行也是一条真实记录）。
	if _, err := readBatchLine(reader, maxLineBytes); err == nil {
		result.Truncated = true
		cancel()
	} else if !errors.Is(err, io.EOF) {
		return result, err
	}
	result.RowCount = len(result.Rows)
	return result, nil
}

// buildRowFromLine 把一行 TSV 编码为 JSON 对象（默认转义模式）。
func buildRowFromLine(columns []string, line string) (json.RawMessage, error) {
	parts := strings.Split(line, "\t")
	fields := make([]batchField, len(parts))
	for i, part := range parts {
		fields[i] = decodeBatchField(part, true)
	}
	return buildRow(columns, fields)
}

// args 构造 mysql 客户端参数。密码绝不进 argv：只通过 MYSQL_PWD 环境变量传递。
func (c *Client) args(instance InstanceConfig, sql string, skipColumnNames, raw bool) []string {
	args := []string{"--no-defaults", "--batch", "--default-character-set=utf8mb4"}
	if skipColumnNames {
		args = append(args, "--skip-column-names")
	}
	if raw {
		args = append(args, "--raw")
	}
	args = append(args,
		"--connect-timeout=5",
		"-h", instance.Host,
		"-P", strconv.Itoa(instance.Port),
		"-u", instance.User,
		"-e", sql,
	)
	return args
}

// run 执行一次元数据查询并解析 TSV 输出；错误信息不含密码。
func (c *Client) run(instance InstanceConfig, sql string, skipColumnNames, raw bool) ([][]string, error) {
	password, err := instancePassword(instance)
	if err != nil {
		return nil, opErrorf(pluginapi.E_INVALID, "%s", err.Error())
	}
	ctx, cancel := context.WithTimeout(context.Background(), c.timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, c.bin, c.args(instance, sql, skipColumnNames, raw)...)
	cmd.Env = buildCommandEnv(password)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return nil, timeoutError(c.timeout)
		}
		return nil, c.execError(err, password, stderr.String())
	}
	rows, err := parseTSV(stdout.Bytes(), !raw)
	if err != nil {
		return nil, opErrorf(pluginapi.E_INTERNAL, "解析 mysql 输出失败: %v", err)
	}
	return rows, nil
}

// buildCommandEnv 构造最小子进程环境：不透传插件进程里的其它实例密码，
// 只把当前实例密码通过 MYSQL_PWD 交给客户端。
func buildCommandEnv(password string) []string {
	env := []string{"LANG=C.UTF-8", "LC_ALL=C.UTF-8"}
	for _, key := range []string{"PATH", "HOME", "TMPDIR"} {
		if value, ok := os.LookupEnv(key); ok && value != "" {
			env = append(env, key+"="+value)
		}
	}
	if password != "" {
		env = append(env, "MYSQL_PWD="+password)
	}
	return env
}

// isLocalForkFailure 判断错误是否为**本地进程创建失败**（exec 一侧的系统错误）。
//
// 这类失败与上游 MySQL 无关，stderr 必为空，旧实现会把它们统一落成
// 「mysql 客户端无错误输出」，把宿主问题误报成数据库故障。已实证的 errno：
//   - ETXTBSY（text file busy）：测试夹具写完假客户端脚本立刻被并发 exec，
//     写端 fd 尚未在 fork 出来的子进程里关闭时 exec 同一 inode → 立即失败。
//     2026-11-24 第 15 段偶发 FAIL 的真身即此（原文见 CHANGELOG 0.5.1）；
//   - EAGAIN / EMFILE / ENFILE / ENOMEM / EINTR：宿主 fork 预算瞬时耗尽。
//
// ENOENT / EACCES 由 exec.Error 分支（LookPath 失败）先行处理，不走这里。
func isLocalForkFailure(err error) bool {
	var pathErr *os.PathError
	return errors.As(err, &pathErr) && pathErr.Op == "fork/exec"
}

// probeAttribution 是探测失败的归因类别——决定「实例不可达」还是「整体返错」。
//
// 为什么需要分类：`Instances` 的 per-instance 结果是 `Reachable bool`，一旦把
// 非实例原因写进去，就只能表达成 `reachable:false` = 「实例不可达」，运维会去查
// 网络 / 防火墙 / 端口。归因错了，排查方向就全错。
type probeAttribution int

const (
	// probeUnreachable：客户端已跑起来、**连接层**失败（拒连 / 连接超时 / DNS 解析失败）。
	// 这就是「实例不可达」，保留 Reachable:false 语义不变。
	probeUnreachable probeAttribution = iota
	// probeHostFailure：本地进程创建失败（fork/exec：ETXTBSY / EAGAIN / EMFILE /
	// ENFILE / ENOMEM / EINTR）。与目标实例无关，是宿主预算问题 → 整体返错，不降级。
	probeHostFailure
	// probeUnknown：无法归因到网络（鉴权失败 / SQL 错 / 空 stderr / 输出解析失败）。
	// 既不升级成整体错误（单个实例的凭据问题不该毒化整张表），也**不能**伪装成
	// 「网络不可达」——文案必须带 probeUnknownPrefix，把排查方向指回凭据与客户端。
	probeUnknown
)

// probeUnknownPrefix 是「未知归因」探测失败的统一前缀，与网络不可达在文案上可区分。
const probeUnknownPrefix = "探测失败（非网络不可达，请先排查凭据与客户端）: "

// networkFailureMarkers 是 mysql/mariadb 客户端**连接层**失败的稳定片段（2002/2003/
// 2013 与 errno 110/111/113/115 的文本形态）。命中即判定实例不可达。
//
// 只列连接层片段：1045（Access denied）、SQL 语法错、1044/1142 权限不足都不在此列——
// 它们证明链路是通的，归 probeUnknown，不能让运维按网络排查。
var networkFailureMarkers = []string{
	"Can't connect to MySQL server",
	"Can't connect to local MySQL server",
	"Unknown MySQL server host",
	"Lost connection to MySQL server",
	"Connection refused",
	"Connection timed out",
	"Operation timed out",
	"No route to host",
	"Network is unreachable",
	"Name or service not known",
	"Temporary failure in name resolution",
	"getaddrinfo",
}

// attributeProbeError 给一次探测失败定性，并给出 per-instance error 文案。
//
// 判据顺序（结构化优先，不靠匹配自己写的中文文案）：
//  1. fork/exec 的 *os.PathError → probeHostFailure（宿主预算，见 isLocalForkFailure）；
//  2. context.DeadlineExceeded → probeUnreachable（连接/整体超时，实例不可达）；
//  3. 客户端真跑起来的 *exec.ExitError 且 stderr 命中 networkFailureMarkers → probeUnreachable；
//  4. 其余 → probeUnknown（带 probeUnknownPrefix）。
func attributeProbeError(err error) (probeAttribution, string) {
	if isLocalForkFailure(err) {
		return probeHostFailure, ""
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return probeUnreachable, errorText(err)
	}
	text := errorText(err)
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		for _, marker := range networkFailureMarkers {
			if strings.Contains(text, marker) {
				return probeUnreachable, text
			}
		}
	}
	return probeUnknown, probeUnknownPrefix + text
}

// execError 把客户端启动/执行失败映射为协议错误；stderr 先脱敏再截断。
func (c *Client) execError(err error, password, stderr string) *OpError {
	var execErr *exec.Error
	if errors.As(err, &execErr) {
		return opErrorf(pluginapi.E_UNSUPPORTED,
			"无法执行 mysql 客户端 %q：请安装 mysql/mariadb 客户端，或用 %s 指定绝对路径", c.bin, EnvBin)
	}
	// fork/exec 系统错误（EAGAIN / EMFILE / ENOMEM）是**本地进程创建**失败，不是上游
	// MySQL 失败。这类错误 stderr 必为空，旧实现只带 stderr → 落成「mysql 客户端无错误
	// 输出」，把本机 fork 预算耗尽误报成上游故障（运维会去查数据库，真凶在宿主）。
	// 带出 errno：诊断能区分「本地进程创建失败（ETXTBSY/EAGAIN…）」与「被测路径真失败」，
	// 并发用例也据此只对本地创建错误重试（断言强度不变）。
	if isLocalForkFailure(err) {
		op := opErrorf(pluginapi.E_UPSTREAM, "本地进程创建失败（%s）: %v", c.bin, err)
		op.cause = err
		return op
	}
	return &OpError{
		Code:    pluginapi.E_UPSTREAM,
		Message: "mysql 客户端失败: " + sanitizeStderr(stderr, password),
		cause:   err,
	}
}

// sanitizeStderr 过滤 WARNING 行、脱敏已知密码并截断，最多保留 500 字符。
func sanitizeStderr(stderr string, secrets ...string) string {
	lines := strings.Split(stderr, "\n")
	kept := make([]string, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(strings.ToUpper(line), "WARNING:") ||
			strings.HasPrefix(strings.ToUpper(line), "MYSQL: [WARNING]") {
			continue
		}
		kept = append(kept, line)
	}
	message := strings.Join(kept, " | ")
	for _, secret := range secrets {
		if secret != "" {
			message = strings.ReplaceAll(message, secret, "***")
		}
	}
	if message == "" {
		message = "mysql 客户端无错误输出"
	}
	if runes := []rune(message); len(runes) > 500 {
		message = string(runes[:500]) + "…"
	}
	return message
}

// 编译期断言：Client 实现 Executor。
var _ Executor = (*Client)(nil)
