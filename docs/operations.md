# Operación y diagnóstico local

Dársena ofrece una demo local sin autenticación y un modo local experimental explícito con sesión de propietario y creación de servicios aislados. El modo experimental está bajo revisión y no es apto para producción. Mantén ambos en loopback, los sockets y bases en directorios privados, y la identidad age y la credencial de propietario separadas y protegidas. Las rutas de salud no revelan rutas, identidades ni valores de configuración.

## Salud

- `GET /api/v1/health/live` responde `200` mientras el servidor atiende peticiones. `GET /api/v1/health/ready` responde `200` si la demo está lista y, cuando se configura un worker, este responde preparado; devuelve `503` en caso contrario. El campo `worker` es `disabled`, `ready` o `unavailable`.
- `GET /api/v1/capabilities` responde `200` con `Cache-Control: no-store` y categorías operativas: en demo, `mode` es `demo` y `real_execution` indica ejecución deshabilitada; en modo experimental informa la activación y disponibilidad declarada del proveedor. `worker.configured` indica si se ha configurado el socket, mientras `worker.status` indica `disabled`, `ready` o `unavailable`. `state` y `vault` indican `disabled`, `ready`, `unavailable` o `unknown`. Si el worker no responde o su respuesta no es válida, su estado es `unavailable` y ambos almacenes quedan `unknown`; eso no demuestra que el vault esté deshabilitado. En experimental se consulta el preflight del daemon: disponibilidad no garantiza que una imagen exista ni certifica aislamiento efectivo.
- Por el socket privado del worker, `GET /livez` responde `200` mientras atiende peticiones. `GET /readyz` comprueba que se pueden consultar las tablas de estado y, si está habilitado, del vault. Devuelve `503` si falla alguna. Sus campos `state` y `vault` indican `ready`, `unavailable` o, para el vault, `disabled`.
- Las comprobaciones están limitadas a un segundo y no descifran ni verifican todos los registros. Una respuesta lista no garantiza la integridad completa de las bases. Las rutas anteriores `/api/v1/status` y `/health` se mantienen.

Por ejemplo, con los valores de tu instalación:

```sh
curl --fail http://127.0.0.1:7300/api/v1/health/ready
curl --unix-socket /ruta/privada/worker.sock http://worker/readyz
```

Si el servidor está vivo pero el worker no está disponible, comprueba que el proceso esté activo, que ambos usen el mismo socket y que el directorio sea privado. Si el socket queda tras una parada anormal, verifica primero que no haya un worker activo y sólo entonces retira el socket obsoleto. No borres las bases para resolver un error de arranque.

Si falla la apertura del almacén cifrado, comprueba permisos y que la identidad sea la original. Si el estado o el vault son inválidos, detén el worker, conserva una copia de las bases y sus archivos auxiliares junto con la identidad protegida y utiliza una copia de seguridad verificada. No sustituyas la identidad ni elimines datos automáticamente: hacerlo puede impedir recuperar los secretos.

## Servicio aislado experimental

Para el arranque con `--experimental`, la identidad de instalación estable, el socket del daemon rootless dedicado, las bases privadas y la credencial del propietario, consulta [README](../README.md) y [runtime](runtime.md). El servidor y el worker deben compartir la identidad de instalación; el origen HTTP debe coincidir exactamente con el listener loopback. Las sesiones se mantienen en memoria y terminan al reiniciar. La cookie sola no autoriza consultas: el cliente usa además un token por sesión. Mantén privado el socket del worker; su acceso local da autoridad sobre el worker y no sustituye la protección HTTP.

Una aplicación admite una operación inicial: utiliza una imagen local por digest y una revisión de configuración (si no hay variables, se guarda una revisión vacía al crear). La imagen se comprueba antes de persistir nada (`image_missing`, `image_incompatible`, `runtime_unavailable`); el formulario permite descargar imágenes públicas por digest sin credenciales, una a la vez y con un límite de 10 minutos. El estado de una descarga vive en memoria del worker: tras reiniciar, la disponibilidad se vuelve a leer del daemon. Los registros `image_pull_started`, `image_pull_completed` e `image_pull_failed` no incluyen la referencia. El puerto es metadato, no se publica; no hay red ni URL pública. La interfaz permite parar el servicio y eliminar el contenedor detenido mediante confirmación; la definición y su configuración se conservan. No hay recreación ni rotación automática. Las acciones se persisten antes de enviarse al daemon; un resultado incierto requiere consultar el estado, sin reenviar la acción a ciegas. La recuperación genérica de intenciones permanece separada y no recupera estas acciones automáticamente. Ante limpieza manual, verifica instalación, etiquetas de propiedad e ID completo y limita la operación al recurso revisado; no ejecutes limpieza global. El administrador del daemon puede inspeccionar variables de entorno de contenedores.

### Arranque en una máquina dedicada de OrbStack

`scripts/orbstack.sh` prepara y gestiona el modo experimental en una máquina Linux dedicada de OrbStack (ver [README](../README.md)). Los procesos se ejecutan como unidades de usuario de systemd `darsena-worker` y `darsena-server`; `down` solo para esas unidades y nunca procesos ajenos. Los datos viven en `~/darsena/data` (0700) y nunca se borran ni se sobrescriben; cada compilación sustituye `~/darsena/app` y conserva la anterior en `app.previous`. Los registros se escriben en `~/darsena/log` y se rotan al arrancar si superan 10 MiB (se conserva un archivo anterior).

- Puerto ocupado en el Mac o en la máquina: el script indica qué lo ocupa; elige otro con `DARSENA_ORB_PORT`.
- Socket del worker obsoleto: si nadie escucha en él, `up` lo elimina y lo indica; si otro proceso escucha, se detiene sin tocarlo.
- Daemon no disponible: comprueba `systemctl --user status docker` en la máquina. El socket configurado no se cambia automáticamente; cambiar de daemon dejaría sus contenedores sin gestionar.
- Si falta la identidad de instalación o la identidad age pero existen bases, el script se detiene: restaura los archivos desde la copia en lugar de generar otros.
- Si el servidor no llega a arrancar, se paran ambos procesos y se muestran las últimas líneas del registro.

## Registros

El proceso escribe JSON en stderr con fecha UTC, nivel y componente. Cada petición al servidor recibe un `X-Request-ID` generado por el servidor; `http_request` registra ese identificador, el patrón de ruta, el estado y la duración al terminar la respuesta. Las conexiones SSE se registran al cerrarse. Cuando la respuesta del worker incluye su propio identificador, el registro del servidor lo relaciona mediante `worker_request_id`; las cabeceras que envía el cliente no se consideran identificadores fiables. Los eventos `job_enqueued`, `job_claimed`, `job_completed`, `job_completion_failed` y `job_interrupted` comparten `job_id`; el primero incluye un `request_id` propio del worker, que puede correlacionarse con el servidor mediante `worker_request_id`. Las llamadas directas al socket lo reciben en `X-Request-ID`; a través del servidor, la respuesta contiene el identificador del servidor. `jobs_interrupted` resume el número de trabajos recuperados al arrancar. Un identificador persistido inválido se omite del registro. Consulta `GET /jobs/{id}` por el socket para conocer el estado de un trabajo interrumpido y examina posibles efectos externos antes de decidir si repetirlo; no se reintenta automáticamente. La actualización de SQLite y la emisión de logs no son atómicas: una interrupción entre ambas puede dejar un evento sin emitir. Los trabajos demo son simulados y los registros de la interfaz son distintos de estos registros operativos. No se incluyen cuerpos, variables, claves de idempotencia ni identificadores de recurso.

La aplicación no rota archivos de registro. Configura el supervisor para limitar tamaño y retención; por ejemplo, con Docker y el controlador `local`, `--log-opt max-size=10m --log-opt max-file=3`. Protege el acceso a los registros y aplica la política de retención de tu entorno. Este ejemplo sólo regula los registros del proceso y no convierte la demo en un servicio de producción.

## Límites actuales

El worker procesa trabajos simulados de uno en uno y sondea cada 100 ms. Cada base usa una conexión SQLite y espera hasta 5 segundos ante bloqueo; readiness dispone de un segundo. El cuerpo de creación de trabajos admite 4096 bytes y el de configuración y proxy 100000 bytes. Los listados devuelven hasta 100 trabajos o 500 aplicaciones. El vault admite hasta 200 variables por configuración, valores de hasta 65536 bytes y documentos descifrados de hasta 16 MiB.

Estos límites de consulta no son cuotas de almacenamiento: no existe purga ni retención automática de trabajos o claves de idempotencia. El vault conserva hasta 20 revisiones cifradas por recurso; esas revisiones pueden conservar valores sustituidos o eliminados. Supervisa el espacio disponible y define copias y conservación para tu entorno; no elimines archivos de una base activa.

### Copia y restauración offline experimental

Detén **todos** los procesos que puedan usar las bases de trabajos, configuración e intenciones antes de comenzar y durante todo el procedimiento. El bloqueo del estado impide la copia mientras otro worker lo mantiene, pero no cubre accesos independientes al vault o a las intenciones, ni la apertura del vault que precede al bloqueo durante el arranque. Las instantáneas de distintas bases no comparten una transacción; el procedimiento solo es coherente cuando todos los usuarios permanecen detenidos. Usa directorios privados, bajo control exclusivo del operador, y rutas sin enlaces. Las comprobaciones de rutas no son atómicas frente a cambios concurrentes.

Desde la raíz del repositorio, con el directorio padre privado ya existente y una ruta de destino que aún no exista:

```sh
go run ./cmd/darsena-backup create --state /ruta/privada/state.db --vault /ruta/privada/config.db --destination /ruta/privada/copia-nueva
```

`--vault` se omite si no hay almacén cifrado; `--intent /ruta/privada/intent.db` incluye el almacén de intenciones experimental, incluidas sus acciones de parada y eliminación en el esquema v2. La validación de copias admite el estado de trabajos en las versiones 2 y 3 del esquema, el almacén cifrado en las versiones 0 y 1 y el de intenciones en v1 y v2, y comprueba las columnas de cada versión. Una copia antigua (estado v2, vault v0) se restaura tal cual; al abrirla con el binario actual se migra de forma irreversible (proyectos y versión en las aplicaciones; fechas en el vault, que quedan desconocidas para las revisiones previas). Un binario anterior rechaza las bases migradas, así que conserva una copia previa antes de actualizar. El manifiesto se escribe al final: una copia sin él está incompleta. Los archivos son instantáneas SQLite autocontenidas que incluyen lo confirmado en WAL. La suma SHA-256 detecta corrupción accidental, pero no autentica una copia cuyo contenido y manifiesto pueda modificar un tercero.

La identidad age **no está incluida**. Consérvala por separado en un lugar seguro y verifica periódicamente su recuperación: sin la identidad original no se pueden descifrar las variables. Para restaurar sin sobrescribir datos existentes, con todos los procesos aún detenidos:

```sh
go run ./cmd/darsena-backup restore --source /ruta/privada/copia-nueva --destination /ruta/privada/restauracion-nueva
```

El destino contiene `state.db` y, si estaban incluidos, `vault.db` e `intent.db`. La restauración verifica hashes, integridad SQLite, versiones y columnas principales, pero no autentica el manifiesto ni descifra el vault. Comprueba la apertura con la identidad original y revisa los datos restaurados antes de configurar el worker con las nuevas rutas. Los trabajos que estaban en ejecución pasan a interrumpidos al arrancar; no se reenvían automáticamente. No borres los originales hasta verificar la recuperación. Si la operación falla, conserva los originales y trata el directorio de copia sin manifiesto como incompleto.
