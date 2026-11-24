# kore-plugin-db-mysql

MySQL/MariaDB 管理插件：真实探测实例、列库、执行只读查询、读取关键状态；L2 起提供日常管理——建库/删库、建用户/删用户、授权、导入导出。**不引入任何 MySQL driver、不内置 mysql/mysqldump 客户端二进制**（G-17）：通过 `os/exec` 调用宿主机上的 `mysql` / `mysqldump`，插件只依赖 `kore/pkg/pluginapi`。

## 定位与边界

- **管理用户已有 MySQL/MariaDB**：实例由使用者自行部署与维护；插件不代管备份策略、不做集群/复制编排。
- **写操作是 L2 能力，不是默认放开**：建库/删库/建用户/删用户/授权/导入导出要求 `info.json` 声明 `system:admin`，插件运行期再次校验（声明缺失即 `E_UNAUTHORIZED`）；删库/删用户还必须显式 `confirm=true`。内核当前对 `invoke` 的方法级 capability 门控仍在内核侧演进（见内核 `authorizeTargetAction` 注释），前端应把写方法作为管理员操作暴露。
- **客户端由宿主机提供**：默认从 `PATH` 找 `mysql`（MariaDB 客户端同样可用）与 `mysqldump`，可用 `KORE_MYSQL_BIN` / `KORE_MYSQLDUMP_BIN` 指定绝对路径；找不到时返回 `E_UNSUPPORTED`，不静默降级。
- **零 driver / 零捆绑**：不 import `go-sql-driver/mysql` 或任何数据库驱动，不打包 `mysql`/`mysqldump`/`mariadb`。
- **读写边界**：`db-mysql.query` 仍然只读；写操作只存在于 L2 的显式管理方法，且先过能力门控与参数/路径校验再触达数据库。

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

方法名遵循 `kore.plugin.v1` 的 `<plugin-id>.<action>` 约定（PLUGIN-PROTOCOL §7.2）：域必须等于本插件 id `db-mysql`，**只接受完整限定名**，不接受裸动词或旧 `mysql.` 前缀。HTTP 调用为 `POST /api/v1/invoke/db-mysql/db-mysql.instances`。

新增写方法的 `instance` 都是可选：缺省用 `default` 实例；只有一个实例时直接用它；多实例且无 `default` 时必须显式指定。

### 只读（L1.5）

| 方法 | 入参 | 结果 |
| --- | --- | --- |
| `db-mysql.instances` | `instance`（可选，限定单个实例） | `{instances:[{name,host,port,reachable,version,error,credential_source}],count}`（`credential_source ∈ kernel_env|plugin_config|none`，只报来源不含口令） |
| `db-mysql.databases` | `instance`（必填）、`include_system`（可选，默认 false） | `{instance,databases:[...],count}` |
| `db-mysql.query` | `instance`（必填）、`sql`（必填）、`limit`（可选，默认 100，上限 1000） | `{columns,rows,row_count,truncated}` |
| `db-mysql.status` | `instance`（必填） | `{instance,version,max_connections,threads_connected,uptime_s,queries,slow_queries}` |
| `db-mysql.slowlog` | `instance`（必填）、`schema`（可选）、`min_ms`（可选，0–3600000）、`limit`（可选，1–100，默认 20） | `{instance,source,schema,min_ms,limit,count,queries:[{schema,digest,sql_text,executions,total_ms,avg_ms,max_ms,rows_examined_avg,rows_sent_avg,first_seen,last_seen}],warnings?,hint}` |

- `db-mysql.instances`：对每个实例执行 `SELECT 1, VERSION()`；单实例失败只写入该实例的 `error` 字段（其余实例照常返回），返回值中**永远没有密码**
- `db-mysql.databases`：`SHOW DATABASES`，默认过滤 `information_schema` / `mysql` / `performance_schema` / `sys`
- `db-mysql.query`：只读白名单（见下节），结果按列名编码为对象；NULL → JSON `null`，规范数字 → JSON number，其余 → string；列名重复时追加 `_2`、`_3`
- `db-mysql.status`：`SHOW STATUS` 的 `Threads_connected` / `Uptime` / `Queries` / `Slow_queries` + `SHOW VARIABLES` 的 `version` / `max_connections`
- `db-mysql.slowlog`：读 `performance_schema.events_statements_summary_by_digest`（按总耗时倒序，可按库与 `min_ms` 过滤），不读 `slow_query_log` 文件、不接受调用方 SQL；需要 `performance_schema=ON` 且账号有 SELECT 权限

### 日常管理（L2，全部要求 `system:admin`）

| 方法 | 入参 | 结果 |
| --- | --- | --- |
| `db-mysql.createDatabase` | `name`（必填）、`charset`（默认 `utf8mb4`）、`collation`（默认 `utf8mb4_unicode_ci`） | `{created,name}` |
| `db-mysql.dropDatabase` | `name`、`confirm`（**必须 true**） | `{dropped,name}` |
| `db-mysql.createUser` | `user`、`host`（默认 `localhost`）、`password`（可选）、`grants`（可选） | `{created,user,host}` |
| `db-mysql.changePassword` | `user`、`host`（默认 `localhost`）、`password`（≥8 字节）、`confirm`（root/系统账号**必须 true**） | `{user,host,changed}`；**不含密码** |
| `db-mysql.dropUser` | `user`、`host`（默认 `localhost`）、`confirm`（**必须 true**） | `{dropped}` |
| `db-mysql.grant` | `user`、`host`（默认 `localhost`）、`database`、`privileges[]` | `{granted,privileges}` |
| `db-mysql.export` | `database`、`tables[]`（可选）、`file_path` | `{database,file_path,size_bytes,tables}` |
| `db-mysql.import` | `database`、`file_path` | `{database,file_path,size_bytes}` |

行为约定：

- `createDatabase`：`CREATE DATABASE IF NOT EXISTS \`name\` CHARACTER SET ... COLLATE ...`；库名只允许 `[a-zA-Z0-9_]`（1-64）；`created=false` 表示库此前已存在。显式传 `charset` 而不传 `collation` 时只写 `CHARACTER SET`，由服务端选该字符集的默认排序规则。
- `dropDatabase`：系统库 `mysql` / `information_schema` / `performance_schema` / `sys` **在任何 confirm 下都返回 `E_UNAUTHORIZED`**；缺少 `confirm=true` 返回 `E_INVALID`（「删库不可恢复，请传 confirm=true」），库不会被删除；库不存在返回 `E_NOTFOUND`。
- `createUser`：`CREATE USER 'u'@'h' IDENTIFIED BY '...'`；不传 `password` 时创建无密码账号（MySQL 默认语义，建议始终传密码）；用户名只允许 `[a-zA-Z0-9_]`（1-32），host 只允许字母/数字/`.`/`_`/`:`/`%`/`-`；已存在时 `created=false`，**不会改密码**；密码只进 SQL（经 stdin），响应与日志都不回显。
- `createUser.grants` 是可选便捷授权，每项格式 `<database>:<权限>[,<权限>...]`，例如 `["app_db:SELECT,INSERT"]`；复用 `grant` 的白名单，创建成功后逐项执行。
- `changePassword`：`ALTER USER 'u'@'h' IDENTIFIED BY ?` 的代码层参数绑定（模板不含密码，绑定值经 stdin 单点转义）；user/host 走同一白名单（引号/分号/反引号一律 `E_INVALID`）；root 等受保护账号缺 `confirm=true` 回 `E_INVALID` 并给 hint；弱密码（<8 字节 / 与账号同名）拒绝；MySQL 错误号映射人话（1045 `E_UNAUTHORIZED`、1396 `E_NOTFOUND`、1227/1142 `E_UNAUTHORIZED`、1819 `E_INVALID`）；密码不进 argv/日志/结果。
- `dropUser`：`root` 账号（任意 host）返回 `E_UNAUTHORIZED`；缺少 `confirm=true` 返回 `E_INVALID`；用户不存在返回 `E_NOTFOUND`。
- `grant`：`GRANT <privs> ON \`db\`.* TO 'u'@'h'`；权限白名单 `SELECT/INSERT/UPDATE/DELETE/CREATE/DROP/ALTER/INDEX/EXECUTE`，**明确拒绝 `ALL PRIVILEGES`、`ALL` 与 `GRANT OPTION`**（授权不可再转授）；权限去空、去重、统一大写。
- 所有写方法的 `database` / `name` / `table` / `charset` / `collation` 都过同一套白名单正则；拼 SQL 时标识符再用反引号包裹，绝不让用户输入以带引号字符串形式进入 SQL。

## 导入导出

- 目录白名单：`file_path` 必须落在 **`KORE_DB_EXPORT_DIR`** 下；未设置时默认 `<KORE_STATE_DIR>/exports`。两者都没有时方法返回 `E_INVALID` 并说明该配哪个变量。
- 路径校验复用 `ResolveExisting` / `ResolveTarget` 语义：词法层拒绝 NUL、反斜杠、`..` 段与越界；再跟随符号链接与真实根目录比对，父目录被换成根外软链接同样拒绝。允许相对路径与「位于导出目录内的绝对路径」。
- 单文件上限 **256 MiB**：导出超限会删除半成品文件并返回 `E_INVALID`；导入空文件、超限文件、非普通文件同样拒绝。
- `export` 调 `mysqldump` 固定 argv（`--no-defaults --single-transaction --quick --default-character-set=utf8mb4` + 连接参数 + 库名/表名位置参数），stdout 直接写目标文件；密码只经 `MYSQL_PWD` 环境变量，不进 argv。
- `import` 把文件接到 `mysql` 客户端 **stdin**（`--database=<db>` 指定目标库），不使用 shell，也不做字符串重定向。
- 导出不含 `CREATE DATABASE`；导入前先用 `createDatabase` 建好目标库（E2E 即按这个顺序验证）。

## 只读 SQL 白名单

- 只允许**单条**以 `SELECT` / `SHOW` / `DESCRIBE` / `DESC` / `EXPLAIN` 开头的语句（大小写不敏感）
- 分号只能出现在末尾；语句内部出现分号（字符串/注释里的除外）一律 `E_INVALID`。**这是必须的**：`mysql -e` 会执行分号分隔的多条语句
- 拒绝 `INSERT` / `UPDATE` / `DELETE` / `DROP` / `TRUNCATE` / `CREATE` / `ALTER` / `REPLACE` / `CALL` / `SET` / `USE` 等任何非白名单开头
- 追加拦截：`SELECT ... INTO OUTFILE/DUMPFILE`、`FOR UPDATE` / `FOR SHARE` / `LOCK IN SHARE MODE`、`SLEEP()` / `BENCHMARK()`、`LOAD_FILE()` / 命名锁函数、`EXPLAIN ANALYZE` 内层非 `SELECT`、可执行注释 `/*! ... */`、客户端反斜杠命令（`\g`、`\!` 等）
- 字符串字面量与注释在判定前被剥离，所以 `SELECT ';'`、`SELECT 'DROP TABLE x'` 不会被误杀

> 只读白名单是应用层防线，不替代数据库账号权限。日常查询建议给插件配置**只读账号**（`SELECT`/`SHOW` 权限）；L2 管理请使用具备建库/删库/建用户/授权权限的账号——`createUser`/`dropUser` 的 `created`/`dropped` 判定需要读取 `mysql.user`。

## 安全

- **密码不进 argv**：只读查询经 `MYSQL_PWD` 环境变量传给客户端；L2 写操作把 SQL 通过 **stdin** 交给客户端（没有 `-e`，也没有 `-p`），含密码的 `CREATE USER` 同样不会出现在 `ps` 里。
- **密码字面量按 sql_mode 转义**：先查 `@@SESSION.sql_mode`；默认模式下把 `\` 写成 `\\`，`NO_BACKSLASH_ESCAPES` 模式下保持反斜杠原样；单引号统一写成两个（两种模式都成立）。E2E 用含 `'` 与 `\` 的密码真实登录验证。
- **最小子进程环境**：只透传 `PATH`/`HOME`/`TMPDIR` + 当前实例的 `MYSQL_PWD`，不会把其它实例的密码环境变量泄漏给客户端。
- **`--no-defaults`**：忽略 `~/.my.cnf` 等用户配置，避免隐式选项/凭据干扰。
- **错误脱敏**：`stderr` 先过滤 `WARNING` 行、把已知密码替换为 `***`，再截断到 500 字符；`createUser` 的执行面错误再按请求密码做一次替换。返回值与响应都不含密码。
- **超时**：普通方法默认 15s（`KORE_MYSQL_TIMEOUT` 可调，范围 `(0, 2m]`）；导入/导出使用独立的 10 分钟超时。
- **结果上限**：`limit` 默认 100、上限 1000；单行 ≤1 MiB、整次结果 ≤4 MiB，超出即终止客户端并标记 `truncated`（低于协议 8 MiB 单帧上限）。

## 客户端输出解析

使用 `mysql --batch` 的制表符分隔输出，自己解析而不是依赖驱动：

- `db-mysql.query` 使用默认字段转义（`\t`、`\n`、`\\`、`\0`），解析时还原。**刻意不使用 `--raw`**：`--raw` 会关闭字段转义，字段内的制表符/换行会破坏 TSV（MariaDB 11.8 与 MySQL 8.4 客户端实测一致）
- 版本/状态等元数据值域受控，使用 `--batch --raw --skip-column-names`
- 客户端把 SQL `NULL` 输出为文本 `NULL`（`--batch` 下 MariaDB 与 MySQL 客户端行为一致），因此 `NULL` / `\N` 都映射为 JSON `null`；已知边界：batch 文本无法区分 SQL NULL 与字符串字面量 `NULL`，需要该字面量时用 `CONCAT('NU','LL')` 之类写法规避

## 四类故障注入

`tests/fault/` 对全部方法（含 L2 写操作）分别注入 kill -9、超时、磁盘满、cgroup 超限，断言方法级错误码与类别对应：

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
tar -tzf dist/db-mysql-0.5.0.tar.gz | head -1   # info.json
```

`dist` 使用确定性参数归档（`--sort=name --mtime=@0 --owner=0 --group=0 --numeric-owner` + `gzip -9n` + chmod 归一，显式列条目且无 `./` 根条目），重复构建 sha256 不变；`size_bytes` 由打包脚本回填并与包体实际大小收敛（±2 字节）。

### 真实 E2E

本机 MySQL（9.x，密码 `***`）：

```bash
KORE_MYSQL_E2E=1 \
KORE_MYSQL_INSTANCES='[{"name":"primary","host":"127.0.0.1","port":3306,"user":"root","password_env":"KORE_MYSQL_PASSWORD"}]' \
KORE_MYSQL_PASSWORD=*** \
go test -timeout 300s -v ./tests/e2e -run TestL2RealMySQL
```

Docker 容器（只读用例）：

```bash
docker run -d --name kore-mysql-test -e MYSQL_ROOT_PASSWORD=test123 -p 13306:3306 mysql:8
KORE_MYSQL_E2E=1 \
KORE_MYSQL_INSTANCES='[{"name":"primary","host":"127.0.0.1","port":13306,"user":"root","password_env":"KORE_MYSQL_PASSWORD"}]' \
KORE_MYSQL_PASSWORD=test123 \
go test -timeout 120s -v ./tests/e2e
docker rm -f kore-mysql-test
```

`TestL2RealMySQL` 覆盖：建库 → 建用户（含引号/反斜杠密码并真实登录）→ 授权并验证 `SHOW GRANTS`/实际读表 → 导出（文件存在且大小 >0）→ 导入新库（列级结构签名与行数一致）→ 不带 confirm 删用户/删库被 `E_INVALID` 拒绝且对象仍在 → 删系统库 `E_UNAUTHORIZED` → 带 confirm 删除并确认消失 → 清理测试库/用户。默认 `go test ./...` 会跳过该用例。

<!-- oss:begin -->
## 开源与贡献

[![CI](https://github.com/kore-pl/kore-plugin-db-mysql/actions/workflows/ci.yml/badge.svg)](https://github.com/kore-pl/kore-plugin-db-mysql/actions/workflows/ci.yml)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](LICENSE)
[![Go 1.26](https://img.shields.io/badge/Go-1.26-00ADD8?logo=go&logoColor=white)](go.mod)

本仓库是 **kore** 的 `MySQL/MariaDB 管理插件`（id `db-mysql`），独立仓库、独立版本线、独立 CI（少触发：push main / tag / PR / 手动）。内核与协议见 [kore](https://github.com/kore-pl/kore)。

- **许可**：MIT（inbound = outbound，不要求 CLA）；提交用 `git commit -s` 签 DCO。
- **贡献**：[CONTRIBUTING.md](CONTRIBUTING.md)（构建、门禁、发版、边界）。
- **安全**：[SECURITY.md](SECURITY.md)——漏洞不要开公开 issue。
- **行为准则**：[CODE_OF_CONDUCT.md](CODE_OF_CONDUCT.md)。
- **兼容性**：`protocol: kore.plugin.v1` · `requires_kore: >=0.1.0 <0.2.0` · `lifecycle: lazy` · 深度等级 `production`。
- **验证方式**：本地 `make check` 优先（CI 只在 push main / tag / PR / 手动跑；发布前 `make dist` 连跑两次核对 sha 一致）。
- **发布产物**：`make dist` → `dist/db-mysql-0.5.0.tar.gz` + `.sha256`（位级可复现，连跑两次 sha 相同）。
- **相关仓库**：[索引 kore-plugins](https://github.com/kore-pl/kore-plugins) · [协议文档](https://github.com/kore-pl/kore/blob/main/docs/PLUGIN-PROTOCOL.md) · [SDK 指南](https://github.com/kore-pl/kore/blob/main/docs/PLUGIN-SDK.md)

<!-- oss:end -->
