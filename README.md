# ivy

[![License: MIT](https://img.shields.io/badge/license-MIT-yellow.svg)](LICENSE)
[![Go Version](https://img.shields.io/badge/go-1.25%2B-00ADD8?logo=go)](https://go.dev)

A hierarchical content-construction engine for Go: content starts from a root
template and flows down a path-addressed tree, where every node applies a
chain of Processors to the content inherited from its parent. Query a path,
get the result shaped by every layer along the way.

```
template ──► /            (root content)
              └─ /api       Processor chain A
                  └─ /v1    Processor chain B   ──► GET /api/v1
```

## Features

- **Path-addressed trees** — a `Tree` holds content, a Processor chain and
  children; `Forest` manages named trees with build/refresh lifecycle and
  global rate limiting
- **Directives** — declare "path + Processor chain" pairs; trees are built by
  applying directives from shallow to deep
- **Pluggable format drivers** — JSON, YAML, TOML, XML, Tile; each driver
  composes three independent concerns: `PathParser` (path semantics),
  `Realizer` (chain execution), `Modem` (processor serialization)
- **Composable processors** — document edits (`sjson`-style), HTTP fetching
  (`curl`), `${param}` template interpolation, and raw pass-through
- **Dynamic layer** — processors that declare `ParamAware` recompute on every
  query and never enter the cache
- **Four evaluation modes** — standard (eager), lazy (build on access),
  instant (always recompute), and cache-with-TTL
- **HTTP serving** — gin-based exposure with Swagger docs, or embed the
  engine as a library

## Installation

```bash
go get github.com/tr1v3r/ivy
```

## Quick Start

```go
f := ivy.NewForest(
    mustTree(ivy.NewLazyJSONTree("cfg", `{"api": {"v1": {"ok": true}}}`)),
)

val, err := f.GetVal("cfg", "/api/v1") // → {"ok":true}
```

Attach transforms with directives — a `curl` processor that pulls remote
content into a path, a `template` processor that interpolates request params:

```go
tree, _ := ivy.NewLazyJSONTree("cfg", `{}`,
    ivy.NewDirective("/", new(driver.CURLProcessor)),     // Load() from rule data
    ivy.NewDirective("/api/v1", new(driver.TemplateProcessor)),
)
```

Or run it as a service from a rules file:

```bash
RULES_FILE=./rules.json go run ./cmd/serve
```

```json
[
    {
        "path": "/",
        "Processors": [
            {
                "type": "curl",
                "data": { "url": "https://example.com/config.json" }
            }
        ]
    }
]
```

### curl processor security

The `curl` processor owns its security posture explicitly:

- **TLS verification is on by default.** Certificates are verified against
  the system roots; rules targeting self-signed upstreams must opt in per
  rule with `"insecure": true`.
- **Response bodies are capped** at 10 MiB by default. `"max_bytes": N`
  raises (or lowers) the cap; `"max_bytes": -1` disables it. An oversized
  response fails with an explicit error instead of exhausting memory.
- **URLs are allowlistable** process-wide: set `IVY_CURL_ALLOW_HOSTS` to a
  comma-separated list of hostnames (e.g.
  `IVY_CURL_ALLOW_HOSTS=example.com,api.example.org`). An unset or empty
  variable allows every host (out-of-box default); otherwise a rule's URL —
  and every redirect target it follows — must match one entry exactly
  (case-insensitive, ports ignored). The URL scheme must be `http` or
  `https`.

```json
{
    "type": "curl",
    "data": {
        "url": "https://internal.local/config.json",
        "insecure": true,
        "max_bytes": 1048576
    }
}
```

## Evaluation modes

| Factory | Mode | Behavior |
|---|---|---|
| `NewJSONTree` | standard | static prefix computed at build time |
| `NewLazyJSONTree` | lazy | nodes created and realized on access |
| `NewLazyInstantJSONTree` | instant | recompute on every access |
| `NewLazyCacheJSONTree` | cache TTL | lazy semantics with expiry-based recompute |

(Same variants exist for YAML, TOML, XML, and Tile drivers.)

## Development

```bash
make test    # unit tests
make race    # race detector
make cover   # coverage
```

See [ARCHITECTURE.md](ARCHITECTURE.md) for the full design walkthrough:
concept dictionary, module map, interface contracts, and the dynamic-layer
semantics.

## License

[MIT](LICENSE)
