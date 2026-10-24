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
| `add php\|node\|go\|python\|docker\|redis\|database:postgres\|database:mysql\|database:caimandb` | Instala bajo demanda, ajustado a los recursos (`add --list`) |
| `service list` | Servicios administrados |
| `backup create\|list\|verify\|restore\|prune` | tar.gz + sha256, retención y restauración; `--encrypt` cifra, `--db` incluye PostgreSQL |
| `guard [--interval 30s]` | Autopiloto: reinicia servicios caídos (máx. 5/hora), limpia disco al 90 %, renueva certificados |
| `work /L [ruta]` | Lista un directorio y sale |
| `work [ruta]` | Shell interactivo (ver abajo) |

Opciones globales: `--dry-run` (simula y no guarda estado), `--yes`, `--verbose`.

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
