# kore-plugin-db-mysql

MySQL/MariaDB 管理插件（**骨架**）：覆盖实例管理 / 慢查询 / 备份钩子协作面的方法层。本仓只交付协议骨架与参数校验，**不真连数据库、不内置 mysql/mysqldump/mariadb 等任何客户端二进制**（裁决 37 / 提案 §2.1 批 2）。

## 协议面

- `kore.plugin.v1`，`lifecycle=lazy`，`depth_level=production`
- 能力声明：`system:admin`；与 `db-postgres` 互斥（均占宿主 3306/5432 类管理面，仓库边界由内核校验）
- 方法：`mysql.instances` / `mysql.slowQueries`（同时接受裸 `instances` / `slowQueries`）
- 握手与会话 stdout 留给协议帧，日志一律写 stderr

## 方法与参数（骨架口径）

| 方法 | 入参 | 结构化结果 |
| --- | --- | --- |
| `mysql.instances` | `instance`（可选，实例名） | `root` / `instances[]` / `count` |
| `mysql.slowQueries` | `instance` / `threshold_ms` / `limit` / `window` | 回填后的过滤条件 + `queries[]` / `count` |

- 实例名：以小写字母/数字开头，仅含小写字母、数字、`.`、`-`，≤64 字符；拒绝路径分隔符与 `..` 段
- `threshold_ms` ∈ `[0, 86400000]`，`limit` ∈ `[0, 1000]`，`window` 须为合法 Go 时长（如 `5m`、`1h`）
- 缺省值：`threshold_ms=1000`、`limit=20`、`window=1h`
- 根目录由 `KORE_MYSQL_ROOT` 提供（缺省 `/var/lib/kore/db-mysql`），仅用于逻辑校验，**不执行任何数据库连接或系统调用**
- 非法参数返回 `E_INVALID`；未知方法返回 `E_NOTFOUND`

## 备份钩子

本插件不内置备份实现、不捆绑 mysqldump。备份由 `backup` 插件承担（提案批 2：`backup` 可选依赖 `db-*`）；本插件提供**实例枚举与状态占位**作为协作契约：`backup` 通过 `mysql.instances` 获取待备实例清单，一致性快照的暂停/恢复由 `backup` 侧实现。骨架阶段不落任何真实快照或 SQL。

## 四类故障注入

`tests/fault/` 四个文件分别注入 kill -9、超时、磁盘满、cgroup 超限，断言方法级返回结构化错误，且 `Error.Code` 与故障类别一一对应：

| 故障 | 文件 | 期望错误码 |
| --- | --- | --- |
| kill -9 | `kill_test.go` | `E_UPSTREAM` |
| 超时 | `timeout_test.go` | `E_UPSTREAM` |
| 磁盘满 | `disk_test.go` | `E_INTERNAL` |
| cgroup 超限 | `cgroup_test.go` | `E_UNSUPPORTED` |

会话层测试使用 `net.Pipe`（无缓冲）：每个 `kore.shutdown` 请求都配对读一次响应帧，避免 `serve` 阻塞在 `WriteJSON` 导致死锁。

## 构建与验收

```bash
gofmt -l . && go vet ./... && go test -timeout 200s ./...
make build && make dist
tar -tzf dist/db-mysql-0.1.0.tar.gz | head -1   # info.json
```

`dist` 使用确定性参数归档（`--sort=name --mtime=@0 --owner=0 --group=0 --numeric-owner` + `gzip -9n` + chmod 归一，显式列条目且无 `./` 根条目），重复构建 sha256 不变；`size_bytes` 由打包脚本回填，与实际包体偏差 ≤10%。
