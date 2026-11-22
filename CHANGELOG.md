# Changelog

## 0.1.0 (2026-11-22)

- feat: 新建 db-mysql 插件骨架，复用 kore.plugin.v1 握手与会话循环；声明生产级深度与 `system:admin` 能力，与 `db-postgres` 互斥
- feat: mysql.instances / mysql.slowQueries 方法级参数校验与结构化结果，实例名与查询参数限定边界，不真连数据库、不内置客户端二进制
- feat: 备份钩子协作契约（向 backup 插件提供实例枚举占位，不内置快照实现）
- feat: 四类故障注入测试（kill -9 / 超时 / 磁盘满 / cgroup 超限），验证方法级错误返回
- build: 确定性打包（--sort=name --mtime=@0 --owner=0 --group=0 --numeric-owner + gzip -9n + chmod 归一）
