# AGENTS.md — apinfra

Instructions for AI coding agents working in this Go repository.

## Project status

Greenfield Go project. Prefer small, idiomatic modules over premature frameworks.
Do not invent package layout until real code needs it. When adding the first packages,
use a standard layout:

```text
cmd/<app>/main.go      # entrypoints
internal/...           # private application code
pkg/...                # only if intentionally reusable outside this repo
```

Module path will live in `go.mod`. If absent, create it with `go mod init <module>`
before adding packages — ask for the module path if unknown.

## Commands

Run from the repository root.

```bash
# Dependencies
go mod tidy
go mod download

# Build all packages
go build ./...

# Build a specific binary (when cmd/ exists)
go build -o bin/<app> ./cmd/<app>

# Tests
go test ./...
go test -race ./...
go test -count=1 ./...                 # disable cache
go test -cover ./...
go test -coverprofile=coverage.out ./...
go tool cover -func=coverage.out

# Vet / static checks
go vet ./...
go fmt ./...

# Run a main package
go run ./cmd/<app>
```

Prefer `./...` over single-package commands unless working in one package.
Do not commit binaries, coverage artifacts, or `vendor/` unless explicitly requested.

## Code guidelines

### Errors

- Always check and handle errors; never ignore with `_` unless the reason is obvious and local.
- Wrap with context using `%w`: `fmt.Errorf("load config: %w", err)`.
- Return errors to the caller; log at the boundary (main, HTTP handler, worker loop), not in every layer.
- Do not use `panic` for expected failures (I/O, validation, missing config, network).
- `panic` / `Must*` helpers are allowed only for impossible states or true init-time programmer errors, and must be rare.
- Prefer `errors.Is` / `errors.As` over string comparison of error messages.

### Naming

- Packages: short, lowercase, no underscores (`config`, `store`, not `config_utils`).
- Files: lowercase with underscores only when needed (`user_store.go`).
- Exported identifiers: `CamelCase`; unexported: `camelCase`.
- Interfaces: usually noun or `-er` (`Store`, `Reader`) — name by behavior, not `IStore`.
- Receivers: short (1–3 letters), consistent within a type (`func (s *Server)`, not `this` / `self`).
- Acronyms stay consistent with Go style: `URL`, `HTTP`, `ID` in identifiers (`ServeHTTP`, `userID`).

### Style

- Keep functions small; prefer early returns over deep nesting.
- Pass `context.Context` as the first parameter for I/O, RPC, and long-running work.
- Avoid global mutable state; wire dependencies explicitly (constructors / `New...`).
- Do not add comments that restate the code; comment non-obvious intent and exported APIs when helpful.
- Match existing style once code exists; until then, follow Effective Go and the standard library.

### Tests

- Table-driven tests for pure logic.
- Use `t.Helper()`, `t.Parallel()` where safe, and `t.Cleanup` for teardown.
- Prefer real behavior over heavy mocks; if interfaces exist for testing, keep them small.
- Race detector (`-race`) for concurrent code before considering it done.

### Security & secrets

- Never commit secrets, tokens, or real `.env` values.
- Keep examples in `.env.example` only.
- Validate untrusted input at trust boundaries.

## Agent behavior

- Change only what the task requires; no drive-by refactors.
- Do not add Markdown docs the user did not ask for.
- After non-trivial Go changes, run `go test ./...` (and `go vet ./...` when practical).
- If `go.mod` / toolchain is missing and the task needs it, set that up first or ask.
