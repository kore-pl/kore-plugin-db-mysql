# 使用方法 — MySQL/MariaDB 管理插件

## 协议面

协议 `kore.plugin.v1`，`lifecycle=lazy`，`depth_level=production`，能力 `system:admin`，与 `db-postgres` 互斥。核心方法：`instances` / `databases` / `query` / `status`（只读）与 L2 写方法 `createDatabase` / `dropDatabase` / `createUser` / `dropUser` / `grant` / `changePassword` / `exec` / `export` / `import`。配置契约 `db-mysql.config.get` / `db-mysql.config.set` 描述默认实例，口令只经 0600 配置文件或环境变量，`config.get` 只回 `***`。

## 核心能力

客户端默认从 PATH 找 `mysql` / `mysqldump`，可用 `KORE_MYSQL_BIN` / `KORE_MYSQLDUMP_BIN` 指定绝对路径。配置优先级 `config.json` > 环境变量 > 代码默认值，存储于 `<KORE_STATE_DIR>/config.json`（0600 原子写），保存后同进程下一次调用立即生效（`restart_required=false`）。`db-mysql.exec` 采用两级确认：`INSERT/UPDATE/DELETE/REPLACE` 只需 `confirm=true`，`DROP/TRUNCATE/ALTER/CREATE/...` 必须手打语句首两个关键字。

## 典型流程

1. 握手：调用 `kore.ping` 确认插件存活（`kore.plugin.v1`）。
2. 读操作：调用本插件的只读方法（列表 / 详情 / 探测 / 查询），确认返回结构。
3. 写操作：确认 `info.json` 已声明对应能力，按方法要求提供显式确认参数（如 `confirm: true`）。
4. 失败处理：错误以结构化 `{"code","message"}` 返回；按 `code` 分流，不解析中文文案。

## 安全边界

写能力不是默认放开：`system:admin` 声明 + 运行期再次校验，声明缺失即 `E_UNAUTHORIZED`；删库/删用户必须 `confirm=true`。读写边界严格分离：`query` 仍只读白名单，写操作只存在于显式管理方法，口令经环境变量/0600 文件传递，不进 argv、日志或响应。探测错误按归因分流：真不可达保留 `reachable:false`，本地 fork/exec 失败整体返回 `E_UPSTREAM`，不返回半张表。
