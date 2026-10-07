# go-remarkable-cloud

Pure Go library for the reMarkable Cloud Sync v3 API with zero external dependencies (standard library only).

## Architecture & Design Decisions

- **Domain-Oriented Structure**: Core concerns are separated cleanly into domains:
  - `config.go`: `~/.rmapi` key-value configuration loader and serializer.
  - `auth.go`: Device pairing and session token renewal (`POST /token/json/2/...`).
  - `discovery.go`: Dynamic service discovery with resilient fallback hosts.
  - `sync.go`: Sync v3 root state, line-delimited schema records, and document content models.
  - `client.go`: Thread-safe client orchestrating automatic token renewals, blob fetching, and manifests. `NewClient` returns every config file read or parse error except `ErrConfigNotFound`. `RenewToken` returns a renewed token it could not save together with `ErrConfigNotSaved`; renewals made while serving a request keep the token and report that error to the `WithSaveErrorHandler` handler. Never discard a config read or write error.
  - `cache.go`: Blob address validation, blob verification, and the blob disk cache. `getBlob` passes every hash through `validateBlobHash` before it reaches a request URL or a cache path, and `downloadBlob` returns `ErrBlobHashMismatch` for any download `blobMatchesHash` rejects; never add a blob read that bypasses either. Access entries only through the `os.Root` that `openBlobCache` returns, serve an entry only when `blobMatchesHash` accepts it, and keep directories `0700` and entries `0600`. `blobMatchesHash` accepts the schema v3 record-hash rule only for `.docSchema` names; never extend it to other files, because a schema-shaped blob could then stand in for a file whose bytes it splits into record hashes.
  - `response.go`: Bounded response body reads. Read rejected bodies through `readStatusError` (4096 bytes kept, `Truncated` marks more) and auth, discovery, root state, and root commit answers through `readServiceResponse` (64 KiB). `downloadBlob` reads at most the size a document index lists for a file (`GetFile`, used for `.metadata` and `.content`) or the client's blob size limit (`MaxBlobSize` by default) for every other blob. Every over-limit body returns `ErrResponseTooLarge` wrapped with `%w`; never read a response body without a limit, and never classify a size error by its message text. Only a file record of a document index carries the file's exact size; root index records carry a document's total size, so never bound an index read by a record's `Size`.
  - `resolver.go`: Document/collection hierarchy resolution by UUID, path (`"Folder/Subfolder/Doc"`), or `visibleName`. A name or path segment that matches several live items returns `*AmbiguousNameError` with every candidate in a fixed order; never return the first listing match, because `ListItems` order varies between calls.
  - `errors.go`: Typed sentinel errors (`ErrUnauthorized`, `ErrItemNotFound`, `ErrInvalidSchema`, etc.) and `StatusError`, which every non-success HTTP response returns; it unwraps to the sentinel that classifies the status (401, 403, and the malformed-token 400 to `ErrUnauthorized`, through the same `credentialsRefused` check that decides token renewal). `AmbiguousNameError` unwraps to `ErrAmbiguousName` and must not match `ErrItemNotFound`, so `Resolve` stops instead of falling back. Its `Error()` states only the name, the query when it differs, and the candidate count; never inline candidates there, because consumers render `Candidates` from the typed fields.
- **Zero External Dependencies**: Uses only Go standard library packages (`net/http`, `encoding/json`, `crypto/rand`, etc.). No external network calls in unit tests (all tests use `net/http/httptest`).
- **Resilient Fallbacks**: If service discovery fails, the library automatically falls back to tectonic and webapp cloud endpoints (`DefaultRawHost` and `DefaultWebappHost`).
- **Automatic Token Lifecycle**: Mints a new user token if missing, or when a device token is present and a request is refused with 401, 403, or a 400 whose JSON `message` is exactly `token is malformed` (the sync storage service's answer to a bearer token it cannot parse), then replays the request once. Never renew on any other 400 or match substrings of a 400 body: renewal writes the config file and replays a request the server rejected for its own content.
- **Root Commit Outcome**: `commitRoot` returns the write state. A 4xx answer, including a refused token whose renewal fails before the replay, means the server applied nothing and leaves `staged` with the error's classification intact. Only a missing answer, a non-200 status that is not a 4xx, or a 200 that does not confirm the new root leaves `commit-unknown`; never report a definite rejection as `commit-unknown`, because callers then inspect cloud state instead of fixing the cause.

## Invariants

1. **Self-Contained Go**: No Python scripts, wrappers, or external CLI dependencies.
2. **Deterministic Offline Tests**: All test suites must run in isolation without external network access using `httptest.Server`.
3. **Restricted File Permissions**: Written credential files (e.g. `~/.rmapi`) and blob cache entries must be created with `0600` permissions inside `0700` directories and safely written by renaming a uniquely named temporary file into place; remove the temporary file on every failure, because it holds the tokens or document bytes.
4. **Code Coverage**: Maintained at >= 90% statement coverage.

## Commands

- `just test`: Runs test suite (`go test -v ./...`).
- `just lint`: Runs static analysis with `golangci-lint run`.
- `just check`: Executes both `lint` and `test`.
