# Architecture

The first preview has a Go HTTP process and a React frontend. The kernel registers trusted, statically compiled modules, checks their dependencies, starts them in order, and stops them in reverse order. `worker --demo` is a separate simulated process with exclusive database ownership and a private Unix socket. The local HTTP server can proxy its small job interface. It has no privileged operations.

The demo module holds bounded data in memory and advances simulated operations. HTTP exposes applications and operations under `/api/v1`; SSE delivers a complete operation snapshot on every connection and revision. A reconnect receives current state rather than a durable replay. A failed simulation preserves any previously healthy version.

The frontend uses the OpenAPI-generated types, TanStack Query, and Base UI components. It closes streams on terminal states and refreshes state on reconnect. Its data is fictional and the server is restricted to loopback.

Authentication, an outbox, integrations, real deployments, and MCP remain future work. An experimental offline backup CLI is available as described below. Neither the current kernel nor the process separation provides a security sandbox.

## Experimental persistent worker

SQLite stores simulated jobs independently from the in-memory applications. When the worker is configured, the activity screen can create and list these persistent simulated jobs. A per-database advisory lock is acquired before recovering interrupted jobs. Idempotency keys identify requests; a unique partial index prevents overlapping jobs for one resource. The worker claims queued jobs and marks interrupted running jobs on startup. The socket exposes only job creation and lookup, with bounded JSON requests and private filesystem permissions.

This is not a production authorization boundary: ownership, credential isolation, process installation, external-effect reconciliation, and a real executor remain to be designed and tested. SQLite persists worker job metadata and application definitions; the optional separate vault persists encrypted variables. Steps, events, and the UI demo remain volatile. No automatic retry follows an interrupted operation.

## Experimental encrypted configuration

An optional worker configuration vault encrypts each complete resource configuration with an external age X25519 identity. SQLite stores ciphertext, revisions, and up to 20 encrypted historical revisions. Responses omit secret values; changing a value requires the current revision. Concurrent edits conflict instead of overwriting each other.

The experimental UI can create persistent application definitions and edit variables when the vault is configured. Resource identifiers must refer to persisted application definitions. This is not integrated into deployments. The local demonstration has no production authentication: use test values only. Old encrypted revisions can retain deleted or replaced secrets. Back up the age identity separately; loss of that identity prevents recovery. Back up SQLite consistently, including its WAL, rather than copying a live database file alone. Key rotation, secret injection, application lifecycle, and audit policy are future work.

Persistent application definitions are stored separately from the in-memory demo. Their image must be pinned to a SHA-256 digest. Creating a definition does not pull an image or start a container. Configuration endpoints require an existing persistent application ID. There is currently no deletion or deployment flow for these definitions.


## Startup configuration

The server and simulated worker resolve explicit flags before environment variables and defaults. Worker identity and path configuration is validated before opening its persistent state. The vault opens before job recovery; opening SQLite can create or migrate files even if a later startup step fails. Filesystem prechecks are not atomic and require private directories. The age identity is read from a private file, never a command-line secret. The optional vault and job store persist data, while demo application state remains volatile. The service remains an unauthenticated loopback-only demo; configuration does not enable a real runtime.

## Health and recovery

Liveness is independent of dependency readiness. Readiness performs bounded schema access checks, not a full integrity audit. On exclusive worker startup, running jobs are marked interrupted in bounded transactions and logged individually using validated identifiers. Database commits and logs are not atomic; queued and terminal jobs are unchanged and no retry is automatic. Operational limits and recovery steps are documented in [operations.md](operations.md).

## Proposed runtime boundary

[runtime.md](runtime.md) describes the proposed isolation, recovery, and ownership requirements for a first local provider. The provider-neutral contract, experimental Docker adapter, durable intent store, execution coordinator and read-only recovery are disconnected from startup, the worker and HTTP. Tests use doubles and a simulated Engine; the adapter has not been verified against a real rootless Linux daemon or enabled. Its version 3 policy accepts certain optional collections omitted by the Engine API, while requiring explicit safety-critical controls. A demo remains a simulation, regardless of whether a daemon exists on the host.

## Experimental offline backup

`cmd/darsena-backup` snapshots the job state and optionally the encrypted vault and runtime intentions into a new private directory. It acquires the worker's state lock and uses SQLite snapshots that include committed WAL contents; restoration checks the manifest, hashes, integrity and primary schema fields before writing to a new directory. All users of every included database must remain stopped throughout: the worker lock does not protect independent vault or intent users, and snapshots of different databases are not one atomic transaction. The age identity is held separately. Checksums detect accidental corruption, not a maliciously modified manifest. The procedure and its limitations are in [operations.md](operations.md).
