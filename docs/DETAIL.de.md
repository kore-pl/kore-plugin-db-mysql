# Plugin zur MySQL/MariaDB-Verwaltung（kore-plugin-db-mysql）

## Überblick

Dieses Plugin untersucht tatsächlich MySQL/MariaDB-Instanzen, listet Datenbanken, führt schreibgeschützte Abfragen aus und liest wichtige Statuswerte; ab L2 kommen tägliche Verwaltungsaufgaben hinzu — Datenbank/Benutzer anlegen/löschen, Berechtigungen, Export/Import. Es bringt keinen MySQL-Treiber und keine mysql/mysqldump-Binaries mit (G-17): es ruft `mysql` / `mysqldump` des Hosts über `os/exec` auf und hängt nur von `kore/pkg/pluginapi` ab. Ein fehlender Client liefert `E_UNSUPPORTED`, ohne stille Herabstufung.

## Protokollfläche

Protokoll `kore.plugin.v1`, `lifecycle=lazy`, `depth_level=production`, Fähigkeit `system:admin`, gegenseitig ausschließend mit `db-postgres`. Kernmethoden: `instances` / `databases` / `query` / `status` (schreibgeschützt) sowie L2-Schreibmethoden `createDatabase` / `dropDatabase` / `createUser` / `dropUser` / `grant` / `changePassword` / `exec` / `export` / `import`. Der Konfigurationsvertrag `db-mysql.config.get` / `db-mysql.config.set` beschreibt die Standardinstanz; Passwörter laufen nur über eine 0600-Datei oder Umgebungsvariablen, und `config.get` gibt nur `***` zurück.

## Kernfunktionen

Clients liegen standardmäßig als `mysql` / `mysqldump` im PATH; `KORE_MYSQL_BIN` / `KORE_MYSQLDUMP_BIN` erlauben absolute Pfade. Die Konfigurationsreihenfolge ist `config.json` > Umgebung > Code-Defaults, gespeichert in `<KORE_STATE_DIR>/config.json` (0600 atomisch) und ab dem nächsten Aufruf im selben Prozess wirksam (`restart_required=false`). `db-mysql.exec` nutzt eine zweistufige Bestätigung: `INSERT/UPDATE/DELETE/REPLACE` benötigen nur `confirm=true`, `DROP/TRUNCATE/ALTER/CREATE/...` erfordern die Eingabe der ersten beiden Schlüsselwörter der Anweisung.

## Sicherheitsgrenze

Schreibrechte sind nicht standardmäßig offen: Sie erfordern eine `system:admin`-Deklaration plus eine Laufzeitprüfung; fehlt sie, gilt `E_UNAUTHORIZED`; das Löschen einer Datenbank oder eines Benutzers verlangt `confirm=true`. Lese- und Schreibgrenzen sind strikt getrennt — `query` behält eine schreibgeschützte Whitelist, Schreibvorgänge existieren nur in expliziten Verwaltungsmethoden, und Passwörter laufen über Umgebung/0600-Dateien, nie in argv, Logs oder Antworten. Sondefehler werden nach Ursache getrennt: wirklich unreachable Instanzen behalten `reachable:false`, lokale fork/exec-Fehler liefern ein gesamtheitliches `E_UPSTREAM` ohne Teilantwort.

## Build und Verifizierung

Die tägliche Prüfung nutzt lokales `make check` (gofmt → go vet → go test -timeout 200s); vor dem Release `make dist` zweimal mit identischem sha256. Grenz- und Nebenläufigkeitsfälle (z. B. 8 parallele Anlegen/Löschen) liegen in `tests/boundary/`; die Fehlerzuordnung hat positive und negative Selbstbeweisfälle. Version 0.5.2; gegenseitig ausschließend mit `db-postgres` — nicht für beide gleichzeitig Schreibrechte aktivieren.

## Verwandte Links

- [源码](https://github.com/kore-pl/kore-plugin-db-mysql)
- [kore 内核与协议](https://github.com/kore-pl/kore)
- [插件索引](https://github.com/kore-pl/kore-plugins)
