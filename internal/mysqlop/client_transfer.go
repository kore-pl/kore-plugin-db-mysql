package mysqlop

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"time"

	"github.com/pengtang1024/kore/pkg/pluginapi"
)

// transferTimeout 是导入/导出的独立超时：dump 大库可能远超普通 DDL 的 15s，
// 但也不能无限挂住（默认 10 分钟，可用 KORE_MYSQL_TIMEOUT 调普通方法超时）。
const transferTimeout = 10 * time.Minute

// Export 用固定 argv 调用 mysqldump，写 stdout 到白名单目录内的目标文件。
//
// 固定 argv 的含义：数据库名与表名来自已校验的白名单（不含 - 开头、不含空白），
// 它们只作为位置参数出现在最后；没有任何用户输入被拼进选项位。
// mysqldump 不支持 --connect-timeout（MariaDB 11.8 实测报 unknown variable），
// 所以这里不传，连接超时由客户端的 --connect-timeout 在 import 路径负责。
func (c *Client) Export(instance InstanceConfig, database string, tables []string, filePath string) (int64, error) {
	if err := ValidateDatabaseName(database); err != nil {
		return 0, opErrorf(pluginapi.E_INVALID, "%s", err.Error())
	}
	for _, table := range tables {
		if err := ValidateTableName(table); err != nil {
			return 0, opErrorf(pluginapi.E_INVALID, "%s", err.Error())
		}
	}
	if _, err := exec.LookPath(c.dumpBin); err != nil {
		return 0, opErrorf(pluginapi.E_UNSUPPORTED,
			"未找到 mysqldump %q：请安装 mysql/mariadb 客户端工具，或用 %s 指定绝对路径", c.dumpBin, EnvDumpBin)
	}
	password, err := instancePassword(instance)
	if err != nil {
		return 0, opErrorf(pluginapi.E_INVALID, "%s", err.Error())
	}
	if err := os.MkdirAll(filepath.Dir(filePath), 0o700); err != nil {
		return 0, opErrorf(pluginapi.E_INTERNAL, "创建导出目录失败: %v", err)
	}
	file, err := os.OpenFile(filePath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return 0, opErrorf(pluginapi.E_INTERNAL, "创建导出文件失败: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), transferTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, c.dumpBin, c.dumpArgs(instance, database, tables)...)
	cmd.Env = buildCommandEnv(password)
	cmd.Stdout = file
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	runErr := cmd.Run()
	closeErr := file.Close()
	if runErr != nil {
		_ = os.Remove(filePath)
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return 0, timeoutError(transferTimeout)
		}
		return 0, c.execError(runErr, password, stderr.String())
	}
	if closeErr != nil {
		_ = os.Remove(filePath)
		return 0, opErrorf(pluginapi.E_INTERNAL, "关闭导出文件失败: %v", closeErr)
	}
	info, err := os.Stat(filePath)
	if err != nil {
		return 0, opErrorf(pluginapi.E_INTERNAL, "读取导出文件信息失败: %v", err)
	}
	return info.Size(), nil
}

// Import 用 stdin 把 SQL 文件喂给 mysql 客户端；不使用 shell，也不做字符串重定向。
func (c *Client) Import(instance InstanceConfig, database, filePath string) error {
	if err := ValidateDatabaseName(database); err != nil {
		return opErrorf(pluginapi.E_INVALID, "%s", err.Error())
	}
	password, err := instancePassword(instance)
	if err != nil {
		return opErrorf(pluginapi.E_INVALID, "%s", err.Error())
	}
	file, err := os.Open(filePath)
	if err != nil {
		return opErrorf(pluginapi.E_INVALID, "无法读取导入文件: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), transferTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, c.bin, c.importArgs(instance, database)...)
	cmd.Env = buildCommandEnv(password)
	cmd.Stdin = file
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	runErr := cmd.Run()
	closeErr := file.Close()
	if runErr != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return timeoutError(transferTimeout)
		}
		return c.execError(runErr, password, stderr.String())
	}
	if closeErr != nil {
		return opErrorf(pluginapi.E_INTERNAL, "关闭导入文件失败: %v", closeErr)
	}
	return nil
}

// dumpArgs 构造 mysqldump 固定 argv：库名与表名只出现在最后的位置参数位。
func (c *Client) dumpArgs(instance InstanceConfig, database string, tables []string) []string {
	args := []string{
		"--no-defaults", "--single-transaction", "--quick",
		"--default-character-set=utf8mb4",
		"-h", instance.Host,
		"-P", strconv.Itoa(instance.Port),
		"-u", instance.User,
		database,
	}
	return append(args, tables...)
}

// importArgs 构造 mysql 客户端 argv：目标库用 --database，SQL 走 stdin。
func (c *Client) importArgs(instance InstanceConfig, database string) []string {
	return []string{
		"--no-defaults", "--batch", "--default-character-set=utf8mb4",
		"--connect-timeout=5",
		"-h", instance.Host,
		"-P", strconv.Itoa(instance.Port),
		"-u", instance.User,
		"--database=" + database,
	}
}
