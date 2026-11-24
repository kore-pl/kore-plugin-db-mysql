# Changelog

## 0.5.2 (2026-11-24)

- fix: `Client.Instances` 不再把**本地进程创建失败**降级成「实例不可达」。旧实现把所有探测错误一律写成 per-instance `{reachable:false, error:"…"}`——运维看到 `reachable:false` 会去查网络 / 防火墙 / 端口，而真凶是宿主 `fork` 预算（ETXTBSY / EAGAIN / EMFILE / ENFILE / ENOMEM / EINTR）。现按归因分流：本地 `fork/exec` 失败（`*os.PathError{Op:"fork/exec"}`，复用 0.5.1 的 `isLocalForkFailure`）→ **整个 `db-mysql.instances` 返回错误**，错误码 `E_UPSTREAM`（可重试语义不变），且**不返回半张表**——半张表同样会被读成「实例不可达」
- feat(归因): 新增 `probeAttribution` 三分类 + `attributeProbeError`，判据结构化（`errors.As` 到 `*os.PathError` / `*exec.ExitError` / `context.DeadlineExceeded`），不匹配自己写的中文文案：
  - **真不可达**（客户端已运行、stderr 命中连接层片段：2002 / 2003 / 2005 / 2013、Connection refused、timed out、DNS 解析失败；或整体超时）→ 保留 `reachable:false`，语义不变
  - **宿主失败**（fork/exec）→ 整体返错，不进 per-instance 结果
  - **未知归因**（1045/1044 鉴权、SQL 错、空 stderr、输出解析失败）→ 保留 per-instance 结果（单个实例的凭据问题不该毒化整张表），但文案加前缀「探测失败（非网络不可达，请先排查凭据与客户端）」，把排查方向指回凭据与客户端；「探测返回空结果」同样归此类
- refactor: `OpError` 新增 `cause` 字段 + `Unwrap()`，只为结构化归因服务，不参与 `Error()`／JSON（`Code` / `Message` 语义与响应零变化）；`timeoutError` 的 cause 固定为 `context.DeadlineExceeded`
- test: 新增 `internal/mysqlop/probe_attribution_test.go`（归因判据表：EAGAIN/EMFILE/ENFILE/ENOMEM/ETXTBSY/EINTR → 宿主；超时与连接层 → 不可达；1045/1044/1064/空输出 → 未知。外加本地失败返错、真不可达仍 false、鉴权打标签）与 `tests/boundary/probe_attribution_test.go`（运维视角：方法返回 `E_UPSTREAM` 且不返回 per-instance 负载）。**未放宽任何既有断言**——`tests/boundary/concurrency_test.go` 与既有单测一行未改
- test(反例自证): 把 `probe` 还原成旧行为（所有错误落 per-instance）后，两个新用例均 FAIL，旧输出原文 `{"reachable":false,…,"error":"本地进程创建失败（…）: fork/exec …: exec format error"}`——证明用例真的在守这条缺陷
- test(压测): 8 路并发 × `-count=40`（合计 320 轮、约 4.6 万次真实 `fork/exec`）`TestConcurrentInstanceProbes` 全绿，0 命中
- 已知边界（诚实标注）: `reachable` 仍是 `bool`，表达不了三态；「未知归因」当前只能靠文案标签区分。若需要机器可读，应加 `error_kind` 字段——属协议变更，本轮不做，避免与「不改 E_UPSTREAM 语义」的约束撞车
- chore: 版本 0.5.1 → 0.5.2（fix = patch），`info.json` / `manifest.json` 同步

## 0.5.1 (2026-11-24)

- fix: `execError` 不再把**本地进程创建失败**误报成「上游 MySQL 失败且无错误输出」。`fork/exec` 返回 `ETXTBSY`（text file busy）/`EAGAIN`/`EMFILE`/`ENFILE`/`ENOMEM`/`EINTR` 时，旧实现只带 stderr（这类错误 stderr 必为空）→ 落成 `E_UPSTREAM: mysql 客户端失败: mysql 客户端无错误输出`，把宿主进程创建失败误导成数据库故障；现按 `*os.PathError{Op:"fork/exec"}` 识别，message 带出 `errno`（`本地进程创建失败（<bin>）: fork/exec ...`），错误码仍为 `E_UPSTREAM`（可重试语义不变，不破坏下游映射）
- test: 并发边界用例加**相对 fork 预算**——`runConcurrent` 每个调用受 `forkGate` 限流，预算按 `GOMAXPROCS` 相对推导并夹在 `[4,8]`（不写死绝对数字，同 `4aae07a` 的 fd 预算教训）；仅对「本地进程创建失败」做 5 次退避重试。**断言一条未放宽**：其他任何错误立即判 FAIL，最终仍要求全部成功
- fix(flaky): 消除 `TestConcurrentCreateDropDatabase` 偶发 FAIL。根因不是并发建库/删库有 Bug：多路 `go test` 并发时宿主 `fork` 预算紧张，`fork/exec` 假客户端脚本返回 `ETXTBSY`（text file busy）/`EAGAIN`，被上面的 `execError` 缺陷伪装成「上游无输出」。复现证据：8 路 `go test ./tests/boundary/` 并发压测，失败原文 `建库 kore_b_07_00 失败: E_UPSTREAM: mysql 客户端失败: mysql 客户端无错误输出`；加临时诊断后抓到真身 `*fs.PathError: fork/exec /tmp/.../mysql: text file busy`。单进程 `-count=30` 全绿 → 只在多进程压测下暴露
- test: `TestConcurrentChangePassword` 同样接入 `runOne` 的 fork 限流与退避重试（此前漏接，压测下仍会命中 ETXTBSY）
- 已知剩余（同一归因缺陷的插件侧）：压测下 `TestConcurrentInstanceProbes` 仍可能以 `Reachable:false` 暴露——`Client.Instances` 把本地进程创建失败降级成「实例不可达」，而不是返回错误；待按 `execError` 的区分口径在探测路径上单独修（不在本次 flaky 用例范围）
- chore: 版本 0.5.0 → 0.5.1（fix = patch），`info.json` / `manifest.json` 同步；仅测试与错误归因改动，不进 dist 产物行为

## 0.5.0 (2026-11-24)

- feat: 新增写语句方法 `db-mysql.exec`（`system:admin`）——把「数据库管理」从只读查询补齐到可改数据/结构：入参 `{instance, sql, confirm, confirm_text?}`，返回 `{instance, kind, executed, columns, rows, row_count}`
- security: **两级确认**取代「一刀切只读」——`INSERT/UPDATE/DELETE/REPLACE` 只要 `confirm=true`（数据行级、可重放）；`DROP/TRUNCATE/ALTER/CREATE/RENAME/GRANT/REVOKE/FLUSH/SET/CALL` 必须 `confirm_text` 手打语句首两个关键字（如 `DROP TABLE`），错误信息直接给出应填短语
- security: 写能力**不借道** `db-mysql.query`——query 仍 `system:read` 只读白名单，exec 单独 `system:admin`；只读操作员拿不到 DROP 权限（方法级 capability 是授权面，不能靠 UI 挡）
- security: 写入白名单共用只读侧的清洗纪律——单条语句（分号只在末尾）、拒 NUL / 反斜杠客户端命令 / 可执行注释 / `INTO OUTFILE|DUMPFILE` / `LOAD_FILE` / `SLEEP|BENCHMARK` / 命名锁 / 超 64 KiB；`LOAD DATA` 明确拒绝（读宿主文件）
- test: 新增 `writeguard_test.go`（放行 10 例 + 拒绝 14 例 + `callExec` 三级断言：未确认不触达执行面、合法写带回分级、ddl 缺短语带可执行提示）；方法表与 capability 一致性表同步
- chore: 版本 0.4.1 → 0.5.0（新增方法 = minor），`info.json` / `manifest.json` 同步
- 未做（留待后续）：表结构浏览 `describeTable`、分页数据浏览 `selectRows`、独立 `import` 文件上传通道——本轮只交付 P0「SQL 控制台能写」

## 0.4.1 (2026-11-23)

- feat: 优先采用内核注入凭据（ADR-0026 / §3.5 #8）——默认实例若存在 `MYSQL_PWD` 环境变量（内核按凭据授权注入），
  直接使用且**不再读取插件 config 口令**；缺失才回落既有 config（`password` / `password_env`），向后兼容、不破坏既有部署
- feat: `db-mysql.instances` 新增只读诊断 `credential_source ∈ kernel_env|plugin_config|none`，让 UI 与排障能看出凭据来自内核注入还是插件配置（只报来源，不含口令）
- security: 口令仍只进子进程环境（不进 argv / 日志 / 响应 / 落盘）；`config.get` 口令字段继续打码 `***`；多实例的非默认实例不被默认实例的内核凭据覆盖

## 0.4.0 (2026-11-23)

- feat: 改密方法 `db-mysql.changePassword`（`system:admin`）——入参 `{instance, user, password, host?}`，执行 `ALTER USER 'u'@'h' IDENTIFIED BY ?`；补上 §3.5 #2「改密需要单独方法」的最后缺口
- security: 改密语句的代码层参数绑定（模板 `ALTER USER ... IDENTIFIED BY ?` 不含密码，绑定值在 `stringLiteral` 单点按服务端 `sql_mode` 转义，仅经 stdin 传给 mysql 客户端）；密码不进 argv / 日志 / 结果体，错误消息统一脱敏为 `***`
- security: user/host 沿用 `[a-zA-Z0-9_]` / host 白名单（引号、分号、反引号直接 `E_INVALID`，不触达执行面）；新密码强度 ≥8 字节、不得与账号同名、≤1024 且无 NUL；root 受保护账号改密缺 `confirm=true` 回 `E_INVALID` 并给可操作 hint（`mysql.sys` 等含点账号本就被白名单拒绝）
- feat: MySQL 错误号映射人话——`ER_ACCESS_DENIED`(1045)/`ER_SPECIFIC_ACCESS_DENIED_ERROR`(1227/1142) → `E_UNAUTHORIZED`，`ER_CANNOT_USER`(1396) → `E_NOTFOUND`，`ER_NOT_VALID_PASSWORD`(1819) → `E_INVALID`；成功只返回 `{user,host,changed:true}`
- chore: `method_capabilities` 增加 `db-mysql.changePassword: system:admin` 与既有 `db-mysql.slowlog: system:read`（此前声明缺项，本次一致性门禁收口）；新增表驱动 capability 一致性测试
- test: 参数化断言（模板无密码、绑定后仅出现一次、引号/反斜杠按 sql_mode 转义、占位符数量严格匹配）、标识符注入拒绝、缺 confirm 拒绝、弱密码拒绝、错误码映射与密码脱敏、结果不含密码、非 admin 拒绝；`tests/fault` 故障矩阵同步
- build: 版本 0.3.2 → 0.4.0（新增方法 = minor），`info.json` / `manifest.json` 同步

## 0.3.2 (2026-11-23)

- feat: 慢查询分析 `db-mysql.slowlog`（只读，`system:read`）——从 `performance_schema.events_statements_summary_by_digest` 读取按 digest 聚合的慢查询，返回 SQL 摘要、库名、执行次数、总/平均/最大耗时（ms）、平均扫描/返回行数与首末出现时间；支持 `instance`（必填）、`schema`（可选）、`min_ms`（0–3600000）、`limit`（1–100，缺省 20，按总耗时倒序）
- security: 不读 `slow_query_log` 文件（需 FILE 权限与宿主日志路径，跨主机不可移植），不接受调用方 SQL；失败 hint 指明 `performance_schema=ON` 与 SELECT 授权要求
- chore: `method_capabilities` 增加 `db-mysql.slowlog: system:read`；`tests/fault` 故障矩阵同步

## 0.3.1 (2026-11-23)

- feat: 声明方法级能力 `method_capabilities`——`config.get` / `instances` / `databases` / `query` / `status` 为 `system:read`，`config.set` / `createDatabase` / `dropDatabase` / `createUser` / `dropUser` / `grant` / `export` / `import` 为 `system:admin`；内核按方法取用，只读方法不再被当成 admin 类
- refactor: 审计口径随方法级能力收口——`db-mysql.instances` 等只读方法 10 次调用审计增量 0 条（此前 10 条），写方法仍 1 条；单用户模式下授权仍全放行，方法级能力只在审计侧生效
- docs: 只读/写方法清单与内核审计口径见 `kore` 仓 `docs/PLUGIN-PROTOCOL.md` §3.1.2.1

## 0.3.0 (2026-11-23)

- feat: L2 日常管理能力——`db-mysql.createDatabase`（`CREATE DATABASE IF NOT EXISTS`，默认 `utf8mb4`/`utf8mb4_unicode_ci`）、`db-mysql.dropDatabase`、`db-mysql.createUser`、`db-mysql.dropUser`、`db-mysql.grant`（7 个有真实副作用的方法/方法对）
- feat: `db-mysql.export` 调用 `mysqldump` 导出到白名单目录，`db-mysql.import` 用 stdin 把 SQL 文件交给 `mysql` 客户端；单文件上限 256 MiB，失败不留半成品
- security: 写操作要求插件声明并被授予 `system:admin`；`dropDatabase`/`dropUser` 必须 `confirm=true`，否则 `E_INVALID` 且对象保持不动
- security: 系统库（`mysql`/`information_schema`/`performance_schema`/`sys`）与 `root` 账号受保护，任何 confirm 下都返回 `E_UNAUTHORIZED`
- security: 库名/用户名/表名/字符集/排序规则统一白名单正则（`[a-zA-Z0-9_]`）+ 反引号包裹；授权权限白名单拒绝 `ALL PRIVILEGES`/`ALL`/`GRANT OPTION`
- security: 密码只出现在经 stdin 传入的 SQL 里，不进 argv/ps；字面量按服务端 `sql_mode` 转义（`NO_BACKSLASH_ESCAPES` 兼容）；响应永不含密码，执行面错误再按请求密码脱敏
- security: import/export 路径白名单走 `ResolveExisting`/`ResolveTarget`（词法 + 符号链接二次归属），拒绝 `..`、根外绝对路径与软链逃逸
- feat: 新增 `KORE_MYSQLDUMP_BIN`（mysqldump 路径）与 `KORE_DB_EXPORT_DIR`（导入导出白名单根，缺省 `<state>/exports`）
- test: 单测覆盖标识符注入、系统库/root 保护、confirm 缺失、权限白名单、路径穿越/软链逃逸、密码不回显与错误脱敏；`tests/fault/` 四类故障扩展到全部 11 个方法
- test: 真实 MySQL E2E 全流程（建库→建用户→授权→导出→导入→不带 confirm 被拒→系统库保护→删除→清理）
- docs: README 补 L2 方法表、安全边界、导入导出规则与真实 MySQL E2E 命令

## 0.2.0 (2026-11-23)

- feat: 新增配置契约 `db-mysql.config.get` / `db-mysql.config.set`（`<KORE_STATE_DIR>/config.json`，0600 原子写）——默认实例 `host/port/user/password/password_env/databases_include_system`，优先级 config.json > 环境变量 > 默认值，保存后同一进程下一次调用即生效
- feat: 默认实例密码可由界面填写并保存在本机 0600 配置文件；`config.get` 只回 `***`，`config.set` 收到 `***` 保留原值；`password_env` 旧路径继续兼容且被直接密码覆盖
- feat: `db-mysql.databases` 未显式传 `include_system` 时使用配置项 `databases_include_system`
- test: 配置优先级、默认实例合并（config 覆盖旧路径的 default）、密码掩码、热生效与非法值不落盘单测
- docs: README 补配置契约与密码来源说明
- feat: 真实 MySQL 连接与只读查询（拒绝写操作）
- refactor: 方法名统一为 db-mysql.<action>，对齐协议域校验
- build: 位级可复现打包（stage chmod + tar --mode 归一、size_bytes 收敛），二进制改嵌 manifest.json，消除同版本 sha 漂移

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
