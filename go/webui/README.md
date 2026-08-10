# Nyro Go WebUI

This WebUI targets the Go implementation of Nyro.

The root `webui/` directory remains for the Rust implementation during the
parallel period. New Go-facing UI work belongs here and should use the Go admin
API schema directly: upstreams, routes, consumers, settings, logs, and stats.

## Development

```bash
pnpm install
pnpm run test
pnpm run lint
pnpm run build
```

Serve the built output with:

```bash
cd ..
go run . serve --webui-dir ./webui/dist --auto-migrate
```

The management listener defaults to loopback and the WebUI calls its
same-origin `/api/v1` endpoints directly. Off-host deployments must protect
the management listener with a private network, firewall, or authenticated
HTTPS reverse proxy.
