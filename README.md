`go-remarkable-cloud` is a self-contained Go client library for reMarkable Cloud Sync v3, providing authentication, service discovery, document resolution, content-addressed blob streaming, and disk caching.

# What It Does

- **Full Sync v3 implementation**: Connects directly to reMarkable Cloud Sync v3 endpoints (`/sync/v3/root`, `/sync/v3/files/{hash}`).
- **Content-addressed disk caching**: Stores downloaded blobs and manifests by SHA-256 hash, eliminating duplicate network downloads.
- **Generation-checked document writes**: Stages file updates, verifies uploaded bytes, and commits document and root manifests while detecting concurrent root changes.
- **Separate document creation**: Creates a document from caller-encoded files without replacing an existing ID, with precommit recovery progress and fresh byte verification.
- **Document resolution**: UUID lookup scans the root manifest in O(n) time and fetches the matching item's manifest and metadata. Name and path lookups load metadata across the library.
- **Automated token lifecycle**: Handles 8-character device pairing and transparent user token renewal on HTTP 401 responses.
- **Zero external dependencies**: Built using standard Go library primitives (`net/http`, `crypto/rand`, `encoding/json`).

# How It Works

- Loads existing credentials from `$XDG_CONFIG_HOME/remarkable-cli/config.json` or `~/.rmapi`, or pairs a new device token.
- Queries service discovery endpoints to resolve active auth and storage hosts.
- Fetches the root generation state and line-delimited schema manifests.
- Downloads content-addressed file blobs (PDF stationery, EPUB, content schemas, `.rm` vector strokes) and deserializes metadata.

# How it Really Works

- Metadata download and JSON decoding failures return contextual errors. Listings return an error rather than incomplete results when an item's manifest or metadata cannot be read.

- When a cache directory is configured via `WithCacheDir`, `GetBlob` checks for `$CACHE_DIR/blobs/<hash>` before making network calls and writes new blobs atomically using temporary files.
- For nested root manifests, `ResolveByID` reads the root state, root manifest, matching item's manifest, and metadata. Other items' metadata is not fetched; cached blobs can avoid download requests. Flattened root manifests use the listing path.
- Client instances are safe for concurrent use across multiple goroutines; a single client should be shared across the lifetime of an application.
- On HTTP 401 or 403 responses, the client automatically acquires a fresh session token and replays the failed request before returning an error.
- Every rejected HTTP response returns a `*StatusError` carrying the operation name, status code, and the response body exactly as the server sent it; read it with `errors.As`. A 404 blob download also matches `ErrItemNotFound` with `errors.Is`, and a 409 or 412 root commit matches `ErrGenerationConflict`. A non-200 discovery response matches only `ErrDiscoveryFailed`, including 401 and 403; a 401 or 403 from every other request matches `ErrUnauthorized`.
- A failed session token renewal wraps `ErrUnauthorized` only when no device token is configured or the auth service rejects the device token with 401 or 403. Network failures, cancellation, deadlines, other statuses, and empty token responses keep their own errors: `errors.As` finds `*url.Error` or `*StatusError`, and `errors.Is` finds `context.Canceled` or `context.DeadlineExceeded`. Errors contain no re-pairing instructions; applications check `errors.Is(err, cloud.ErrUnauthorized)` and supply their own.
- All network calls obey the caller's `context.Context` cancellation and deadlines.
- `UpdateDocumentFiles` requires the destination's previously inspected schema hash. It preserves unchanged file references, writes sorted document-v3 and root-v4 indexes, and broadcasts the generation-checked root commit. It downloads updated files directly from the cloud before and after commit to compare bytes and document associations.
- `UpdateDocumentFilesAtRoot` also requires the caller's preflight `RootState` in `UpdateDocumentOptions.ExpectedRoot`. It rejects a changed root hash or generation before uploading, including source changes that leave the destination unchanged, and commits against the same generation. Set `ID`, `ExpectedHash`, and `Files` for the destination, and inspect every source and destination against that root snapshot. A change during staging rejects the commit with `ErrGenerationConflict`; recompute preflight instead of retrying with a newer generation. Both update methods preserve unchanged document file references and freshly verify the updated files. Callers verify document-specific source preservation and other unchanged bytes after the operation.
- Write errors return an `UpdateResult` with confirmed uploads and state: `staged` before a successful root commit, `commit-unknown` when a commit attempt cannot be confirmed, `committed` when subsequent verification fails, or `verified` on success. A rejected generation check wraps `ErrGenerationConflict`; staged blobs remain unreferenced. Callers validate document-specific page structure and handwriting conflicts before invoking this file-level operation.
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
| `Client.ResolveByID` | `(ctx context.Context, id string) (*Item, error)` | UUID resolution with an O(n) root-manifest scan |
| `Client.PairDevice` | `(ctx context.Context, code string) (string, error)` | Exchanges 8-character pairing code for a device token |
| `Client.RenewToken` | `(ctx context.Context) (string, error)` | Mints a new short-lived session bearer token |
| `StatusError` | `struct { Op string; StatusCode int; Body string; Err error }` | Rejected HTTP response; `Unwrap` returns the sentinel classification in `Err`, or nil |

# Configuration

| Option | Type | Default | Description |
| :--- | :--- | :--- | :--- |
| `WithConfigFile` | `string` | none | Path to rmapi token file |
| `WithDefaultConfigFile` | | none | Automatically loads from candidate rmapi paths |
| `WithConfig` | `*Config` | none | In-memory token credentials struct |
| `WithCacheDir` | `string` | none | Path to content-addressed blob disk cache |
| `WithAutoRenew` | `bool` | `true` | Automatically refresh session token on 401 |
| `WithHTTPClient` | `*http.Client` | default client | Custom HTTP transport for logging or proxies |

# License

MIT License (c) 2026 Alex Gorbatchev
