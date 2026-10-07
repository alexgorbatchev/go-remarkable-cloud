`go-remarkable-cloud` is a self-contained Go client library for reMarkable Cloud Sync v3, providing authentication, service discovery, document resolution, content-addressed blob streaming, and disk caching.

# What It Does

- **Full Sync v3 implementation**: Connects directly to reMarkable Cloud Sync v3 endpoints (`/sync/v3/root`, `/sync/v3/files/{hash}`).
- **Verified, private disk caching**: Stores downloaded blobs and manifests by SHA-256 hash in owner-only files, eliminating duplicate network downloads, and serves an entry only after checking its bytes against its hash.
- **Generation-checked document writes**: Stages file updates, verifies uploaded bytes, and commits document and root manifests while detecting concurrent root changes.
- **Separate document creation**: Creates a document from caller-encoded files without replacing an existing ID, with precommit recovery progress and fresh byte verification.
- **Document resolution**: UUID lookup scans the root manifest in O(n) time and fetches the matching item's manifest and metadata. Name and path lookups load metadata across the library and report every candidate when a name is shared instead of picking one.
- **Automated token lifecycle**: Handles 8-character device pairing and transparent user token renewal when the cloud refuses the session token.
- **Zero external dependencies**: Built using standard Go library primitives (`net/http`, `crypto/rand`, `encoding/json`).

# How It Works

- Loads existing credentials from `$XDG_CONFIG_HOME/remarkable-cli/config.json` or `~/.rmapi`, or pairs a new device token.
- Queries service discovery endpoints to resolve active auth and storage hosts.
- Fetches the root generation state and line-delimited schema manifests.
- Downloads content-addressed file blobs (PDF stationery, EPUB, content schemas, `.rm` vector strokes) and deserializes metadata.

# How it Really Works

- Metadata download and JSON decoding failures return contextual errors. Listings return an error rather than incomplete results when an item's manifest or metadata cannot be read.
- `Item.GetContent` reads the item manifest when the item's known entries have no `.content` file, as in a flattened root. A failed manifest download returns its own error, such as a `*StatusError` for a 5xx status, or one for a 401 that also matches `ErrUnauthorized`; it matches `ErrItemNotFound` only when the manifest is missing (a 404 or no schema hash) or lists no `.content` entry.

- When a cache directory is configured via `WithCacheDir`, `GetBlob`, `GetManifest`, and `GetContent` read `$CACHE_DIR/blobs/<hash>` before making network calls. The hash must be 64 lowercase hexadecimal digits, the form of every sync v3 blob address. Any other hash, such as `../../planted.txt` from a damaged or hostile manifest, returns an error matching `ErrInvalidBlobHash` before the cache or the network is touched. `GetBlobFresh` and a client without a cache directory do not check the hash.
- A cache entry is served only when its bytes match its hash. Files and schema v4 root indexes match the SHA-256 of their bytes. A blob requested under a filename ending in `.docSchema` may instead match the schema v3 rule the tablet uses: the SHA-256 of its records' hashes in record ID order, which covers those hashes and their order but no other record field. An entry that does not match is deleted and downloaded again. A download is cached only when it matches; one that does not is returned uncached, exactly as `GetBlobFresh` returns it.
- The cache directory and its `blobs` directory are created with mode 0700, and entries are written with mode 0600 through a temporary file renamed into place. An existing `blobs` directory loses its group and other permissions, which also closes entries that earlier versions wrote with mode 0644; an existing cache directory keeps its permissions. Entries are read and replaced only inside `blobs`, never through a symbolic link that leaves it. When the cache cannot be created or secured, reads download without caching.
- For nested root manifests, `ResolveByID` reads the root state, root manifest, matching item's manifest, and metadata. Other items' metadata is not fetched; cached blobs can avoid download requests. Flattened root manifests use the listing path.
- `ResolveByName`, `ResolveByPath`, and `Resolve` match exact, case-sensitive visible names among live items: items whose own metadata is neither deleted nor in the trash. A document inside a trashed folder is still live and resolves by name. A path walks down from the root, and every segment but the last matches only folders.
- When a name, or one path segment, matches more than one live item, resolution fails with an `*AmbiguousNameError` instead of choosing one; `Resolve` returns it unchanged. `errors.Is(err, cloud.ErrAmbiguousName)` matches it, and it never matches `ErrItemNotFound`. Its `Error` text names the ambiguous name, the query when it differs, and the number of candidates, such as `ambiguous item name: "Notes" in "Work/Notes" matches 2 items`, and lists no candidates; read them with `errors.As` and render them yourself. `Candidates` holds every matching item with its `FolderPath`, the `/`-joined names of the live folders above it, empty at the root. When an ancestor is trashed, deleted, missing from the listing, a document, or part of a parent loop, `Unreachable` is true, `FolderPath` holds only the live folders below that break, and no path reaches the item. A reachable candidate's `FolderPath` plus its name can still be ambiguous: another item in the same folder can share its name, or another folder beside one of its ancestors can share that ancestor's name. Only the ID always selects one candidate. Reachable candidates come first, then candidates sort by `FolderPath` and then by ID, both byte-wise, so the same cloud state always yields the same list.
- Client instances are safe for concurrent use across multiple goroutines; a single client should be shared across the lifetime of an application.
- On HTTP 401 or 403 responses, the client automatically acquires a fresh session token and replays the failed request before returning an error. It does the same for a 400 whose JSON body has the `message` `token is malformed`, which the sync storage service returns for a session token it cannot parse. A malformed-token 400 that is still refused after renewal matches `ErrUnauthorized`. Every other 400 returns as a `*StatusError` that matches no sentinel, without renewal or replay.
- Every rejected HTTP response returns a `*StatusError` carrying the operation name, status code, and the first 4096 bytes of the response body exactly as the server sent them; read it with `errors.As`. When the body is longer, `Truncated` is true, the rest is never read, and `Error` ends with ` [truncated]`. A truncated 400 never counts as the malformed-token answer. A 404 blob download also matches `ErrItemNotFound` with `errors.Is`, and a 409 or 412 root commit matches `ErrGenerationConflict`. A non-200 discovery response matches only `ErrDiscoveryFailed`, including 401 and 403; a 401, a 403, or a malformed-token 400 from every other request matches `ErrUnauthorized`.
- A failed session token renewal wraps `ErrUnauthorized` only when no device token is configured or the auth service rejects the device token with 401, 403, or a malformed-token 400. Network failures, cancellation, deadlines, other statuses, and empty token responses keep their own errors: `errors.As` finds `*url.Error` or `*StatusError`, and `errors.Is` finds `context.Canceled` or `context.DeadlineExceeded`. Errors contain no re-pairing instructions; applications check `errors.Is(err, cloud.ErrUnauthorized)` and supply their own.
- Pairing, token renewal, discovery, root state, and root commit answers are read up to 64 KiB; a longer answer fails with a `response exceeds 65536 bytes` error instead of being read. Blob downloads are documents and are read whole.
- `NewClient` returns an error when the `WithConfigFile` file, or the file `WithDefaultConfigFile` finds, cannot be read or parsed, so an unreadable credentials file is never mistaken for missing credentials. A `WithConfigFile` path that does not exist is not an error: the client starts with the `WithConfig` credentials, if any, and the first saved renewal creates the file. Finding no default config file is not an error either.
- When a renewed user token cannot be written to the config file, `RenewToken` and `PairDevice` return the new token together with an error that matches `ErrConfigNotSaved` and wraps the write error. Renewals the client makes on its own while serving a request continue with the new token and pass that error to the `WithSaveErrorHandler` function; without one, the failure is not reported and the next process renews again. The client keeps the new token in memory in every case.
- All network calls obey the caller's `context.Context` cancellation and deadlines.
- `UpdateDocumentFiles` requires the destination's previously inspected schema hash. It preserves unchanged file references, writes sorted document-v3 and root-v4 indexes, and broadcasts the generation-checked root commit. It downloads updated files directly from the cloud before and after commit to compare bytes and document associations.
- `UpdateDocumentFilesAtRoot` also requires the caller's preflight `RootState` in `UpdateDocumentOptions.ExpectedRoot`. It rejects a changed root hash or generation before uploading, including source changes that leave the destination unchanged, and commits against the same generation. Set `ID`, `ExpectedHash`, and `Files` for the destination, and inspect every source and destination against that root snapshot. A change during staging rejects the commit with `ErrGenerationConflict`; recompute preflight instead of retrying with a newer generation. Both update methods preserve unchanged document file references and freshly verify the updated files. Callers verify document-specific source preservation and other unchanged bytes after the operation.
- Write errors return an `UpdateResult` with confirmed uploads and state: `staged` when a step before the root commit fails or the server refuses the commit, `commit-unknown` when a sent commit cannot be confirmed, `committed` when subsequent verification fails, or `verified` on success. A refused commit is one answered with a 4xx status: a 409 or 412 generation conflict, any other 4xx, a refused token that is refused again after renewal, or a refused token whose renewal fails before the commit is replayed. A refused token is a 401, a 403, or the malformed-token 400 described above. Its error keeps its classification: `errors.As` finds the commit's `*StatusError`, which matches `ErrGenerationConflict` for 409 or 412 and `ErrUnauthorized` for a refused token, and a failed renewal returns the renewal error classified as described above. A commit that gets no response, any non-200 status that is not a 4xx (such as a 5xx or a redirect that is not followed), or a 200 that does not confirm the new root is `commit-unknown`. Staged blobs remain unreferenced. Callers validate document-specific page structure and handwriting conflicts before invoking this file-level operation.
- `CreateDocument` requires a caller-chosen document ID, encoded files, and the `RootState` used for folder/title preflight. It rejects an existing ID and any root hash or generation change before uploading; the commit also checks the generation. Callers construct valid PDF metadata/content, decide title collision behavior, and handle tablet initialization of native pages. The library preserves the supplied bytes and creates no native page structures.
- Creation returns a `CreateResult` even on failure, with document ID/hash, intended root hash, preflight generation, confirmed uploads, and the same four states as file updates. The optional synchronous `OnProgress` callback runs before staging, immediately before sending the commit, after confirmation, and after fresh association/byte verification. Persist the callback's recovery identity before the commit; a callback error stops the operation, and a failure before the request returns `staged`. For `commit-unknown`, inspect the current root and document association using the recorded ID/hash before retrying, since the server may already have committed the document. Library calls do not persist progress automatically.

# Prerequisites

- [Go](https://go.dev/) 1.26 or newer.
- An active reMarkable tablet paired with cloud storage.

# Installation

```bash
go get github.com/alexgorbatchev/go-remarkable-cloud
```

# Quick Start

```go
package main

import (
	"context"
	"fmt"

	cloud "github.com/alexgorbatchev/go-remarkable-cloud"
)

func main() {
	// Automatically loads from ~/.rmapi or $RMAPI_CONFIG
	client, err := cloud.NewClient(cloud.WithDefaultConfigFile())
	if err != nil {
		panic(err)
	}

	ctx := context.Background()

	// Fetch current sync generation
	root, err := client.GetRootState(ctx)
	if err != nil {
		panic(err)
	}
	fmt.Printf("Generation: %d, Root Hash: %s\n", root.Generation, root.Hash)

	// Resolve a document by title
	item, err := client.Resolve(ctx, "Quick sheets")
	if err != nil {
		panic(err)
	}
	fmt.Printf("Resolved: %s (UUID: %s)\n", item.Metadata.VisibleName, item.ID)
}
```

# API

| Export | Signature | Description |
| :--- | :--- | :--- |
| `NewClient` | `(opts ...Option) (*Client, error)` | Constructs a reMarkable Cloud client |
| `Client.GetRootState` | `(ctx context.Context) (*RootState, error)` | Fetches the current root sync generation and schema hash |
| `Client.GetManifest` | `(ctx context.Context, hash, filename string) (*Manifest, error)` | Downloads and parses schema records for a file hash |
| `Client.GetBlob` | `(ctx context.Context, hash, filename string) ([]byte, error)` | Downloads raw blob bytes (hits disk cache if enabled) |
| `Client.GetBlobFresh` | `(ctx context.Context, hash, filename string) ([]byte, error)` | Downloads raw bytes directly from storage without reading or writing the disk cache |
| `Client.UpdateDocumentFiles` | `(ctx context.Context, id, expectedHash string, files []FileUpdate) (*UpdateResult, error)` | Adds or replaces named document files; returns progress even when staging, commit, or verification fails |
| `Client.UpdateDocumentFilesAtRoot` | `(ctx context.Context, opts UpdateDocumentOptions) (*UpdateResult, error)` | Updates files against the caller's root hash and generation; rejects concurrent source or destination changes |
| `Client.CreateDocument` | `(ctx context.Context, opts CreateDocumentOptions) (*CreateResult, error)` | Creates a separate document using the caller's root snapshot; returns recovery identity and reports progress before the commit |
| `Client.ListItems` | `(ctx context.Context, opts ...ListOption) ([]*Item, error)` | Resolves all documents and collections in cloud storage |
| `Client.Resolve` | `(ctx context.Context, query string) (*Item, error)` | Resolves an item by UUID, folder path, or visible title |
| `Client.ResolveByName` | `(ctx context.Context, name string) (*Item, error)` | Resolves the single live item with an exact visible name |
| `Client.ResolveByPath` | `(ctx context.Context, path string) (*Item, error)` | Resolves a `/`-separated folder path from the root |
| `Client.ResolveByID` | `(ctx context.Context, id string) (*Item, error)` | UUID resolution with an O(n) root-manifest scan |
| `Client.PairDevice` | `(ctx context.Context, code string) (string, error)` | Exchanges 8-character pairing code for a device token |
| `Client.RenewToken` | `(ctx context.Context) (string, error)` | Mints a new short-lived session bearer token; returns it with an `ErrConfigNotSaved` error when it cannot be saved |
| `StatusError` | `struct { Op string; StatusCode int; Body string; Truncated bool; Err error }` | Rejected HTTP response with the first 4096 body bytes; `Unwrap` returns the sentinel classification in `Err`, or nil |
| `AmbiguousNameError` | `struct { Query string; Name string; Candidates []AmbiguousCandidate }` | A name or path segment matched several live items; `Error` states the name and candidate count, `Unwrap` returns `ErrAmbiguousName` |
| `AmbiguousCandidate` | `struct { Item *Item; FolderPath string; Unreachable bool }` | One matching item and the live folders above it |

# Configuration

| Option | Type | Default | Description |
| :--- | :--- | :--- | :--- |
| `WithConfigFile` | `string` | none | Path to rmapi token file; `NewClient` fails when it exists but cannot be read or parsed |
| `WithDefaultConfigFile` | | none | Automatically loads from candidate rmapi paths |
| `WithConfig` | `*Config` | none | In-memory token credentials struct |
| `WithSaveOnRenew` | `bool` | `true` | Write renewed tokens back to the config file |
| `WithSaveErrorHandler` | `func(err error)` | none | Receives the `ErrConfigNotSaved` error when a renewal made while serving a request cannot be saved |
| `WithCacheDir` | `string` | none | Path to the verified, owner-only blob disk cache |
| `WithAutoRenew` | `bool` | `true` | Automatically refresh the session token when a request is refused with 401, 403, or the malformed-token 400, then replay the request once |
| `WithHTTPClient` | `*http.Client` | default client | Custom HTTP transport for logging or proxies |

# License

MIT License (c) 2026 Alex Gorbatchev
