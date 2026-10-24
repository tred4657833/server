# Tawanzu Server

Gestor de servidores, infraestructura y proyectos en Go. Un solo binario, **solo librería estándar** (sin dependencias externas).

```bash
make build            # o: go build -o tawanzu ./cmd/tawanzu
sudo make install
sudo tawanzu init --plan   # ver el plan sin tocar nada
sudo tawanzu init          # aplicar
```

Compatible con Debian 13 / Ubuntu Server (apt) y, con menos pruebas, Fedora/RHEL (dnf). Linux únicamente para operar; compila en cualquier SO.

## Comandos

| Comando | Qué hace |
|---|---|
| `init [--plan]` | Detecta el entorno y aplica: swap, sysctl, Nginx, firewall (SSH siempre permitido antes de activar), fail2ban, SSH seguro, parches automáticos, Certbot, backup diario y guardián |
| `status [--watch] [--json] [--top N]` | CPU (por núcleo), RAM, swap, discos e inodos, red en vivo, procesos, temperatura |
| `advise` | Recomendaciones según el estado real y valores ajustados a tu RAM/CPU |
| `doctor` | Diagnóstico concurrente: red, disco, memoria, Nginx, servicios, proyectos y caducidad de certificados |
| `clone name:miapp respo:usuario repositorio` | Clona (también `clone <url>`, `branch:rama`) y detecta el tipo |
| `project list\|inspect\|setup\|deploy\|logs [-f]\|restart\|rollback <nombre>` | Ciclo de vida con releases atómicos y vuelta atrás automática si el despliegue no responde |
| `domain add ejemplo.com` | Pregunta el puerto y si quieres HTTPS, verifica el registro **A**, configura Nginx y emite el certificado |
| `domain list \| remove` | Administrar dominios |
| `view [--local] [--public] [--web] [--json]` | `--local`: IP privada (marca la principal de salida); `--public`: IP pública; `--web`: cada dominio del servidor, qué IP resuelve hoy (DNS público, sin caché local) y si apunta aquí. Sin banderas muestra las tres. Siempre consulta en el momento y **avisa si la IP cambió** desde la última vez (y si un dominio sigue apuntando a la IP anterior) |
| `add dns <host> [--ip IP] [--provider p] [--zone z] [--ttl N]` | Crea/actualiza el registro **A** de cualquier dominio. Detecta el proveedor por los servidores de nombres (Cloudflare, DigitalOcean, GoDaddy, Hostinger) o usa `--provider`; con `TAWANZU_DNS_CMD` ejecuta tu propio comando para cualquier otro. Sin proveedor reconocido, te dice qué registro crear a mano |
| `setup domain <host> --port N [--ip IP] [--provider p] [--no-dns]` | Todo en uno: DNS + Nginx (proxy a `127.0.0.1:N`) + HTTPS. **No abre N en el firewall**: la app sigue privada y se publica por 80/443 |
| `add port <n> [--from IP] [--public P]` | **Todo en uno**: Nginx como proxy a `127.0.0.1:n` (límites de conexiones y peticiones, keep-alive, timeouts, cabeceras), firewall solo para el puerto público, desactiva la página "Welcome to nginx!" y lo registra. Con `--raw`, UDP, rangos (`8000-8010`) o listas, solo abre el firewall |
| `remove port <público>` | Deja de publicarlo (Nginx + firewall) |
| `add php\|node\|go\|python\|docker\|redis\|database:postgres\|database:mysql\|database:caimandb` | Instala bajo demanda, ajustado a los recursos (`add --list`) |
| `service list` | Servicios administrados |
| `backup create\|list\|verify\|restore\|prune` | tar.gz + sha256, retención y restauración; `--encrypt` cifra, `--db` incluye PostgreSQL |
| `guard [--interval 30s]` | Autopiloto: reinicia servicios caídos (máx. 5/hora), limpia disco al 90 %, renueva certificados |
| `update [--keep]` | Descarga `https://github.com/tred4657833/server/archive/refs/heads/main.zip`, lo descomprime, renombra la carpeta a `server`, ejecuta `make build` y `make install`. Necesita `make` y `go`; si la compilación falla no toca la versión instalada. `TAWANZU_UPDATE_URL` cambia el origen |
| `work /L [ruta]` | Lista un directorio y sale |
| `work [ruta]` | Shell interactivo (ver abajo) |

Opciones globales: `--dry-run` (simula y no guarda estado), `--yes`, `--verbose`.

### `add port`
```
sudo tawanzu add port 1555                  # publica la app que corre en 127.0.0.1:1555 (muestra la URL con la IP de tu red local, p. ej. http://192.168.x.x/)
sudo tawanzu add port 1555 --index          # tu app como página principal de la red local: http://192.168.x.x/ (puerto 80); reescribe el sitio por defecto de Nginx
sudo tawanzu add port 1555 --lan            # SOLO red local: http://192.168.x.x/ (puerto 80), nada hacia Internet
sudo tawanzu add port 1555 --from 10.0.0.5  # solo accesible desde esa IP/red (firewall + Nginx)
sudo tawanzu add port 3000 --public 8080    # Nginx escucha en 8080 y reenvía a la app en 3000
sudo tawanzu add port 5432 --raw            # solo firewall, sin Nginx (bases de datos, TCP crudo)
sudo tawanzu add port 8080/udp 8000-8010    # UDP y rangos: solo firewall
sudo tawanzu remove port 1555               # deshacer
```

`add port` no necesita `init` previo: si falta Nginx o el firewall, los instala y los arranca, y al terminar muestra la URL lista
con la IP de tu red local (`http://192.168.x.x:1555/`, o `http://192.168.x.x/` con `--index`).

`--index` edita `/etc/nginx/sites-available/default` (el sitio por defecto de Nginx): lo reemplaza por la app, con `listen <IP privada>:80 default_server`
y acceso limitado a tu red local, y lo habilita en `sites-enabled`. El contenido original queda en `default.tawanzu-orig`; `remove port 80` lo
restaura. Si tu sitio por defecto estaba personalizado, pregunta antes; si tu sistema no usa `sites-available` (RHEL/Fedora), usa un archivo
propio en `conf.d` como el resto de puertos.

Al terminar, `add port` pide la URL publicada y te dice si responde tu app o si algo la tapa (p. ej. la página «Welcome to nginx!»).

### El puerto que escribes es el que usas
`add port 1555` te deja entrar por `http://IP:1555/`. Si la app escucha solo en `127.0.0.1:1555`, Nginx usa ese mismo número en tu IP
local. Si la app ya ocupa el 1555 en **todas** las interfaces (`0.0.0.0`), Nginx no puede escuchar ahí; en vez de mudarse a otro número,
escucha en un puerto interno libre y una regla NAT (`iptables`, unidad `tawanzu-redirect-1555.service`) desvía al interno lo que llega
de la red al 1555. Tu app no se toca ni se mueve, y Nginx la sigue alcanzando por `127.0.0.1:1555`. `remove port` quita la regla. Si el
sistema no tiene systemd o `iptables`, mantiene el comportamiento anterior (publicar en otro puerto y avisar). Solo IPv4.

### Cuando el puerto está mal
- Si pides `add port 1555` pero nada escucha ahí y sí en uno cercano (±10, p. ej. el 1556), `add port` te pregunta si esa es tu app y,
  si aceptas, publica el 1555 apuntando al 1556. Con `--yes` no cambia nada solo: te dice el comando exacto.
- Si ya publicaste un puerto que apunta a una app que no responde, `sudo tawanzu add port 1556 --public 1555` reemplaza esa publicación
  (solo si la anterior está caída y la nueva responde).
- `sudo tawanzu doctor` revisa cada puerto publicado: avisa si su app no responde y sugiere el comando para corregirlo.
- Si la app no habla HTTP (base de datos, TCP propio), no se pone detrás de Nginx: se abre el puerto en el firewall. Si además la app
  escucha solo en `127.0.0.1`, avisa de que abrir el firewall no basta: hay que hacer que escuche en `0.0.0.0` o en tu IP local.

### `remove all`
```
sudo tawanzu remove all     # quita todo lo que instaló Tawanzu para empezar de nuevo con `sudo tawanzu init`
```
Elimina Nginx (y sus puertos/dominios publicados), el firewall (ufw/firewalld, queda desactivado y sin reglas), Certbot, Fail2ban,
el guardián y la copia diaria, los ajustes de Tawanzu en sysctl y SSH, y el estado (módulos, puertos, dominios). **Conserva** tus
proyectos y sus servicios, las copias de seguridad, los certificados de `/etc/letsencrypt`, git, curl, el swap y las
actualizaciones automáticas. Pide confirmación (`--yes` la omite) y admite `--dry-run` para ver qué haría.
Un solo comando deja listo: proxy de Nginx, límites por IP (40 conexiones simultáneas, `tawanzu_req` por segundo con ráfaga de 60; responde 429), keep-alive hacia la app, timeouts contra conexiones lentas, bloqueo de dotfiles (`/.env`, `/.git`), cabeceras de seguridad, `X-Forwarded-For` sin falsificar, firewall (solo el puerto público) y registro en `state.json`. Si Nginx no pasa `nginx -t` o no recarga, se restaura la configuración anterior.

**`--lan`.** Nginx escucha únicamente en las IPs privadas del servidor (192.168.x, 10.x, 172.16-31.x) en el puerto 80 (o el de `--public`), el firewall solo admite la red local (la `/24` de cada interfaz, o lo que pongas en `--from`) y la app sigue en su puerto privado. Dos apps no pueden usar el 80: la segunda debe llevar otro `--public`.

**Cómo escucha cada cosa.** Tu app debe escuchar en `127.0.0.1:N`. Nginx escucha en las IPs de la máquina en el mismo número (no chocan: un bind a una IP concreta convive con otro a `127.0.0.1`). Si tu app ya escucha en `0.0.0.0:N` (típico de `app.listen(3000)`), Nginx no puede ocupar el mismo puerto: elige el siguiente libre (o `--public`), deja N cerrado en el firewall y te lo explica. Si nada escucha todavía, configura igual y Nginx responde 503 hasta que arranque la app. Si el puerto está en uso pero no habla HTTP, ofrece abrirlo tal cual.

Es HTTP sin cifrar: para HTTPS usa un dominio (`setup domain`). Necesita el firewall instalado (`tawanzu init`, que deja SSH permitido). Los puertos 80/443 y SSH se rechazan. Prueba antes con `--dry-run`.

### DNS: proveedores y tokens
| Proveedor | Token (variable o `<home>/<proveedor>.token`, 0600) |
|---|---|
| Cloudflare | `TAWANZU_CLOUDFLARE_TOKEN` (API token con permiso *Zone · DNS · Edit*) |
| DigitalOcean | `TAWANZU_DIGITALOCEAN_TOKEN` |
| GoDaddy | `TAWANZU_GODADDY_TOKEN` con formato `CLAVE:SECRETO` (GoDaddy limita su API a cuentas con varios dominios) |
| Hostinger | `TAWANZU_HOSTINGER_TOKEN` |
| Otro | `TAWANZU_DNS_CMD`: se ejecuta con `sh -c` y recibe `TAWANZU_DNS_HOST`, `_NAME`, `_ZONE`, `_IP`, `_TTL` |

Si no hay token, `add dns` lo pide por terminal y lo guarda. En Cloudflare se conserva el estado *proxied* de un registro existente (uno nuevo se crea sin proxy).

### `domain add`
```
$ sudo tawanzu domain add ejemplo.com
Puerto del proyecto [8080]: 8080
¿Configurar HTTPS automáticamente? [S/n]: s
```
Antes crea un registro DNS tipo `A` de `ejemplo.com` hacia la IP pública del servidor. Si el DNS aún no apunta, deja Nginx en HTTP y te avisa; repite el comando con `--https` cuando propague. Si `www.ejemplo.com` también apunta, se incluye en el certificado.

### `work` — explorador de archivos
`/L` (o `ls -a`), `cd`, `pwd`, `cat`, `view` (paginado con líneas), `head`, `tail -n N`, `edit` (usa `$EDITOR`/nano/vim; si no hay, editor integrado), `mkdir`, `touch`, `cp`, `mv`, `chmod`, `rm [-r] [-f]`, `clean` (cachivos: tmp, bak, cachés, logs de +30 días), `find`, `grep`, `du`, `tree`, `stat`. Las rutas del sistema (`/`, `/etc`, `/usr`, `/var`, `/home`…) están protegidas contra borrado, y todo borrado pide confirmación mostrando cuánto se elimina. `find`, `grep`, `du`, `cp` y `clean` usan pools de goroutines.

### Declarar cómo arrancar un proyecto
Autodetecta Go, Node, PHP, Python, Docker y estático. Para casos propios, añade `tawanzu.yml` (ver `configs/tawanzu.yml.example`) o un `Procfile` con `web:`.

### Módulos externos
Cualquier JSON en `<home>/modules/*.json` aparece en `tawanzu add --list` (ver `configs/modulo-ejemplo.json`).

## Dónde guarda cosas
Como root: estado en `/var/lib/tawanzu`, proyectos en `/srv/tawanzu/<nombre>/{repo,releases,current,shared}`, copias en `/var/backups/tawanzu`. Se pueden cambiar con `TAWANZU_HOME`, `TAWANZU_PROJECTS` y `TAWANZU_BACKUPS`. Cada archivo de configuración modificado conserva una copia `*.tawanzu.bak.<fecha>`.

### Backups cifrados y copia externa
Variables (en el timer diario, ponlas en `/etc/tawanzu/backup.env`): `TAWANZU_BACKUP_ENCRYPT=1` cifra con AES-256-GCM usando `<home>/backup.key` (**guárdala fuera del servidor**); `TAWANZU_BACKUP_UPLOAD` es un comando que se ejecuta tras verificar la copia, p. ej. `rsync -a "$TAWANZU_BACKUP_FILE" "$TAWANZU_BACKUP_SUM" usuario@otro-host:/backups/`. Si una copia queda incompleta, `backup create` termina con error. Las apps deben guardar sus datos (uploads, SQLite) en `$DATA_DIR` (`<proyecto>/data`) para que entren en la copia.

## Estructura
```
cmd/tawanzu        CLI
internal/system    detección y métricas (/proc), concurrente
internal/tune      cálculo de valores según recursos
internal/brain     plan de init, advise, status
internal/netcfg    Nginx, DNS, TLS, domain add
internal/security  firewall, fail2ban, SSH, sysctl, swap
internal/projects  clone, detección, setup, deploy, rollback
internal/modules   catálogo de herramientas (+ externos JSON)
internal/services  systemd
internal/doctor    diagnóstico
internal/backup    copias verificables
internal/guard     autopiloto
internal/work      explorador de archivos
```

## Notas importantes
- `database:caimandb` prepara usuario y directorio de datos. Si no existe el binario `caimandb` termina con error (y no se marca como instalado), así que puedes repetirlo después. Mientras no definas `TAWANZU_CAIMANDB_EXEC` con el comando real de arranque, la unidad systemd se escribe pero **no se habilita ni arranca**; con la variable definida se habilita, se arranca y se comprueba que quede activo.
- Pruébalo primero con `--dry-run` y en un VPS desechable: `init`, `domain add` y los deploys con systemd/Nginx/Certbot requieren un servidor real, y en el desarrollo se verificaron con simulación, pruebas unitarias y un contenedor sin systemd.
