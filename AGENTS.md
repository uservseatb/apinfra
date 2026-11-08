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
golangci-lint run ./...

# Task runner (requires go-task: brew install go-task)
task lint
task test
task check   # fmt + vet + lint + test

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
- Prefer `errors.Is`, `errors.As`, and `errors.AsType` over string comparison of error messages.

### Naming
- Packages: short, lowercase, no underscores (`config`, `store`, not `config_utils`).
- Files: lowercase with underscores only when needed (`user_store.go`).
- Exported identifiers: `CamelCase`; unexported: `camelCase`.
- **Strict Encapsulation & Visibility**: Minimize the surface area of your packages. Every function, method, struct, or type must be unexported (private, starting with a lowercase letter) by default, unless it is explicitly required to be accessed from another package. If a function is only used within its own package, it must remain lowercase.
- Interfaces: usually noun or `-er` (`Store`, `Reader`) — name by behavior, not `IStore`.
- Receivers: short (1–3 letters), consistent within a type (`func (s *Server)`, not `this` / `self`).
- Acronyms stay consistent with Go style: `URL`, `HTTP`, `ID` in identifiers (`ServeHTTP`, `userID`).

### Style

- Keep functions small; prefer early returns over deep nesting.
- Pass `context.Context` as the first parameter for I/O, RPC, and long-running work.
- Avoid global mutable state; wire dependencies explicitly (constructors / `New...`).
- Do not add comments that restate the code; comment non-obvious intent and exported APIs when helpful.
- Match existing style once code exists; until then, follow Effective Go and the standard library.
- **Multi-way branches → `switch`**: An `if` / `else` with only two outcomes is fine. When a decision has **more than two branches** (`if` / `else if` / `else`, or mutually exclusive early returns / type assertions that encode 3+ alternatives for the same value), use `switch` / `switch {` / type-`switch` instead of an if-ladder. Independent sequential guards (`if err != nil`, then `if x == nil`) stay as separate `if`s.
- **goconst / no magic strings**: Repeated string literals are a must-have `const` in **production code first**, not only in tests. If the same string appears 3+ times in a package (or is a domain/config/protocol value reused across call sites), extract a named `const` near related code. Tests should reuse those package consts when the value is shared with production logic; keep test-only fixtures in `*_test.go` (e.g. `testconst_test.go`). Never leave duplicated magic strings just because “it’s only a test” or “it’s only used twice more later.” Run `task lint` / `golangci-lint run ./...` before considering work done.

## Data Formats & Code Architecture Rules (YAML, JSON, TOML)

- **No Manual String Manipulation for Structural Data**: Strictly do not parse, modify, or construct YAML, JSON, or TOML contents using manual string functions (e.g., `strings.Index`, `strings.Replace`, slicing, custom string find loops, or regex). This approach is fragile and breaks indentation.
- **Library-Driven Approach**: Always use specialized and robust marshaling libraries for configuration management (e.g., `gopkg.in/yaml.v3` for YAML files).
- **Unmarshal-Modify-Marshal Pattern**: For any structured data modification, always unmarshal the content into native Go structs or `map[string]any`, mutate the data safely within the native types, and then marshal it back to the target format.
- **Idiomatic Go**: Keep the code self-documenting. Write clear, idiomatic Go and avoid over-commenting obvious statements, keeping comments focused only on architectural context and complex logic.


### Security & secrets

- Never commit secrets, tokens, or real `.env` / `.apinfra` values.
- Keep host templates in `build/deb/apinfra.example` and `build/deb/env.example` only (packaged into the .deb).
- Validate untrusted input at trust boundaries.

## Agent behavior

- Change only what the task requires; no drive-by refactors.
- Do not add Markdown docs the user did not ask for.
- After non-trivial Go changes, always run `go mod tidy`, `go fmt ./...`, and then run `go test ./...` to ensure everything builds, format matches, and tests pass cleanly.
- If `go.mod` / toolchain is missing and the task needs it, set that up first or ask.

### Tests & Testing Strategy (CRITICAL)

This project (apinfra) interacts heavily with the OS, Network, Git, and Docker API. Classic unit testing with heavy mocking of internal pipelines is strictly prohibited because it creates fragile tests that hinder refactoring.

#### 1. Architecture & Mocking Policy
* **Prefer real behavior over heavy mocks.** If interfaces exist for testing, keep them small.
* **Do NOT write unit tests** for internal orchestrator steps, control flow, or functions interacting with external tools (Git, Docker, ACME, Nginx). Do NOT invent mock interfaces just to test that "Function A calls Function B".
* **Environment Mocking:** Instead of mocking Go functions, mock the environment. For Git tests, initialize a real local git repo via `file:///tmp/...`. For binary dependencies like `dig` or `nginx`, use fake shell script stubs in a temporary `$PATH`.
* **Idempotency Testing:** Always include cases verifying the "Zero-Disk In-Memory Execution" principle. Running the agent twice against the same Git state must not mutate the host filesystem or restart healthy containers.

#### 2. Pure Logic -> Table-driven tests
* **Table-driven tests for pure logic only.** Use them for isolated, side-effect-free functions (e.g., `settings.yml` validation schema, `.env` string normalization, port stripping logic, certificate mode downgrade calculations).
* Use `t.Helper()`, `t.Parallel()` where safe, and `t.Cleanup` for teardown.
* **No helpers for a test gap:** do not invent production helpers, test-only wrappers, or parallel “desired API” functions just to make tests nicer. Extend tests against the existing functions under test. New units belong only as the real product fix, not as helpers introduced for tests.

#### 3. Execution & Structure
* **Test file ↔ source file:** Tests for symbols defined in `foo.go` must live in `foo_test.go`. Do not put tests for `foo.go` into an unrelated `bar_test.go`. Extra files named `foo_<facet>_test.go` are allowed only when they still test only `foo.go`. When moving production code between files, move the matching tests with it.
* **Golden Files:** Use snapshot testing (`testdata/` pattern) to compare complex CLI outputs or generated Nginx configs against verified historical snapshots rather than hardcoding long strings in Go code.
* Race detector (`-race`) for concurrent code before considering it done.
