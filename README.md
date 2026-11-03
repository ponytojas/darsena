# Dársena

Dársena is an open-source platform for managing applications. Its demo runs **local simulations**; demo applications are in memory, and the optional worker persists jobs and encrypted saved configuration. An explicitly enabled local experimental mode can create one isolated container per saved application using a dedicated rootless Docker Engine. It does not build images, connect to GitHub, publish ports or provide public URLs. URLs ending in `.example.invalid` are fictitious. The experimental mode is under review and is not production-ready.

## Run the demo

Requirements: Go 1.27, Node.js 24, and pnpm 10.33.0.

```sh
pnpm install --frozen-lockfile
pnpm build
go run ./cmd/darsena serve --demo
```

Open http://127.0.0.1:7300. Stopping the server clears all simulated data. The server binds only to a loopback IP and has no authentication; it is unsuitable for production or exposure to a network.

For frontend development, run `pnpm dev` in a second terminal while the Go server is running, then open the Vite URL. An experimental simulated worker can be run separately. Create a private directory and use it for the state and socket:

```sh
mkdir -m 700 /tmp/darsena-worker
go run ./cmd/darsena worker --demo --state /tmp/darsena-worker/state.db --socket /tmp/darsena-worker/worker.sock
```

In another terminal, run `go run ./cmd/darsena serve --demo --worker-socket /tmp/darsena-worker/worker.sock`. The local HTTP API proxies `POST /api/v1/worker/jobs` (JSON `resource_id` and `idempotency_key`) and `GET /api/v1/worker/jobs/{id}` to the worker. The Diagnostics screen can create and list these jobs. They are simulations, separate from the in-memory application demo. The worker never touches Docker. Only one worker may own a state database. After an unclean shutdown, check that no worker is running before manually removing its stale socket. Running jobs are marked interrupted; queued jobs can be picked up after restart.

## Checks

```sh
go test -race ./...
pnpm lint
pnpm build
pnpm --dir web exec playwright install chromium
pnpm test:e2e
```

The OpenAPI contract is in `api/openapi.json`. Run `pnpm generate` after changing it and commit the generated types. See [architecture](docs/architecture.md) for boundaries and current limitations.

## License

MIT; see [LICENSE](LICENSE).

The worker also accepts `--config-db <path> --age-identity <path>` to enable an experimental encrypted configuration API. The identity file must be a private regular file containing exactly one age X25519 identity (the standard `age-keygen` format is accepted). Keep a recovery copy of the identity separate from the database. See [architecture](docs/architecture.md) for its limitations. This local demo has no authentication; use test values only.

For example, after installing the `age` command-line tools, generate a **test** identity in the private worker directory and start the worker with the vault enabled:

```sh
age-keygen -o /tmp/darsena-worker/identity.age
go run ./cmd/darsena worker --demo --state /tmp/darsena-worker/state.db --socket /tmp/darsena-worker/worker.sock --config-db /tmp/darsena-worker/config.db --age-identity /tmp/darsena-worker/identity.age
```

Connect the demo server with `--worker-socket` as described above, then create an application with the *definition* type from **Nueva aplicación**. A pinned image and variables are stored but never deployed. Definitions can be grouped into projects, moved in bulk with **Seleccionar**, edited from their **Ajustes** tab, and their variable history can be reviewed and restored from **Historial** (values are never shown); a definition that ever had a service cannot be deleted or have its image changed. Do not use production credentials in this unauthenticated demo.

In demo mode the interface reports the worker and encrypted-store availability and marks real execution as disabled. If availability cannot be confirmed, affected actions remain disabled and drafts are kept for a retry. `GET /api/v1/capabilities` exposes the effective mode and availability categories; in experimental mode provider readiness is a bounded daemon preflight, not proof of kernel isolation. See [operations](docs/operations.md) for diagnostics.

## Server configuration

SQLite databases use ordinary filesystem paths; `:memory:` and `file:` URIs are rejected. Database sidecars and lock files must be private regular files, and all configured paths must be distinct. Startup checks cannot prevent concurrent filesystem changes: protect the containing directories. Opening SQLite may create or migrate files before a later startup failure.

Explicit command-line flags take precedence over environment variables, which take precedence over defaults. A present but empty environment variable is an explicit value. Select exactly one of `--demo` and `--experimental` on both processes. Use `--help` for usage. Do not put identity contents or credentials in flags, environment variables, or process arguments: provide paths to private files.

| Command | Flag | Environment | Default |
| --- | --- | --- | --- |
| Both | `--demo` | `DARSENA_DEMO` | `false` |
| Both | `--experimental` | `DARSENA_EXPERIMENTAL` | `false` |
| Both | `--installation` | `DARSENA_INSTALLATION` | empty; required in experimental mode |
| serve | `--addr` | `DARSENA_ADDR` | `127.0.0.1:7300` |
| serve | `--web` | `DARSENA_WEB` | `web/dist` |
| serve | `--worker-socket` | `DARSENA_WORKER_SOCKET` | empty |
| serve | `--owner-credential` | `DARSENA_OWNER_CREDENTIAL` | empty; required in experimental mode |
| serve | `--origin` | `DARSENA_ORIGIN` | empty; required in experimental mode |
| worker | `--state` | `DARSENA_STATE` | required |
| worker | `--socket` | `DARSENA_SOCKET` | required |
| worker | `--config-db` | `DARSENA_CONFIG_DB` | empty |
| worker | `--age-identity` | `DARSENA_AGE_IDENTITY` | empty |
| worker | `--intent-db` | `DARSENA_INTENT_DB` | empty; required in experimental mode |
| worker | `--docker-socket` | `DARSENA_DOCKER_SOCKET` | empty; required in experimental mode |

The worker requires state and socket paths; the configuration database and identity must be supplied together. Persistent paths must be distinct. The identity must be a private regular file, at most 4096 bytes, with exactly one X25519 identity. The server requires a loopback IP and a port from 1 to 65535.

For an operated local demo, create a private directory owned by the service account, provision an identity separately with mode `0600`, and use absolute paths, for example:

```sh
DARSENA_DEMO=true DARSENA_STATE=/var/lib/darsena/state.db \
  DARSENA_SOCKET=/var/lib/darsena/worker.sock \
  DARSENA_CONFIG_DB=/var/lib/darsena/config.db \
  DARSENA_AGE_IDENTITY=/var/lib/darsena/identity.age darsena worker
DARSENA_DEMO=true DARSENA_WORKER_SOCKET=/var/lib/darsena/worker.sock darsena serve
```

This illustrates process configuration only: the unauthenticated preview is not production-ready. The test tooling also uses `DARSENA_E2E_PORT` and `CI`; neither configures the server.

See [operations and diagnostics](docs/operations.md) for health endpoints, recovery, log retention and current storage limits.

## Experimental isolated service

This opt-in local mode requires Linux, a dedicated compatible rootless Docker daemon and private paths. Definitions always store an image pinned by digest in canonical form (`docker.io/library/…@sha256:…`); in this mode a public tag such as `nginx:1.27` is resolved to its current digest when typed. The form reports whether the dedicated daemon has the image and for which platform, and offers an explicit download of public images without registry credentials (one at a time, up to 10 minutes). A service is only created once the image is present and compatible, checked before anything is persisted. An application without variables is created with an empty first configuration revision. The saved port is metadata: the container has no network, published port or public URL. One initial service operation is available per application. The UI can stop a running service and, after it is observed stopped, explicitly remove its container; the saved definition and configuration remain. Recreation and automatic rotation are not available. An unknown outcome must be inspected rather than blindly resent.

Create an owner credential with `darsena owner-init --owner-credential /private/path/owner.credential`; the command creates a new private file and does not print its contents. `darsena age-init --age-identity /private/path/identity.age` creates the age identity the same way, without needing `age-keygen`. Keep them and all parent directories private. Start the worker with `--experimental --installation <stable-id> --state <state.db> --socket <worker.sock> --config-db <config.db> --age-identity <identity.age> --intent-db <intent.db> --docker-socket <dedicated-rootless.sock>`. Start the server with `--experimental --installation <same-id> --worker-socket <worker.sock> --owner-credential <owner.credential> --origin http://127.0.0.1:7300`; `--origin` must match the loopback listen address exactly. Replace placeholders with distinct private paths. Enter the credential from its protected file into the local sign-in screen; never place its contents in a URL or command argument. Sessions are in memory and end on server restart.

The daemon owner can inspect container environment values. Keep the daemon dedicated, never use production credentials during this preview, and consult [runtime limits and recovery](docs/runtime.md) before use. No global cleanup is performed. Backend login, configured creation, stop, removal, rejected stale versions and idempotent repetition were exercised with synthetic data on the dedicated OrbStack host. A separate headless Chromium test on macOS at 1440×900 exercised login, saving a definition and configuration, creation, stop and removal against the real OrbStack server and worker; test resources were cleaned up. Mobile UI tests used a simulated provider. These checks are not a full security review or certification for other environments.

### Running it in a dedicated OrbStack machine

`scripts/orbstack.sh` builds the frontend and a Linux executable on macOS, copies them into a dedicated OrbStack Linux machine and runs the worker and server there as systemd user units (`darsena-worker`, `darsena-server`). It never uses the Docker of the Mac and never discovers a daemon socket: the first run requires the dedicated rootless socket, which is then kept with the installation.

```sh
scripts/orbstack.sh up --docker-socket /run/user/<uid>/docker.sock   # first run
scripts/orbstack.sh up             # later runs; add --no-build to start the installed build
scripts/orbstack.sh status         # processes, URL and provider availability
scripts/orbstack.sh credential     # copies the owner credential to the clipboard without printing it
scripts/orbstack.sh logs [-f]
scripts/orbstack.sh down           # stops only its own units; data is kept
```

Open `http://127.0.0.1:7320` on the Mac; OrbStack forwards the machine's loopback port. `DARSENA_ORB_MACHINE` (default `darsena-runtime-test`), `DARSENA_ORB_PORT` (default `7320`, distinct from the demo's `7300`) and `DARSENA_ORB_HOME` (default `~/darsena` in the machine) change the defaults. On the first run the script creates only missing files in a private data directory: installation identity, age identity (with `darsena age-init`), owner credential and the daemon socket setting. It never overwrites them, and refuses to create a new installation or age identity if databases that depend on the old one exist; restore them from a backup instead. Keep a recovery copy of the age identity apart from the databases. Images must already be present in the dedicated daemon by digest; `up` lists them. The machine needs systemd, `curl`, `ss`, `python3` and a running rootless daemon; Go and `age-keygen` are not needed there.
