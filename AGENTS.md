# go-remarkable-cloud

Pure Go library for the reMarkable Cloud Sync v3 API with zero external dependencies (standard library only).

## Architecture & Design Decisions

- **Domain-Oriented Structure**: Core concerns are separated cleanly into domains:
  - `config.go`: `~/.rmapi` key-value configuration loader and serializer.
  - `auth.go`: Device pairing and session token renewal (`POST /token/json/2/...`).
  - `discovery.go`: Dynamic service discovery with resilient fallback hosts.
  - `sync.go`: Sync v3 root state, line-delimited schema records, and document content models.
  - `client.go`: Thread-safe client orchestrating automatic token renewals, blob fetching, and manifests.
  - `resolver.go`: Document/collection hierarchy resolution by UUID, path (`"Folder/Subfolder/Doc"`), or `visibleName`.
  - `errors.go`: Typed sentinel errors (`ErrUnauthorized`, `ErrItemNotFound`, `ErrInvalidSchema`, etc.).
- **Zero External Dependencies**: Uses only Go standard library packages (`net/http`, `encoding/json`, `crypto/rand`, etc.). No external network calls in unit tests (all tests use `net/http/httptest`).
- **Resilient Fallbacks**: If service discovery fails, the library automatically falls back to tectonic and webapp cloud endpoints (`DefaultRawHost` and `DefaultWebappHost`).
- **Automatic Token Lifecycle**: Seamlessly mints a new user token if missing or upon receiving `401 Unauthorized` responses when a device token is present.

## Invariants

1. **Self-Contained Go**: No Python scripts, wrappers, or external CLI dependencies.
2. **Deterministic Offline Tests**: All test suites must run in isolation without external network access using `httptest.Server`.
3. **Restricted File Permissions**: Written credential files (e.g. `~/.rmapi`) must be created with `0600` permissions and safely written via atomic tempfile rename.
4. **Code Coverage**: Maintained at >= 90% statement coverage.

## Commands

- `just test`: Runs test suite (`go test -v ./...`).
- `just lint`: Runs static analysis with `golangci-lint run`.
- `just check`: Executes both `lint` and `test`.
