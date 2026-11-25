# MySQL/MariaDB 管理插件（kore-plugin-db-mysql）

## 简介

本插件真实探测 MySQL/MariaDB 实例、列库、执行只读查询、读取关键状态；L2 起提供日常管理——建库/删库、建用户/删用户、授权、导入导出。不引入任何 MySQL driver、不内置 mysql/mysqldump 客户端二进制（G-17）：通过 `os/exec` 调用宿主机的 `mysql` / `mysqldump`，插件只依赖 `kore/pkg/pluginapi`。客户端缺失返回 `E_UNSUPPORTED`，不静默降级。

## 协议面

协议 `kore.plugin.v1`，`lifecycle=lazy`，`depth_level=production`，能力 `system:admin`，与 `db-postgres` 互斥。核心方法：`instances` / `databases` / `query` / `status`（只读）与 L2 写方法 `createDatabase` / `dropDatabase` / `createUser` / `dropUser` / `grant` / `changePassword` / `exec` / `export` / `import`。配置契约 `db-mysql.config.get` / `db-mysql.config.set` 描述默认实例，口令只经 0600 配置文件或环境变量，`config.get` 只回 `***`。

## 核心能力

客户端默认从 PATH 找 `mysql` / `mysqldump`，可用 `KORE_MYSQL_BIN` / `KORE_MYSQLDUMP_BIN` 指定绝对路径。配置优先级 `config.json` > 环境变量 > 代码默认值，存储于 `<KORE_STATE_DIR>/config.json`（0600 原子写），保存后同进程下一次调用立即生效（`restart_required=false`）。`db-mysql.exec` 采用两级确认：`INSERT/UPDATE/DELETE/REPLACE` 只需 `confirm=true`，`DROP/TRUNCATE/ALTER/CREATE/...` 必须手打语句首两个关键字。

## 安全边界

写能力不是默认放开：`system:admin` 声明 + 运行期再次校验，声明缺失即 `E_UNAUTHORIZED`；删库/删用户必须 `confirm=true`。读写边界严格分离：`query` 仍只读白名单，写操作只存在于显式管理方法，口令经环境变量/0600 文件传递，不进 argv、日志或响应。探测错误按归因分流：真不可达保留 `reachable:false`，本地 fork/exec 失败整体返回 `E_UPSTREAM`，不返回半张表。

## 构建与验证

日常验证用本地 `make check`（gofmt → go vet → go test -timeout 200s）；发布前 `make dist` 连跑两次核对 sha256 一致。边界与并发用例（如 8 路并发建删库）在 `tests/boundary/` 下；故障归因有正反例自证。版本 0.5.2；与 `db-postgres` 互斥，二者不要同时启用写能力。

## 相关链接

- [源码](https://github.com/kore-pl/kore-plugin-db-mysql)
- [kore 内核与协议](https://github.com/kore-pl/kore)
- [插件索引](https://github.com/kore-pl/kore-plugins)
