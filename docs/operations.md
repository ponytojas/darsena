# Local operation and diagnostics

Dársena offers an unauthenticated local demo and an explicit local experimental mode with an owner session and service creation (isolated by default, with optional local access). The experimental mode is under review and is not suitable for production. Keep both on loopback, the sockets and databases in private directories, and the age identity and the owner credential separate and protected. The health routes do not reveal paths, identities or configuration values.

## Health

- `GET /api/v1/health/live` responds `200` while the server is handling requests. `GET /api/v1/health/ready` responds `200` if the demo is ready and, when a worker is configured, it responds as ready; otherwise it returns `503`. The `worker` field is `disabled`, `ready` or `unavailable`.
- `GET /api/v1/capabilities` responds `200` with `Cache-Control: no-store` and operational categories: in demo, `mode` is `demo` and `real_execution` indicates execution is disabled; in experimental mode it reports the activation and the declared availability of the provider. `worker.configured` indicates whether the socket has been configured, while `worker.status` indicates `disabled`, `ready` or `unavailable`. `state` and `vault` indicate `disabled`, `ready`, `unavailable` or `unknown`. If the worker does not respond or its response is not valid, its status is `unavailable` and both stores are `unknown`; that does not prove that the vault is disabled. In experimental mode the daemon preflight is queried: availability does not guarantee that an image exists nor certify effective isolation.
- Over the worker's private socket, `GET /livez` responds `200` while it is handling requests. `GET /readyz` checks that the state tables and, if enabled, the vault can be queried. It returns `503` if any fails. Its `state` and `vault` fields indicate `ready`, `unavailable` or, for the vault, `disabled`.
- The checks are limited to one second and do not decrypt or verify every record. A ready response does not guarantee the full integrity of the databases. The earlier routes `/api/v1/status` and `/health` are kept.

For example, with the values of your installation:

```sh
curl --fail http://127.0.0.1:7300/api/v1/health/ready
curl --unix-socket /private/path/worker.sock http://worker/readyz
```

If the server is alive but the worker is unavailable, check that the process is running, that both use the same socket and that the directory is private. If the socket remains after an abnormal stop, first verify that no worker is active and only then remove the stale socket. Do not delete the databases to resolve a startup error.

If opening the encrypted store fails, check permissions and that the identity is the original one. If the state or the vault is invalid, stop the worker, keep a copy of the databases and their auxiliary files together with the protected identity, and use a verified backup. Do not replace the identity or delete data automatically: doing so may make it impossible to recover the secrets.

## Experimental isolated service

For startup with `--experimental`, the stable installation identity, the dedicated rootless daemon socket, the private databases and the owner credential, see [README](../README.md) and [runtime](runtime.md). The server and the worker must share the installation identity; the HTTP origin must exactly match the loopback listener. Sessions are kept in memory and end on restart. The cookie alone does not authorize queries: the client also uses a per-session token. Keep the worker socket private; local access to it gives authority over the worker and does not replace HTTP protection.

An application supports one initial operation: it uses a local image by digest and a configuration revision (if there are no variables, an empty revision is saved on creation). The image is checked before anything is persisted (`image_missing`, `image_incompatible`, `runtime_unavailable`); the form allows downloading public images by digest without credentials, one at a time and with a limit of 10 minutes. The state of a download lives in the worker's memory: after a restart, availability is read again from the daemon. The `image_pull_started`, `image_pull_completed` and `image_pull_failed` logs do not include the reference. With isolated access (the default) the port is metadata, it is not published, and there is no network or public URL; the "Internal network" and "Open on this computer" access levels are described in the access section of [runtime](runtime.md). The interface allows stopping the service and deleting the stopped container through confirmation; the definition and its configuration are kept. There is no automatic recreation or rotation. Actions are persisted before being sent to the daemon; an uncertain result requires checking the state, without blindly resending the action. Generic intent recovery remains separate and does not recover these actions automatically. For manual cleanup, verify the installation, ownership labels and full ID, and limit the operation to the reviewed resource; do not run global cleanup. The daemon administrator can inspect container environment variables.

### Startup on a dedicated OrbStack machine

`scripts/orbstack.sh` prepares and manages the experimental mode on a dedicated OrbStack Linux machine (see [README](../README.md)). The processes run as the systemd user units `darsena-worker` and `darsena-server`; `down` stops only those units and never foreign processes. The data lives in `~/darsena/data` (0700) and is never deleted or overwritten; each build replaces `~/darsena/app` and keeps the previous one in `app.previous`. Logs are written to `~/darsena/log` and rotated at startup if they exceed 10 MiB (one previous file is kept).

- Port in use on the Mac or on the machine: the script says what is using it; choose another with `DARSENA_ORB_PORT`.
- Stale worker socket: if nobody is listening on it, `up` removes it and says so; if another process is listening, it stops without touching it.
- Daemon unavailable: check `systemctl --user status docker` on the machine. The configured socket is not changed automatically; switching daemons would leave its containers unmanaged.
- If the installation identity or the age identity is missing but databases exist, the script stops: restore the files from the backup instead of generating others.
- If the server does not manage to start, both processes are stopped and the last lines of the log are shown.
- Applications port in use: `scripts/orbstack.sh up --apps-port PORT` uses another (1024 to 65535, different from the interface's); the default is 7321.

### Opening an application

With `--apps-addr 127.0.0.1:<port>` (or `DARSENA_APPS_ADDR`), the server serves `local` applications at `http://<domain>.localhost:<port>`. The listener is off by default; `scripts/orbstack.sh` turns it on with `--apps-port` (7321 by default). If it is off, the interface says so. The domain name must be typed exactly as it appears in Settings, and the browser must request the `.localhost` domain, not `127.0.0.1`.

If the page does not open, the proxy's response indicates the reason:

- `503`: the application's service is stopped. Check its status in the interface.
- `502`: the container is running, but the application does not respond yet on the port of its definition. Dársena does not check the health of applications; wait for it to start or check which port it listens on.
- `404`: the domain does not exist, the application is not published (isolated access) or the `Host` does not exactly match `<domain>.localhost`.

Applications have no authentication: any process on this computer and any website open in the browser can send requests to them.

## Logs

The process writes JSON to stderr with UTC date, level and component. Each request to the server receives a server-generated `X-Request-ID`; `http_request` records that identifier, the route pattern, the status and the duration when the response ends. SSE connections are logged when they close. When the worker's response includes its own identifier, the server's log relates it through `worker_request_id`; headers sent by the client are not considered reliable identifiers. The events `job_enqueued`, `job_claimed`, `job_completed`, `job_completion_failed` and `job_interrupted` share `job_id`; the first includes a `request_id` of the worker's own, which can be correlated with the server through `worker_request_id`. Direct calls to the socket receive it in `X-Request-ID`; through the server, the response contains the server's identifier. `jobs_interrupted` summarizes the number of jobs recovered at startup. An invalid persisted identifier is omitted from the log. Query `GET /jobs/{id}` over the socket to learn the state of an interrupted job and examine possible external effects before deciding whether to repeat it; it is not retried automatically. The SQLite update and the emission of logs are not atomic: an interruption between the two can leave an event unemitted. Demo jobs are simulated and the interface's logs are distinct from these operational logs. Bodies, variables, idempotency keys and resource identifiers are not included.

The application does not rotate log files. Configure the supervisor to limit size and retention; for example, with Docker and the `local` driver, `--log-opt max-size=10m --log-opt max-file=3`. Protect access to the logs and apply your environment's retention policy. This example only regulates the process logs and does not turn the demo into a production service.

## Current limits

The worker processes simulated jobs one at a time and polls every 100 ms. Each database uses one SQLite connection and waits up to 5 seconds on a lock; readiness has one second. The job creation body accepts 4096 bytes and the configuration and proxy bodies 100000 bytes. Listings return up to 100 jobs or 500 applications. The vault accepts up to 200 variables per configuration, values of up to 65536 bytes and decrypted documents of up to 16 MiB.

These query limits are not storage quotas: there is no automatic purge or retention of jobs or idempotency keys. The vault keeps up to 20 encrypted revisions per resource; those revisions may retain replaced or deleted values. Monitor the available space and define backups and retention for your environment; do not delete files from an active database.

### Experimental offline backup and restore

Stop **all** processes that could use the jobs, configuration and intents databases before starting and throughout the procedure. The state lock prevents the backup while another worker holds it, but it does not cover independent access to the vault or the intents, nor the opening of the vault that precedes the lock during startup. Snapshots of different databases do not share a transaction; the procedure is consistent only when all users remain stopped. Use private directories, under the operator's exclusive control, and paths without links. Path checks are not atomic against concurrent changes.

From the repository root, with the private parent directory already existing and a destination path that does not exist yet:

```sh
go run ./cmd/darsena-backup create --state /private/path/state.db --vault /private/path/config.db --destination /private/path/new-backup
```

`--vault` is omitted if there is no encrypted store; `--intent /private/path/intent.db` includes the experimental intents store, including its stop and delete actions in schema v2. Backup validation supports the jobs state in schema versions 2, 3 and 4, the encrypted store in versions 0 and 1 and the intents store in v1, v2 and v3, and checks the columns of each version (state v4 adds `access`, `shared_network` and `domain`; intents v3 adds `access`, `port`, `project`, `shared` and `alias`). An old backup (state v2, vault v0) is restored as is; when opened with the current binary it is migrated irreversibly (projects and version in the applications; dates in the vault, which remain unknown for the earlier revisions; access, common network and domain in the applications and the network fields in the intents, all at their isolated value). An application's domain is kept in the backup and its unique index travels with it. An earlier binary rejects the migrated databases, so keep a prior backup before upgrading. The manifest is written last: a backup without it is incomplete. The files are self-contained SQLite snapshots that include what was committed in the WAL. The SHA-256 sum detects accidental corruption, but does not authenticate a backup whose content and manifest could be modified by a third party.

The age identity is **not included**. Keep it separately in a safe place and periodically verify that it can be recovered: without the original identity the variables cannot be decrypted. To restore without overwriting existing data, with all processes still stopped:

```sh
go run ./cmd/darsena-backup restore --source /private/path/new-backup --destination /private/path/new-restore
```

The destination contains `state.db` and, if they were included, `vault.db` and `intent.db`. Restore verifies hashes, SQLite integrity, versions and main columns, but does not authenticate the manifest or decrypt the vault. Check that it opens with the original identity and review the restored data before configuring the worker with the new paths. Jobs that were running become interrupted at startup; they are not resent automatically. Do not delete the originals until the recovery is verified. If the operation fails, keep the originals and treat the backup directory without a manifest as incomplete.
