# Plugin de gestion MySQL/MariaDB（kore-plugin-db-mysql）

## Aperçu

Ce plugin sonde réellement les instances MySQL/MariaDB, liste les bases, exécute des requêtes en lecture seule et lit les états clés ; depuis L2, il ajoute la gestion courante : créer/supprimer une base, créer/supprimer un utilisateur, les privilèges, l’export/import. Il n’embarque aucun driver MySQL ni binaire mysql/mysqldump (G-17) : il appelle `mysql` / `mysqldump` de l’hôte via `os/exec` et ne dépend que de `kore/pkg/pluginapi`. Un client absent renvoie `E_UNSUPPORTED`, sans repli silencieux.

## Surface du protocole

Protocole `kore.plugin.v1`, `lifecycle=lazy`, `depth_level=production`, capacité `system:admin`, mutuellement exclusif avec `db-postgres`. Méthodes principales : `instances` / `databases` / `query` / `status` (lecture seule) et méthodes d’écriture L2 `createDatabase` / `dropDatabase` / `createUser` / `dropUser` / `grant` / `changePassword` / `exec` / `export` / `import`. Le contrat de configuration `db-mysql.config.get` / `db-mysql.config.set` décrit l’instance par défaut ; les mots de passe passent uniquement par un fichier 0600 ou une variable d’environnement, et `config.get` ne renvoie que `***`.

## Fonctionnalités clés

Les clients `mysql` / `mysqldump` sont cherchés dans le PATH par défaut, avec `KORE_MYSQL_BIN` / `KORE_MYSQLDUMP_BIN` pour des chemins absolus. Priorité de configuration : `config.json` > environnement > valeurs du code, stockée dans `<KORE_STATE_DIR>/config.json` (écriture atomique 0600) et effective à l’appel suivant dans le même processus (`restart_required=false`). `db-mysql.exec` utilise une confirmation à deux niveaux : `INSERT/UPDATE/DELETE/REPLACE` ne demandent que `confirm=true`, tandis que `DROP/TRUNCATE/ALTER/CREATE/...` exigent de saisir les deux premiers mots-clés de l’instruction.

## Périmètre de sécurité

La capacité d’écriture n’est pas ouverte par défaut : elle exige la déclaration `system:admin` et un nouveau contrôle à l’exécution, avec `E_UNAUTHORIZED` si elle manque ; supprimer une base ou un utilisateur demande `confirm=true`. Les frontières lecture/écriture sont strictement séparées : `query` garde une liste blanche en lecture seule, les écritures n’existent que dans les méthodes de gestion explicites, et les mots de passe passent par l’environnement/un fichier 0600, jamais dans argv, les journaux ou les réponses. Les erreurs de sondage sont attribuées : les instances vraiment inaccessibles gardent `reachable:false`, tandis qu’un échec local de fork/exec renvoie un `E_UPSTREAM` complet sans table partielle.

## Compilation et vérification

La vérification quotidienne utilise `make check` en local (gofmt → go vet → go test -timeout 200s) ; avant publication, `make dist` deux fois avec sha256 identique. Les cas limites et de concurrence (ex. 8 créations/suppressions simultanées) sont dans `tests/boundary/` ; l’attribution d’erreurs dispose de cas positifs et négatifs auto-probatifs. Version 0.5.2 ; mutuellement exclusif avec `db-postgres` : ne pas activer l’écriture pour les deux en même temps.

## Liens connexes

- [源码](https://github.com/kore-pl/kore-plugin-db-mysql)
- [kore 内核与协议](https://github.com/kore-pl/kore)
- [插件索引](https://github.com/kore-pl/kore-plugins)
