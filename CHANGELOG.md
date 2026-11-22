# Changelog

## 0.1.0 (2026-11-22)

- feat: 新建 db-mysql 插件骨架，复用 kore.plugin.v1 握手与会话循环；声明生产级深度与 `system:admin` 能力，与 `db-postgres` 互斥
- feat: `db-mysql.instances` 真实连通性探测（`SELECT 1, VERSION()`），逐实例返回 `reachable/version/error`，单实例失败不影响其它实例
- feat: `db-mysql.databases`（`SHOW DATABASES`，默认过滤 `information_schema/mysql/performance_schema/sys`）、`db-mysql.query`（只读 SQL，默认 limit 100 / 上限 1000，NULL 与字段转义解析）、`db-mysql.status`（`SHOW STATUS` 关键指标 + `SHOW VARIABLES` 的 version/max_connections）
- feat: 实例配置支持 `KORE_MYSQL_INSTANCES` / `KORE_MYSQL_CONFIG` / `KORE_MYSQL_ROOT/instances.json`，密码只以 `password_env` 引用；严格拒绝未知字段与 argv 选项注入
- security: 密码只经 `MYSQL_PWD` 环境变量传给子进程（不进 argv）；子进程最小环境、`--no-defaults`、错误 stderr 脱敏与截断、默认 15s 超时
- security: SQL 只读白名单（SELECT/SHOW/DESCRIBE/EXPLAIN），拒绝多语句、写语句、`INTO OUTFILE`、加锁读、阻塞函数、可执行注释与客户端反斜杠命令；写操作在触达数据库前返回 `E_INVALID`
- refactor: 移除占位方法 `db-mysql.slowQueries`；`tests/fault/` 四类故障注入改为覆盖四个真实方法
- refactor: 方法名按协议统一为 `<plugin-id>.<action>`（原 `mysql.*` → `db-mysql.*`），移除裸动词与旧域前缀兼容；`POST /api/v1/invoke/db-mysql/db-mysql.instances` 不再被内核以域不一致拒绝（E_INVALID）
- test: 新增 `tests/e2e/` 真实 MySQL docker 端到端验证（实例探测、库列表、只读查询、NULL/制表符、截断、status、写操作拒绝、密码不外泄），默认跳过
- build: 确定性打包（--sort=name --mtime=@0 --owner=0 --group=0 --numeric-owner + gzip -9n + chmod 归一）
