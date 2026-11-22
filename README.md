# kore-plugin-db-mysql

MySQL/MariaDB 管理插件：真实探测实例、列库、执行只读查询、读取关键状态。**不引入任何 MySQL driver、不内置 mysql 客户端二进制**（G-17）：通过 `os/exec` 调用宿主机上的 `mysql` 客户端，插件只依赖 `kore/pkg/pluginapi`。

## 定位与边界

- **管理用户已有 MySQL/MariaDB**：实例由使用者自行部署与维护；插件只读，不建库建表、不代管备份。
- **客户端由宿主机提供**：默认从 `PATH` 找 `mysql`（MariaDB 客户端同样可用），可用 `KORE_MYSQL_BIN` 指定绝对路径；找不到时返回 `E_UNSUPPORTED`，不静默降级。
- **零 driver / 零捆绑**：不 import `go-sql-driver/mysql` 或任何数据库驱动，不打包 `mysql`/`mysqldump`/`mariadb`。
- **读写边界**：只有只读 SQL 白名单内的方法；写操作在触达数据库前返回 `E_INVALID`。进一步的最小权限建议见「安全」。

## 协议面

- `kore.plugin.v1`，`lifecycle=lazy`，`depth_level=production`
- 能力声明：`system:admin`；与 `db-postgres` 互斥
- 握手与响应帧走 stdout，日志一律写 stderr

## 配置契约（`db-mysql.config.get` / `db-mysql.config.set`）

遵循 `kore/docs/PLUGIN-CONFIG.md`：存储 `<KORE_STATE_DIR>/config.json`（0600 原子写），优先级 **config.json > 环境变量 > 代码默认值**，保存后同一进程内下一次调用立即生效（`restart_required=false`）。这份配置只描述**默认实例**（实例名 `default`），旧的多实例路径继续生效，两者同时存在时 default 由配置文件覆盖。

| 字段 | 类型 | 默认 | 说明 |
| --- | --- | --- | --- |
| `host` | string | `127.0.0.1` | 默认实例主机名/IP |
| `port` | int | `3306` | 默认实例端口 |
| `user` | string | `root` | 默认实例用户名 |
| `password` | string（sensitive） | `""` | 密码，只存本机 0600 文件；`config.get` 只回 `***` |
| `password_env` | string | `KORE_MYSQL_PASSWORD` | 密码环境变量名；`password` 优先 |
| `databases_include_system` | bool | `false` | 库列表默认是否包含系统库 |

环境变量兜底（仅当配置文件不存在时生效）：`KORE_MYSQL_HOST`、`KORE_MYSQL_PORT`、`KORE_MYSQL_USER`、`KORE_MYSQL_PASSWORD_ENV`、`KORE_MYSQL_DATABASES_INCLUDE_SYSTEM`。

## 实例配置

按优先级读取（三处都没有时视为「未配置实例」，方法返回空列表/`E_NOTFOUND`，插件仍可启动）：

1. `KORE_MYSQL_INSTANCES`：直接携带 JSON 数组
2. `KORE_MYSQL_CONFIG`：JSON 配置文件路径
3. `KORE_MYSQL_ROOT`（缺省 `/var/lib/kore/db-mysql`）下的 `instances.json`

格式（`port` 缺省 3306、`user` 缺省 `root`）：

```json
[
  {"name": "primary", "host": "127.0.0.1", "port": 3306, "user": "root", "password_env": "MYSQL_PRIMARY_PASSWORD"},
  {"name": "replica", "host": "db.internal", "port": 3307, "user": "reader", "password_env": "MYSQL_REPLICA_PASSWORD"}
]
```

- `name`：小写字母/数字开头，仅含小写字母、数字、`.`、`-`，≤64 字符；不得重复
- `host` / `user`：拒绝空白、控制字符与 `-` 开头（防止被 mysql 客户端当成 argv 选项）
- `password_env`：密码环境变量名；旧多实例路径仍不保存密码值，未声明时不带密码
- 未知字段（例如在 `KORE_MYSQL_INSTANCES` 里误写 `"password": "..."`）直接报错，避免明文密码被静默忽略；默认实例的密码走 `config.json` 的 `password` 字段
- `databases_include_system` 只来自配置契约 / `KORE_MYSQL_DATABASES_INCLUDE_SYSTEM`；`db-mysql.databases` 请求里的 `include_system` 仍可逐次覆盖
- 配置非法不会让插件启动失败：MySQL 方法统一返回 `E_INVALID`，`kore.ping` 仍可用

## 方法

方法名遵循 `kore.plugin.v1` 的 `<plugin-id>.<action>` 约定（PLUGIN-PROTOCOL §7.2）：域必须等于本插件 id `db-mysql`，**只接受完整限定名**，不接受裸动词或旧 `mysql.` 前缀（v0.1.0 无存量用户，不做兼容层）。HTTP 调用为 `POST /api/v1/invoke/db-mysql/db-mysql.instances`。

| 方法 | 入参 | 结果 |
| --- | --- | --- |
| `db-mysql.instances` | `instance`（可选，限定单个实例） | `{instances:[{name,host,port,reachable,version,error}],count}` |
| `db-mysql.databases` | `instance`（必填）、`include_system`（可选，默认 false） | `{instance,databases:[...],count}` |
| `db-mysql.query` | `instance`（必填）、`sql`（必填）、`limit`（可选，默认 100，上限 1000） | `{columns,rows,row_count,truncated}` |
| `db-mysql.status` | `instance`（必填） | `{instance,version,max_connections,threads_connected,uptime_s,queries,slow_queries}` |

- `db-mysql.instances`：对每个实例执行 `SELECT 1, VERSION()`；单实例失败只写入该实例的 `error` 字段（其余实例照常返回），返回值中**永远没有密码**
- `db-mysql.databases`：`SHOW DATABASES`，默认过滤 `information_schema` / `mysql` / `performance_schema` / `sys`
- `db-mysql.query`：只读白名单（见下节），结果按列名编码为对象；NULL → JSON `null`，规范数字 → JSON number，其余 → string；列名重复时追加 `_2`、`_3`
- `db-mysql.status`：`SHOW STATUS` 的 `Threads_connected` / `Uptime` / `Queries` / `Slow_queries` + `SHOW VARIABLES` 的 `version` / `max_connections`

## 只读 SQL 白名单

- 只允许**单条**以 `SELECT` / `SHOW` / `DESCRIBE` / `DESC` / `EXPLAIN` 开头的语句（大小写不敏感）
- 分号只能出现在末尾；语句内部出现分号（字符串/注释里的除外）一律 `E_INVALID`。**这是必须的**：`mysql -e` 会执行分号分隔的多条语句
- 拒绝 `INSERT` / `UPDATE` / `DELETE` / `DROP` / `TRUNCATE` / `CREATE` / `ALTER` / `REPLACE` / `CALL` / `SET` / `USE` 等任何非白名单开头
- 追加拦截：`SELECT ... INTO OUTFILE/DUMPFILE`、`FOR UPDATE` / `FOR SHARE` / `LOCK IN SHARE MODE`、`SLEEP()` / `BENCHMARK()`、`LOAD_FILE()` / 命名锁函数、`EXPLAIN ANALYZE` 内层非 `SELECT`、可执行注释 `/*! ... */`、客户端反斜杠命令（`\g`、`\!` 等）
- 字符串字面量与注释在判定前被剥离，所以 `SELECT ';'`、`SELECT 'DROP TABLE x'` 不会被误杀

> 只读白名单是应用层防线，不替代数据库账号权限。生产环境请给插件配置**只读账号**（`SELECT`/`SHOW` 权限），把「写操作」挡在服务端。

## 安全

- **密码不进 argv**：只通过子进程环境变量 `MYSQL_PWD` 传递；`ps` 看不到密码
- **最小子进程环境**：只透传 `PATH`/`HOME`/`TMPDIR` + 当前实例的 `MYSQL_PWD`，不会把其它实例的密码环境变量泄漏给客户端
- **`--no-defaults`**：忽略 `~/.my.cnf` 等用户配置，避免隐式选项/凭据干扰
- **错误脱敏**：`stderr` 先过滤 `WARNING` 行、把已知密码替换为 `***`，再截断到 500 字符；返回值与日志都不含密码
- **超时**：命令默认 15s 超时（`KORE_MYSQL_TIMEOUT` 可调，范围 `(0, 2m]`），超时终止客户端并返回 `E_UPSTREAM`
- **结果上限**：`limit` 默认 100、上限 1000；单行 ≤1 MiB、整次结果 ≤4 MiB，超出即终止客户端并标记 `truncated`（低于协议 8 MiB 单帧上限）

## 客户端输出解析

使用 `mysql --batch` 的制表符分隔输出，自己解析而不是依赖驱动：

- `db-mysql.query` 使用默认字段转义（`\t`、`\n`、`\\`、`\0`），解析时还原。**刻意不使用 `--raw`**：`--raw` 会关闭字段转义，字段内的制表符/换行会破坏 TSV（MariaDB 11.8 与 MySQL 8.4 客户端实测一致）
- 版本/状态等元数据值域受控，使用 `--batch --raw --skip-column-names`
- 客户端把 SQL `NULL` 输出为文本 `NULL`（`--batch` 下 MariaDB 与 MySQL 客户端行为一致），因此 `NULL` / `\N` 都映射为 JSON `null`；已知边界：batch 文本无法区分 SQL NULL 与字符串字面量 `NULL`，需要该字面量时用 `CONCAT('NU','LL')` 之类写法规避

## 四类故障注入

`tests/fault/` 对四个方法分别注入 kill -9、超时、磁盘满、cgroup 超限，断言方法级错误码与类别对应：

| 故障 | 文件 | 期望错误码 |
| --- | --- | --- |
| kill -9 | `kill_test.go` | `E_UPSTREAM` |
| 超时 | `timeout_test.go` | `E_UPSTREAM` |
| 磁盘满 | `disk_test.go` | `E_INTERNAL` |
| cgroup 超限 | `cgroup_test.go` | `E_UNSUPPORTED` |

## 构建与验收

```bash
gofmt -l . && go vet ./... && go test -timeout 200s ./...
make build && make dist
tar -tzf dist/db-mysql-0.1.0.tar.gz | head -1   # info.json
```

`dist` 使用确定性参数归档（`--sort=name --mtime=@0 --owner=0 --group=0 --numeric-owner` + `gzip -9n` + chmod 归一，显式列条目且无 `./` 根条目），重复构建 sha256 不变；`size_bytes` 由打包脚本回填，与实际包体偏差 ≤10%。

### 真实 E2E（docker）

```bash
docker run -d --name kore-mysql-test -e MYSQL_ROOT_PASSWORD=test123 -p 13306:3306 mysql:8
# 等待就绪后：
KORE_MYSQL_E2E=1 \
KORE_MYSQL_INSTANCES='[{"name":"primary","host":"127.0.0.1","port":13306,"user":"root","password_env":"KORE_MYSQL_PASSWORD"}]' \
KORE_MYSQL_PASSWORD=test123 \
go test -timeout 120s -v ./tests/e2e
docker rm -f kore-mysql-test
```

E2E 覆盖：实例可达 + 版本、库列表（含新建测试库、过滤系统库）、`SELECT 1 AS n` 返回 `[{n:1}]`、NULL 与字段内制表符、`limit` 截断、`status` 指标、多语句与写操作被 `E_INVALID` 拒绝、返回内容不含密码。默认 `go test ./...` 会跳过该用例。
