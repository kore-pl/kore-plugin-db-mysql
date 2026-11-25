# MySQL/MariaDB management plugin（kore-plugin-db-mysql）

## Overview

This plugin really probes MySQL/MariaDB instances, lists databases, runs read-only queries and reads key status; from L2 it adds daily management — create/drop database, create/drop user, grants, export/import. It brings no MySQL driver and bundles no mysql/mysqldump binaries (G-17): it calls the host’s `mysql` / `mysqldump` via `os/exec`, depending only on `kore/pkg/pluginapi`. A missing client returns `E_UNSUPPORTED`, with no silent fallback.

## Protocol surface

Protocol `kore.plugin.v1`, `lifecycle=lazy`, `depth_level=production`, capability `system:admin`, mutually exclusive with `db-postgres`. Core methods: `instances` / `databases` / `query` / `status` (read-only) and L2 write methods `createDatabase` / `dropDatabase` / `createUser` / `dropUser` / `grant` / `changePassword` / `exec` / `export` / `import`. The config contract `db-mysql.config.get` / `db-mysql.config.set` describes the default instance; passwords travel only through a 0600 config file or environment, and `config.get` returns `***` only.

## Core capabilities

Clients are found on PATH as `mysql` / `mysqldump` by default, with `KORE_MYSQL_BIN` / `KORE_MYSQLDUMP_BIN` overriding absolute paths. Config precedence is `config.json` > environment > code defaults, stored in `<KORE_STATE_DIR>/config.json` (0600 atomic write) and effective on the next call in the same process (`restart_required=false`). `db-mysql.exec` uses two-level confirmation: `INSERT/UPDATE/DELETE/REPLACE` need only `confirm=true`, while `DROP/TRUNCATE/ALTER/CREATE/...` require typing the statement’s first two keywords.

## Security boundary

Write capability is not open by default: it requires a `system:admin` declaration plus a runtime re-check, with `E_UNAUTHORIZED` when absent; dropping a database or user requires `confirm=true`. Read and write boundaries are strictly separated — `query` keeps a read-only whitelist, writes live only in explicit management methods, and passwords travel via environment/0600 files, never argv, logs or responses. Probe errors are split by attribution: truly unreachable instances keep `reachable:false`, while local fork/exec failures return a whole `E_UPSTREAM` without a partial table.

## Build and verification

Daily verification uses local `make check` (gofmt → go vet → go test -timeout 200s); before release run `make dist` twice and confirm identical sha256. Boundary and concurrency cases (e.g. 8-way concurrent create/drop) live in `tests/boundary/`; error attribution has positive and negative self-proving cases. Version 0.5.2; mutually exclusive with `db-postgres` — do not enable write capability for both at once.

## Related links

- [源码](https://github.com/kore-pl/kore-plugin-db-mysql)
- [kore 内核与协议](https://github.com/kore-pl/kore)
- [插件索引](https://github.com/kore-pl/kore-plugins)
