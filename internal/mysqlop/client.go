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
	timeout time.Duration
}

// NewClient 用配置构造执行器并补齐缺省值。
func NewClient(cfg Config) *Client {
	bin := strings.TrimSpace(cfg.Bin)
	if bin == "" {
		bin = defaultBin
	}
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	return &Client{bin: bin, timeout: timeout}
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
	wg.Add(workers)
	for w := 0; w < workers; w++ {
		go func() {
			defer wg.Done()
			for i := range jobs {
				statuses[i] = c.probe(instances[i])
			}
		}()
	}
	for i := range instances {
		jobs <- i
	}
	close(jobs)
	wg.Wait()
	return statuses, nil
}

// probe 做一次连通性探测并取回版本；错误文本不含密码。
func (c *Client) probe(instance InstanceConfig) InstanceStatus {
	status := InstanceStatus{
		Name: instance.Name,
		Host: instance.Host,
		Port: instance.Port,
	}
	rows, err := c.run(instance, "SELECT 1, VERSION()", true, true)
	if err != nil {
		status.Error = errorText(err)
		return status
	}
	if len(rows) == 0 || len(rows[0]) < 2 {
		status.Error = "连通性探测返回空结果"
		return status
	}
	status.Reachable = true
	status.Version = rows[0][1]
	return status
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

// execError 把客户端启动/执行失败映射为协议错误；stderr 先脱敏再截断。
func (c *Client) execError(err error, password, stderr string) *OpError {
	var execErr *exec.Error
	if errors.As(err, &execErr) {
		return opErrorf(pluginapi.E_UNSUPPORTED,
			"无法执行 mysql 客户端 %q：请安装 mysql/mariadb 客户端，或用 %s 指定绝对路径", c.bin, EnvBin)
	}
	return &OpError{
		Code:    pluginapi.E_UPSTREAM,
		Message: "mysql 客户端失败: " + sanitizeStderr(stderr, password),
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
