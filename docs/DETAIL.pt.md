# Plugin de gerenciamento MySQL/MariaDB（kore-plugin-db-mysql）

## Visão geral

Este plugin sonda realmente instâncias MySQL/MariaDB, lista bancos, executa consultas somente leitura e lê estados importantes; a partir do L2 acrescenta gerenciamento diário — criar/excluir banco, criar/excluir usuário, permissões, exportar/importar. Não inclui driver MySQL nem binários mysql/mysqldump (G-17): chama `mysql` / `mysqldump` do host via `os/exec` e depende apenas de `kore/pkg/pluginapi`. Cliente ausente retorna `E_UNSUPPORTED`, sem degradação silenciosa.

## Superfície do protocolo

Protocolo `kore.plugin.v1`, `lifecycle=lazy`, `depth_level=production`, capacidade `system:admin`, mutuamente exclusivo com `db-postgres`. Métodos principais: `instances` / `databases` / `query` / `status` (somente leitura) e métodos de escrita L2 `createDatabase` / `dropDatabase` / `createUser` / `dropUser` / `grant` / `changePassword` / `exec` / `export` / `import`. O contrato de configuração `db-mysql.config.get` / `db-mysql.config.set` descreve a instância padrão; senhas trafegam apenas por arquivo 0600 ou variável de ambiente, e `config.get` retorna somente `***`.

## Funcionalidades principais

Os clientes `mysql` / `mysqldump` são encontrados no PATH por padrão; `KORE_MYSQL_BIN` / `KORE_MYSQLDUMP_BIN` definem caminhos absolutos. A precedência é `config.json` > ambiente > valores do código, armazenada em `<KORE_STATE_DIR>/config.json` (escrita atômica 0600) e válida na próxima chamada no mesmo processo (`restart_required=false`). `db-mysql.exec` usa confirmação em dois níveis: `INSERT/UPDATE/DELETE/REPLACE` precisam apenas de `confirm=true`, enquanto `DROP/TRUNCATE/ALTER/CREATE/...` exigem digitar as duas primeiras palavras-chave da instrução.

## Limite de segurança

O direito de escrita não é aberto por padrão: exige declaração `system:admin` e nova verificação em tempo de execução; sem ela, `E_UNAUTHORIZED`; excluir banco ou usuário requer `confirm=true`. Os limites de leitura e escrita são rigorosamente separados: `query` mantém lista branca somente leitura, escritas existem apenas em métodos de gerenciamento explícitos, e senhas trafegam por ambiente/arquivo 0600, nunca em argv, logs ou respostas. Erros de sondagem são classificados: instâncias realmente inalcançáveis mantêm `reachable:false`, enquanto falhas locais de fork/exec retornam um `E_UPSTREAM` inteiro, sem tabela parcial.

## Build e verificação

A verificação diária usa `make check` local (gofmt → go vet → go test -timeout 200s); antes do lançamento, `make dist` duas vezes com sha256 idêntico. Casos limite e de concorrência (ex.: 8 criações/exclusões simultâneas) ficam em `tests/boundary/`; a atribuição de erros tem casos positivos e negativos de autoprova. Versão 0.5.2; mutuamente exclusivo com `db-postgres` — não ativar escrita para ambos ao mesmo tempo.

## Links relacionados

- [源码](https://github.com/kore-pl/kore-plugin-db-mysql)
- [kore 内核与协议](https://github.com/kore-pl/kore)
- [插件索引](https://github.com/kore-pl/kore-plugins)
