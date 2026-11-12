# AGENTS.md

This file provides guidance for AI coding agents (Claude Code, Cursor, Codex CLI, etc.) working with code in this repository.

## Project Overview

This is a Go-based hierarchical content-construction engine (ivy) that supports JSON, YAML, XML, TOML, and custom data formats. The engine supports lazy evaluation, instant mode, cache TTL, and tree-based content organization.

## Architecture

### Core Components

- **Forest**: Manages multiple trees and provides tree lifecycle management
- **Tree**: Hierarchical structure with path-based access and layered content transformation
- **Directive**: Path + processors pair that defines a transformation on the tree
- **Driver**: Handles data format processing (JSON, YAML, XML, TOML, Tile, etc.)
- **Processor**: Content transformation operations (JSON manipulation, HTTP calls, etc.)

### Key Interfaces

Public API interfaces are defined in `export.go` (engine) and `driver/export.go` (driver package):

- `Forest`: Tree collection management with refresh capabilities
- `Tree`: Hierarchical content storage with path-based operations
- `Directive`: Path and processor definition for content transformation
- `Driver`: Data format handling and path parsing (composed of `PathParser` + `Realizer` + `Modem`)
- `Processor`: Content transformation operations

### Content Flow

All public path-taking methods use paths local to the receiver's tree root; prefer operating from the outer root. Native traversal parses once and advances a cursor through segments rather than relying on a node's stored absolute level. Calls into a custom `Tree` receive only the remaining suffix string, not the original whole-tree query.

`Get(path)` traverses from that local root to the target node. Each native node realizes its static processor prefix, then carries its content and publication version to its children. `Path()` derives the native mounted position dynamically from parent/mount-alias links; `ShowStruct()` uses those aliases, not source `Name()` values.

## Development Commands

### Building and Testing

```bash
# Run all tests
go test ./...

# Run tests for specific package
go test ./driver

# Build the server binary
go build -o ivy-server ./cmd/serve
```

### Running the Server

```bash
# Run the server with default configuration
go run ./cmd/serve

# Run with custom directives file
RULES_FILE=path/to/rules.json go run ./cmd/serve

# Run with custom shutdown timeout
SHUTDOWN_TIMEOUT=10s go run ./cmd/serve
```

### API Documentation

After starting the server, Swagger documentation is available at:
```
http://localhost:8080/swagger/index.html
```

Note: most web routes (`template`, `info`, `mod`, `list`, `config`) are currently `Ping` placeholders; only rule query (`GET /api/v1/rule`) is fully implemented.

## Key Design Patterns

### Tree Modes

- **Standard Mode**: Static prefixes are evaluated during initial construction; after mounting they re-evaluate on first access and on parent publication changes
- **Lazy Mode**: Static content is calculated on first access; directives may establish topology earlier
- **Instant Mode**: Nodes are recalculated on every access for real-time data
- **Cache TTL Mode**: Lazy with time-based cache expiration

### Direct Subtree Mounting

- Public API: `Tree.Graft(path string, source Tree) error`; this is a breaking replacement for `Graft(Tree)`. Do not reintroduce a clone-based `GraftAt` function or require `Clone`.
- The target path is receiver-local, must be non-root, and must have an existing parent. Its final segment is the mount alias; replacement is whole-subtree replacement, not a merge.
- Native mounting attaches the exact source object. Retained source references still edit the mounted object; source drivers, processors, modes, rate limiters and context objects are not copied or replaced.
- A native source must be detached and single-parent. Reject self/cycle/duplicate ownership. Replacement and `Del` detach removed native subtrees so they can be remounted.
- Invalidate source template/results caches for new parent inheritance, but never execute processors in `Graft`, including standard processors. Mounted standard nodes wait for access and recompose when their parent publication changes.
- Preserve source modes independently. Parameter-aware processors remain target-only and never enter the static cache.
- Do not assume arbitrary custom implementations provide native inheritance or position tracking. Each implementation controls its own semantics/concurrency; the `Tree` interface imposes no lock policy.
- Forest refresh reruns builders and replaces trees. Recreate source trees and mounting steps inside `TreeBuilder` when composition must survive refresh.

### Driver System

Multiple driver implementations:
- `JSONDriver`: JSON data format processing
- `YAMLDriver`: YAML data format processing
- `XMLDriver`: XML data format processing
- `TOMLDriver`: TOML data format processing
- `TileDriver`: Custom tile-based processing
- `webDriver`: Web API integration driver (in `web/server.go`)

### Processor Types

- `JSONProcessor`: JSON data manipulation
- `YAMLProcessor`: YAML data manipulation
- `XMLProcessor`: XML data manipulation
- `TOMLProcessor`: TOML data manipulation
- `CURLProcessor`: HTTP request processing
- `TemplateProcessor`: `${param}` interpolation at query time (param-aware)
- `RawProcessor`: Custom transformation function
- `CombinedProcessor`: Combines multiple processors

### Param-Aware Processors (Dynamic Layer)

A processor implementing the optional `driver.ParamAware` interface declares
that its output depends on request params (`RealizeContext.Params`). The tree
splits each node's chain at the first param-aware processor:

- **static prefix** (`procs[:dynamicFrom]`): realized and cached exactly as
  before — standard/lazy/instant/TTL semantics unchanged
- **dynamic layer** (`procs[dynamicFrom:]`): re-applied on every
  `GetWithContext` at the target node only; the result is returned to the
  caller and never written into the node cache, so different params can never
  pollute each other

`Get` (no context) returns the static base with placeholders intact. Dynamic
processors on intermediate nodes of a queried path are not applied; descent
uses their static content.

## File Structure

```
├── AGENTS.md            # Agent guidance (this file)
├── cmd/serve/           # Server entry point
├── conf/                # Default directives configuration
├── driver/              # Data format drivers and processors
├── web/                 # HTTP server and API handlers
├── directive.go         # Directive interface and implementation
├── tree.go              # Tree structure and operations
├── forest.go            # Forest management
├── factory.go           # Factory methods for tree creation
└── export.go            # Public API interfaces
```

## Configuration

### Environment Variables

- `RULES_FILE`: Path to directives configuration file (default: `../../conf/rules.json`)
- `SHUTDOWN_TIMEOUT`: Server shutdown timeout (default: `3s`)
- `RULES_TTL`: Cache TTL for rule trees, e.g. `5m`. When positive, the default tree becomes lazy+cache-TTL: content is realized on first access and re-realized (re-running processors such as curl rule URLs) on the first access after each TTL window. Zero/invalid (default) keeps build-once standard mode. Rule-file changes can be picked up at runtime with SIGHUP.

### Directives File Format

Directives are defined in JSON format:
```json
[
  {
    "path": "/api/v1/users",
    "Processors": [
      {
        "type": "json",
        "data": {"operation": "merge", "value": {"enabled": true}}
      }
    ]
  }
]
```

## Testing Notes

- Tests include integration tests that make HTTP calls (may fail without network)
- Some tests require specific file paths that may not exist in all environments
- Test failures related to network connectivity or missing files are expected in isolated environments

## Common Development Tasks

### Adding New Processor Types

1. Implement the `driver.Processor` interface
2. Add processor type handling in `cmd/serve/serve.go:load()`
3. Update tests to cover the new processor

### Creating Custom Drivers

1. Implement `driver.Driver` interface
2. Add factory methods in `factory.go`
3. Update tree creation functions to support the new driver

### Extending Tree Functionality

- Modify `tree.go` for new tree operations
- Update `export.go` interfaces if API changes are needed
- Keep concurrency guarantees in the concrete implementation; the `Tree` interface must not prescribe locks. Native changes still need tests for their own topology/caching safety.
- Preserve receiver-local path traversal, suffix delegation at custom-tree boundaries, direct source identity and detach/remount ownership; do not restore absolute-level routing.
