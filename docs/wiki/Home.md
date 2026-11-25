# MySQL/MariaDB 管理插件（kore-plugin-db-mysql）

kore-plugin-db-mysql 是 kore 的 `db-mysql` 插件，独立仓库、独立版本线、独立 CI。当前版本 `0.5.2`，协议 `kore.plugin.v1`。

## 这是什么

本插件真实探测 MySQL/MariaDB 实例、列库、执行只读查询、读取关键状态；L2 起提供日常管理——建库/删库、建用户/删用户、授权、导入导出。不引入任何 MySQL driver、不内置 mysql/mysqldump 客户端二进制（G-17）：通过 `os/exec` 调用宿主机的 `mysql` / `mysqldump`，插件只依赖 `kore/pkg/pluginapi`。客户端缺失返回 `E_UNSUPPORTED`，不静默降级。

## 快速入口

- [安装](Installation)：插件包获取与安装步骤
- [配置](Configuration)：配置项、能力声明与环境变量
- [使用方法](Usage)：协议方法与典型调用
- [常见问题](FAQ)：边界、权限与降级行为
- [变更记录](Changelog)：语义化版本历史

## 相关链接

- 源码：https://github.com/kore-pl/kore-plugin-db-mysql
- 内核与协议：https://github.com/kore-pl/kore
- 索引：https://github.com/kore-pl/kore-plugins
