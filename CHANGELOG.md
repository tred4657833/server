# Cambios tras la revisión de código

## Nuevo: el puerto pedido se conserva aunque la app lo ocupe (redirección NAT)
- Si la app escucha en `0.0.0.0:N`, `add port N` ya no muda la URL a otro número: Nginx escucha en un puerto interno libre y una
  regla `iptables` (PREROUTING, solo tráfico que no es `lo`) desvía el N de la red hacia él. Persistente con la unidad
  `tawanzu-redirect-N.service`; `remove port` y `remove all` la quitan. Volver a ejecutar `add port N` reutiliza el puerto interno.
  Sin systemd o `iptables`, o con `--lan`/`--index`, se mantiene el comportamiento anterior.

## Nuevo: `add port` verifica la URL al terminar
- Pide la URL publicada y dice qué ve: tu app, la página «Welcome to nginx!» (otro sitio la tapa), 502/503/504 (la app no
  responde en 127.0.0.1:N) o 403 (límite por IP), con la pista para arreglarlo.

## Nuevo: detección de puerto equivocado (`add port` y `doctor`)
- `add port N`: si nada escucha en N pero sí en un puerto cercano (±10), propone usarlo como destino de Nginx y sigue publicando N.
  Con `--yes` solo avisa. Una publicación existente cuya app está caída se reemplaza si la nueva sí responde.
- `doctor`: nueva comprobación «Apps publicadas», con el comando sugerido para corregir.
- Puertos TCP (no HTTP) que solo escuchan en 127.0.0.1: aviso de que el firewall no los hace accesibles.

## Nuevo: `remove all`, `add port --index` y `add port` autosuficiente
- `tawanzu remove all`: desinstala Nginx, firewall, Certbot y Fail2ban, quita guardián, copia diaria y ajustes de Tawanzu
  (sysctl, SSH) y reinicia el estado de infraestructura para volver a empezar con `tawanzu init`. No toca proyectos, copias,
  certificados de Let's Encrypt, git, curl ni swap. Pide confirmación; admite `--dry-run`.
- `add port N --index`: publica la app como página principal de la red local (`http://<IP privada>/`, puerto 80, firewall y
  Nginx limitados a la LAN) editando el sitio por defecto de Nginx (`sites-available/default`, con `default_server`): guarda el
  original en `default.tawanzu-orig`, lo habilita, y `remove port 80` lo restaura. Si estaba personalizado pregunta antes; sin
  `sites-available` usa `conf.d`. Si el 80 ya tenía otra app de `add port`, la reemplaza.
- `add port N` ya no exige `init`: instala y arranca Nginx y el firewall si faltan (SSH siempre permitido). Si el firewall no se
  puede preparar, publica igualmente y avisa. Termina con `Listo: http://192.168.x.x:N/`.

## Cambio: `add port` muestra la IP local, no la pública
- La URL que imprime `add port` ya no usa la IP pública de Internet (p. ej. `189.187.106.104`): usa la IP privada real
  del equipo (192.168.x.x, 10.x.x.x o 172.16-31.x.x), detectada en el momento; la principal de salida si es privada.
  No hay ninguna IP escrita a mano. `view --public` y `setup domain` siguen usando la IP pública, que es lo que necesita el DNS.

## Nuevo: `tawanzu update`
- Descarga el zip de GitHub (`main`), lo extrae sin permitir rutas que escapen del destino, renombra `server-main` a `server`,
  y ejecuta `make build` y `make install`. Trabaja en `<home>/update` y lo borra al terminar (`--keep` lo conserva).
  Probado de punta a punta contra el repositorio real.

## Nuevo: `add port` publica todo en un paso
- `add port N` ahora configura Nginx (proxy a `127.0.0.1:N`, `limit_conn`/`limit_req` por IP con 429, keep-alive
  hacia la app, timeouts, cabeceras, bloqueo de dotfiles, 503 legible si la app no responde), abre solo el puerto
  público en el firewall y lo registra (`published` en `state.json`). Antes solo abría el firewall.
- **"Welcome to nginx!" al entrar por la IP**: `init` y `add port` desactivan `sites-enabled/default` si es el de
  fábrica (el archivo en `sites-available` se conserva; uno personalizado no se toca).
- Detecta cómo escucha la app (`/proc/net/tcp*`): en loopback → mismo número público; en `0.0.0.0` → otro puerto
  público para no exponerla sin Nginx. `--public`, `--from` (firewall + `allow/deny` en Nginx) y `--raw` (comportamiento anterior).
- `add port N --lan`: publica en `http://<IP privada>/` (puerto 80) solo para la red local; firewall limitado a la LAN.
- `remove port <público>` deshace Nginx y firewall. Reversión automática si `nginx -t` o la recarga fallan.
- Probado con Nginx 1.24 real (proxy, 403 en dotfiles, 503 con app caída, 429 bajo ráfaga de 200 peticiones, convivencia
  de `127.0.0.1:N` y `IP:N`) y pruebas unitarias. **Sin probar** contra ufw/firewalld/systemd reales: úsalo primero en un VPS desechable.

## Nuevo: `tawanzu view` (IP local, pública y web)
- `view --local | --public | --web` (sin banderas, las tres; `--json` disponible). Consulta siempre en vivo y guarda la
  última IP vista (`last_ips` en `state.json`, con fusión entre procesos): si cambió, lo avisa, lo anota en el historial
  y en `--web` marca los dominios que aún apuntan a la IP anterior (`tawanzu add dns <dominio>` los corrige).
- `PublicIP()` ahora consulta 3 servicios a la vez, sin conexiones reutilizadas ni caché, y exige IPv4 válida; toma la
  IP en la que coinciden dos servicios. Lo usan también `init`, `domain add`, `add dns` y `doctor`.
- La IP local ignora interfaces de Docker/veth/puentes y la principal es la de la ruta de salida.

## Nuevo: DNS multi-proveedor (`add dns`, `setup domain`)
- `add dns <host>` funciona con cualquier dominio: detecta el proveedor por los NS (Cloudflare, DigitalOcean,
  GoDaddy, Hostinger), acepta `--provider`, y para cualquier otro ejecuta `TAWANZU_DNS_CMD` o muestra el registro
  a crear a mano. Solo toca el registro A de ese nombre; confirma antes de reemplazar uno distinto, rechaza si hay
  CNAME o varios A. Zonas `.com.mx`, `.co.uk`... detectadas (o `--zone`). Los tokens nunca se imprimen.
- `setup domain <host> --port N` encadena DNS + espera de propagación + `domain add` (Nginx y HTTPS). No abre N en el firewall.
- Probado con servidores HTTP simulados; sin probar contra las APIs reales (no hay tokens en el entorno de desarrollo).

## Nuevo: `tawanzu add port`
- `add port <n...> [--from IP|CIDR]` abre uno o varios puertos (`1555`, `8080/udp`, `8000-8010`, `80,443`) en ufw o
  firewalld, valida el rango 1-65535, avisa si el puerto lo usa un proyecto tras Nginx y los guarda en `state.json`
  (`ports`, con fusión entre procesos). Pruebas en `internal/security` e `internal/state`.

## Cuarta pasada: `00-tawanzu.conf` (Nginx)
- **`server_tokens off;` / `gzip on;` duplicados.** Si `nginx.conf` ya los tenía activos en `http { }`
  (típico en Debian/Ubuntu), `nginx -t` fallaba con "directive is duplicate" y la instalación se revertía.
  Ahora `baseSnippet` omite las directivas que `nginx.conf` ya define (deja un comentario en su lugar).
- **Archivo en blanco.** `ensureRateZone` daba por bueno cualquier `00-tawanzu.conf` existente, incluso vacío,
  y los sitios proxy fallaban por falta de la zona `tawanzu_req`. Ahora lo regenera si no contiene la zona.
- **Reversión segura.** Si `nginx -t` fallaba, se borraba el archivo aunque existiera antes; ahora se restaura su copia.

## Tercera pasada: hallazgos de la revisión con compilador
Esta pasada **sí se compiló y probó** (Go 1.22: `go build`, `go vet`, `gofmt` y `go test -race ./...` limpios).
Sigue sin probarse en un VPS real con systemd: haz antes una prueba en un servidor desechable.

- **`state.json` ya no se pisa entre procesos.** `Save()` toma un `flock` (`state.lock`), relee el disco y
  fusiona solo lo que ese proceso cambió desde `Load()` (fusión de tres vías: altas, cambios y borrados).
  El guardián y la CLI pueden convivir. Pruebas en `internal/state`.
- **Backups que no mienten.** Los errores de lectura/escritura ya no se ignoran: si algo no se pudo copiar,
  `backup create` avisa de cada problema y termina con error ("copia INCOMPLETA"), sin podar copias buenas.
  Un archivo que cambia mientras se copia ya no deja el tar corrupto. Solo se excluye el directorio de
  copias (antes, cualquier ruta con `/backups/`). El volcado de PostgreSQL va a un archivo temporal en vez
  de a memoria, y si se pidió `--db` y falla, la copia cuenta como incompleta.
- **Backups cifrados (opcional).** `backup create --encrypt` o `TAWANZU_BACKUP_ENCRYPT=1`: AES-256-GCM por
  fragmentos, detecta archivos alterados o truncados. La clave se crea en `<home>/backup.key`
  (`TAWANZU_BACKUP_KEY` para cambiarla). **Guárdala fuera del servidor**: sin ella no se puede restaurar.
- **Copia fuera del servidor.** `TAWANZU_BACKUP_UPLOAD` ejecuta un comando (`sh -c`) tras verificar la copia,
  con `TAWANZU_BACKUP_FILE` y `TAWANZU_BACKUP_SUM`. El timer diario lee `/etc/tawanzu/backup.env` y añade
  `--db` si hay PostgreSQL instalado.
- **Datos de las apps en los backups.** Además de `shared/`, se respalda `<proyecto>/data`. Las apps reciben
  `DATA_DIR` apuntando ahí: guarda en ese directorio uploads, SQLite, etc.
- **Aislamiento entre proyectos.** Cada proyecto Node/Go/Python usa su propio usuario (`tawanzu-<nombre>`),
  el directorio del proyecto es `0750` y los releases no son legibles por otros. Los proyectos ya
  desplegados siguen con el usuario `tawanzu` hasta que repitas `tawanzu project setup <nombre>`.
  Docker no se puede aislar así: `setup` lo advierte (equivale a root).
- **systemd.** `%` y `$` en `start` y en variables de entorno se escapan (`%%`, `$$`); antes un `%` dejaba la
  unidad inválida.
- **Salud real.** El guardián y el deploy comprueban que la app responda HTTP, no solo que el puerto esté abierto.
- **Firewall/Docker/fail2ban/puertos.** `setup` y `deploy` avisan si un compose publica puertos en todas las
  interfaces (Docker salta ufw; usa `127.0.0.1:puerto:puerto`). fail2ban protege todos los puertos SSH
  detectados. Un `port:` fijado en `tawanzu.yml` se comprueba contra otros proyectos, dominios y puertos en uso.
- **Pruebas nuevas** para `security`, `guard`, `brain` y `modules` (antes sin ninguna), además de `state`,
  `backup`, `services`, `projects` y `system`. `doctor` sigue sin pruebas. `gofmt` aplicado a `backup_test.go`.

Cambios hechos **sin compilador disponible** (revisión manual + comprobación de balance sintáctico).
Antes de usar en un servidor: `make test && make release`.

## Corregido (segunda pasada: fallos críticos)
- **Bloqueo por firewall.** Solo se permitía UN puerto SSH, detectado sin mirar `sshd_config.d/` ni
  `sshd -T`. Si SSH escuchaba en otro puerto y `SSH_CONNECTION` no llegaba (p. ej. bajo `sudo`),
  `ufw enable` dejaba al usuario fuera. Ahora se permiten TODOS los puertos SSH conocidos, el plan
  `init --plan` los muestra y las reglas `allow` se aplican antes de `default deny`.
- **Los backups de certificados quedaban inservibles.** `/etc/letsencrypt/live/*/*.pem` son enlaces
  simbólicos y se omitían: al restaurar, Nginx no encontraba los certificados y dejaba de arrancar.
  Ahora se guardan y restauran los enlaces.
- **`backup restore` escribía en cualquier ruta del archivo.** Ahora solo en las rutas que Tawanzu respalda
  (Nginx, Let's Encrypt, fail2ban, sshd_config.d, unidades `tawanzu-*`, estado y proyectos); los enlaces
  que apunten fuera se omiten, y no se escribe a través de enlaces existentes. Tras restaurar se ejecuta `nginx -t`.
- **El volcado de PostgreSQL fallaba en silencio** (usaba `sudo`, que puede no existir, y mezclaba stderr
  con el SQL). Ahora usa `runuser`, solo stdout, y avisa si la copia NO incluye la base de datos.
- **`state.json` corrupto se sobrescribía con un estado vacío** (se perdían proyectos y dominios).
  Ahora se conserva como `state.json.corrupt-<fecha>` y se avisa.
- **`work rm -rf` esquivable** con un enlace simbólico (`/enlace/etc`). Ahora se resuelven los enlaces de
  los directorios padre, y tampoco se borran recursivamente los subdirectorios directos de `/`, `/etc`,
  `/var`, `/usr`, `/home`…
- **Repetir `domain add` perdía el HTTPS** ya configurado. Ahora se reinstala el certificado existente
  con `certbot install` (sin volver a validar DNS).

## Corregido (primera pasada)
- **runner.Restore podía borrar archivos de configuración.** `WriteFile` devolvía `""` tanto para
  "archivo nuevo" como para "sin cambios"; si `nginx -t` fallaba por otro motivo, `Restore` eliminaba
  `/etc/nginx/nginx.conf` (o el sitio). Ahora devuelve `runner.NoChange` y `Restore` no toca nada.
- **`init` podía dejar sin acceso a root.** `PermitRootLogin prohibit-password` solo se aplica si
  `/root/.ssh/authorized_keys` existe y no está vacío.
- **El guardián no podía renovar certificados.** Su unidad systemd llevaba `ProtectSystem=full`
  (`/etc` de solo lectura). Nueva opción `Unit.NoHarden`, usada por `tawanzu-guard`.
- **El guardián podía agotar los límites de Let's Encrypt.** Si `certbot renew` fallaba, lo reintentaba
  cada 30 s. Ahora, como máximo una vez cada 6 h por dominio.
- **Despliegues como root: el build no podía escribir su caché.** `HOME` apuntaba a un directorio de
  root. Ahora usa `<proyecto>/cache` con el dueño correcto. Se usa `runuser` (util-linux) en lugar de
  `sudo`, que no viene en Debian mínimo (queda `sudo` como alternativa).
- **Proyectos Docker colisionaban.** Todos usaban imagen y contenedor `tawanzu-app`. Ahora
  `tawanzu-<proyecto>` (marcador `${NAME}`), y `docker compose -p tawanzu-<proyecto>`.
- **Primer deploy fallido dejaba `current` colgante.** Ahora se elimina.
- **CaimanDB marcaba el módulo como instalado aunque faltara el binario**, y repetir el comando no hacía
  nada. Ahora devuelve error. Sin `TAWANZU_CAIMANDB_EXEC`, la unidad se escribe pero no se habilita.
  Se comprueba el `chown` y que el servicio quede activo.
- **Nginx:** los sitios estáticos bloquean archivos ocultos; los sitios proxy ya no fallan si no se
  ejecutó `init` (la zona `limit_req` se crea si falta); PHP funciona en Fedora/RHEL (sin
  `snippets/fastcgi-php.conf`, socket en `/run/php-fpm/`).
- `domain add` avisa si el dominio tiene registro AAAA (Let's Encrypt lo usa para validar).

## Pendiente / conocido
- `ufw`/`firewalld` cierran también otros puertos públicos que ya usaran tus servicios (el plan lo muestra).
- `tawanzu.yml` ejecuta `build` con `sh -c` (como root en proyectos Docker): solo despliega repos de confianza.
- `dist/` no se regeneró en esta revisión.
- Los proyectos Docker no tienen aislamiento por usuario (Docker equivale a root).
- `doctor` no tiene pruebas automáticas y nada se ha probado aún en un VPS real con systemd.
