# Complemento de gestión MySQL/MariaDB（kore-plugin-db-mysql）

## Resumen

Este complemento sondea realmente instancias MySQL/MariaDB, lista bases de datos, ejecuta consultas de solo lectura y lee estados clave; desde L2 añade gestión diaria: crear/borrar base de datos, crear/borrar usuario, permisos, exportar/importar. No incluye ningún driver de MySQL ni binarios mysql/mysqldump (G-17): invoca `mysql` / `mysqldump` del host mediante `os/exec` y solo depende de `kore/pkg/pluginapi`. Si falta el cliente devuelve `E_UNSUPPORTED`, sin degradación silenciosa.

## Superficie del protocolo

Protocolo `kore.plugin.v1`, `lifecycle=lazy`, `depth_level=production`, capacidad `system:admin`, mutuamente excluyente con `db-postgres`. Métodos principales: `instances` / `databases` / `query` / `status` (solo lectura) y métodos de escritura L2 `createDatabase` / `dropDatabase` / `createUser` / `dropUser` / `grant` / `changePassword` / `exec` / `export` / `import`. El contrato de configuración `db-mysql.config.get` / `db-mysql.config.set` describe la instancia predeterminada; las contraseñas solo van por un archivo 0600 o variable de entorno, y `config.get` devuelve únicamente `***`.

## Funcionalidades principales

Los clientes se buscan en PATH como `mysql` / `mysqldump` por defecto; `KORE_MYSQL_BIN` / `KORE_MYSQLDUMP_BIN` permiten rutas absolutas. Prioridad de configuración: `config.json` > entorno > valores de código, guardada en `<KORE_STATE_DIR>/config.json` (escritura atómica 0600) y efectiva en la siguiente llamada del mismo proceso (`restart_required=false`). `db-mysql.exec` usa confirmación de dos niveles: `INSERT/UPDATE/DELETE/REPLACE` solo requieren `confirm=true`, mientras `DROP/TRUNCATE/ALTER/CREATE/...` exigen escribir las dos primeras palabras clave de la sentencia.

## Límite de seguridad

La capacidad de escritura no está abierta por defecto: requiere declarar `system:admin` y una nueva comprobación en ejecución; sin declaración se devuelve `E_UNAUTHORIZED`; borrar base de datos o usuario exige `confirm=true`. Los límites de lectura y escritura están separados estrictamente: `query` mantiene una lista blanca de solo lectura, las escrituras solo existen en métodos de gestión explícitos y las contraseñas viajan por entorno/archivo 0600, nunca en argv, registros ni respuestas. Los errores de sondeo se clasifican: los realmente inalcanzables conservan `reachable:false`, mientras los fallos locales de fork/exec devuelven un `E_UPSTREAM` completo, sin tabla parcial.

## Compilación y verificación

La verificación diaria usa `make check` local (gofmt → go vet → go test -timeout 200s); antes de publicar, `make dist` dos veces con sha256 idéntico. Los casos límite y de concurrencia (p. ej. 8 creaciones/borrados simultáneos) están en `tests/boundary/`; la atribución de errores tiene casos positivos y negativos de autoverificación. Versión 0.5.2; mutuamente excluyente con `db-postgres`: no habilitar la escritura en ambos a la vez.

## Enlaces relacionados

- [源码](https://github.com/kore-pl/kore-plugin-db-mysql)
- [kore 内核与协议](https://github.com/kore-pl/kore)
- [插件索引](https://github.com/kore-pl/kore-plugins)
