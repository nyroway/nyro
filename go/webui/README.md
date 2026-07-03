# Nyro Go WebUI

This WebUI targets the Go implementation of Nyro.

The root `webui/` directory remains for the Rust implementation during the
parallel period. New Go-facing UI work belongs here and should use the Go admin
API schema directly: upstreams, routes, consumers, settings, logs, and stats.

## Development

```bash
pnpm install
pnpm run lint
pnpm run build
```

Serve the built output with:

```bash
cd ..
go run . admin --webui-dir ./webui/dist
```
