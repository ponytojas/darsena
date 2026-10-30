# Dársena

Dársena is an open-source platform for managing applications. This first preview is a **local, in-memory simulation**: it does not deploy applications, connect to GitHub, manage infrastructure, or persist data. URLs ending in `.example.invalid` are fictitious.

## Run the demo

Requirements: Go 1.27, Node.js 24, and pnpm 10.33.0.

```sh
pnpm install --frozen-lockfile
pnpm build
go run ./cmd/darsena serve --demo
```

Open http://127.0.0.1:7300. Stopping the server clears all simulated data. The server binds only to a loopback IP and has no authentication; it is unsuitable for production or exposure to a network.

For frontend development, run `pnpm dev` in a second terminal while the Go server is running, then open the Vite URL. The demo worker skeleton can be started with `go run ./cmd/darsena worker`; it does not perform infrastructure operations.

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
