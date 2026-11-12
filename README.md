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
| `NewLazyJSONTree` | lazy | static content realized on first access |
| `NewLazyInstantJSONTree` | instant | recompute on every access |
| `NewLazyCacheJSONTree` | cache TTL | lazy semantics with expiry-based recompute |

(Same variants exist for YAML, TOML, XML, and Tile drivers.)

### Direct subtree mounting and mixed modes

Use `root.Graft(path, source)` to insert or replace an entire subtree at a
non-root path whose parent already exists. The source is attached **directly**:
there is no clone and no separate `GraftAt` function.

```go
root, err := ivy.NewJSONTree[ivy.Directive]("cfg", `{"enabled":true}`)
if err != nil {
    return err
}
branch, err := ivy.NewLazyJSONTree("branch", `{}`,
    ivy.NewDirective("/", &driver.JSONProcessor{
        T: "create", JSONPath: "region", V: []byte("beijing"),
    }),
    ivy.NewDirective("/users"),
)
if err != nil {
    return err
}
if err := root.Graft("/api", branch); err != nil {
    return err
}
value, err := root.Get("/api") // {"enabled":true,"region":"beijing"}
```

The target name is the mount alias (`api`), not `source.Name()` (`branch`).
For native trees, `branch.Path()` now returns `/api`; `ShowStruct()` uses mount
aliases as keys. `branch` is still the same object: editing it affects the
mounted branch, rather than a private copy.

Keep subsequent operations root-oriented, including mounting below a branch
that was itself mounted earlier:

```go
live, err := ivy.NewLazyInstantJSONTree[ivy.Directive]("live-source", `{}`)
if err != nil {
    return err
}
if err := root.Graft("/api/users/live", live); err != nil {
    return err
}
value, err = root.Get("/api/users/live")
```

All public path-taking calls are **local to the receiver's tree root**, even
when the path begins with `/`. For example, `branch.Has("/users/live")` examines
the same node as `root.Has("/api/users/live")`; it does not interpret `/users`
as a path from the outer root. Native traversal parses once and consumes path
segments. When it delegates to a custom `Tree`, the public method receives the
remaining suffix string, not the original whole-tree query.

Native mounting has these semantics:

- Existing descendants at the target are replaced, not merged. The source
  must be detached and may have only one parent; self-mounts, cycles and
  duplicate ownership are rejected. Replacement and `Del` detach the removed
  native subtree so it can be mounted again elsewhere.
- Every source node keeps its own driver, processors, evaluation mode, rate
  limiter and context objects. These are the same objects, not copies.
  Matching content formats (or an explicit conversion processor) is the
  caller's responsibility. Native node aliases must also be representable in
  every enclosing native path grammar; incompatible names are rejected rather
  than creating nodes that root or retained-handle queries cannot address.
- Mounting invalidates the source subtree's cached template/results for its
  new inheritance. Content now flows from the destination parent.
  **`Graft` never executes processors**, even for standard nodes. Mounted
  standard nodes first re-evaluate on access and recompose when the parent
  publication changes; lazy, instant and TTL modes retain their own caching
  behavior. Parameter-aware output remains per-query, never cached.
- Native positions derive dynamically from parent/mount-alias links rather
  than a stored absolute level. Moving a detached subtree therefore does not
  require rewriting every descendant's absolute path.

The `Tree` interface does not prescribe a locking policy. Each implementation
owns its concurrency guarantees; do not assume native inheritance or position
tracking applies to arbitrary custom implementations. Public source references
remain aliases to the attached object, so coordinate edits according to that
implementation's contract. Native retained-handle reads resolve ancestor static
content too, including on the first read after mounting or an ancestor refresh.
Native reads check mount identity as well as parent content versions and retry
from their entry root when ownership changes during a query. This prevents an
old parent's snapshot from populating a remounted subtree's cache; processors
may run again during a retried query. Native realization is not reentrant: do
not transfer a detached source from inside a processor currently realizing
that same source.

`Forest.Refresh` / `RefreshTree` rebuild trees through their `TreeBuilder`;
put the mounting steps in the builder to recreate the same composition on
refresh. Runtime mounts are not automatically replayed onto a newly built tree.

**Breaking API change:** `Graft(Tree)` is now
`Graft(path string, source Tree) error`. Callers must provide a target path and
check the error; custom `Tree` implementations must update their method.

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
