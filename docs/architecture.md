# Architecture

The first preview has a Go HTTP process and a React frontend. The kernel registers trusted, statically compiled modules, checks their dependencies, starts them in order, and stops them in reverse order. `worker` is a separate executable mode reserved for future privileged operations.

The demo module holds bounded data in memory and advances simulated operations. HTTP exposes applications and operations under `/api/v1`; SSE delivers a complete operation snapshot on every connection and revision. A reconnect receives current state rather than a durable replay. A failed simulation preserves any previously healthy version.

The frontend uses the OpenAPI-generated types, TanStack Query, and Base UI components. It closes streams on terminal states and refreshes state on reconnect. Its data is fictional and the server is restricted to loopback.

Authentication, persistent jobs, an outbox, integrations, real deployments, backups, CLI operations, and MCP remain future work. Neither the current kernel nor the process separation provides a security sandbox.
