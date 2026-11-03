# go-remarkable-cloud

Pure Go library for the reMarkable Cloud Sync v3 API with zero external dependencies (standard library only).

## Architecture & Design Decisions

- **Domain-Oriented Structure**: Core concerns are separated cleanly into domains:
  - `config.go`: `~/.rmapi` key-value configuration loader and serializer.
  - `auth.go`: Device pairing and session token renewal (`POST /token/json/2/...`).
  - `discovery.go`: Dynamic service discovery with resilient fallback hosts.
  - `sync.go`: Sync v3 root state and line-delimited schema records. `content.go` and `content_json.go` hold lossless document/page/register models; preserve unknown fields and the distinction between absent, null, and zero values.
  - `client.go`: Thread-safe client orchestrating automatic token renewals, blob fetching, and manifests.
  - `item.go` and `resolver*.go`: Document/collection hierarchy resolution by UUID, path (`"Folder/Subfolder/Doc"`), or `visibleName`. A name or path segment that matches several live items returns `*AmbiguousNameError` with every candidate in a fixed order; never return the first listing match, because `ListItems` order varies between calls.
  - `errors.go`: Typed sentinel errors (`ErrUnauthorized`, `ErrItemNotFound`, `ErrInvalidSchema`, etc.) and `StatusError`, which every non-success HTTP response returns; it unwraps to the sentinel that classifies the status (401/403 to `ErrUnauthorized`). `AmbiguousNameError` unwraps to `ErrAmbiguousName` and must not match `ErrItemNotFound`, so `Resolve` stops instead of falling back. Its `Error()` states only the name, the query when it differs, and the candidate count; never inline candidates there, because consumers render `Candidates` from the typed fields.
- **Zero External Dependencies**: Uses only Go standard library packages (`net/http`, `encoding/json`, `crypto/rand`, etc.). No external network calls in unit tests (all tests use `net/http/httptest`).
- **Resilient Fallbacks**: If service discovery fails, the library automatically falls back to tectonic and webapp cloud endpoints (`DefaultRawHost` and `DefaultWebappHost`).
- **Automatic Token Lifecycle**: Seamlessly mints a new user token if missing or upon receiving `401 Unauthorized` responses when a device token is present.

## Invariants

1. **Self-Contained Go**: No Python scripts, wrappers, or external CLI dependencies.
2. **Deterministic Offline Tests**: All test suites must run in isolation without external network access using `httptest`. Repeated ambiguity fixtures use `handlerTransport` and `httptest.NewRecorder` without TCP sockets. Use real servers for transport behavior. A measured full run on macOS added 204 TIME_WAIT sockets; keep the increase below 500 per run when changing network fixtures (system-wide measurements can include unrelated traffic).
3. **Restricted File Permissions**: Credentials and cached blobs use `0600` files and atomic tempfile rename; cache directories use `0700`. Validate lowercase SHA-256 hashes before filesystem access and verify cached and downloaded bytes. Document schemas and version-3 roots hash decoded entry hashes in ID order; version-4 roots and ordinary files hash their bytes. Test fixtures must use protocol-valid hashes.
4. **Code Coverage**: Maintained at >= 90% statement coverage.

## Commands

- `just test`: Runs test suite (`go test -v ./...`).
- `just lint`: Runs static analysis with `golangci-lint run`.
- `just check`: Executes both `lint` and `test`.
