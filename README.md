`go-remarkable-cloud` is a self-contained Go client library for reMarkable Cloud Sync v3, providing authentication, service discovery, document resolution, content-addressed blob streaming, and disk caching.

# What It Does

- **Full Sync v3 implementation**: Connects directly to reMarkable Cloud Sync v3 endpoints (`/sync/v3/root`, `/sync/v3/files/{hash}`).
- **Content-addressed disk caching**: Stores downloaded blobs and manifests by SHA-256 hash, eliminating duplicate network downloads.
- **Generation-checked document writes**: Stages file updates, verifies uploaded bytes, and commits document and root manifests while detecting concurrent root changes.
- **Fast O(1) document resolution**: Resolves documents by UUID, hierarchical path, or title directly from `root.docSchema` without full-library scanning.
- **Automated token lifecycle**: Handles 8-character device pairing and transparent user token renewal on HTTP 401 responses.
- **Zero external dependencies**: Built using standard Go library primitives (`net/http`, `crypto/rand`, `encoding/json`).

# How It Works

- Loads existing credentials from `$XDG_CONFIG_HOME/remarkable-cli/config.json` or `~/.rmapi`, or pairs a new device token.
- Queries service discovery endpoints to resolve active auth and storage hosts.
- Fetches the root generation state and line-delimited schema manifests.
- Downloads content-addressed file blobs (PDF stationery, EPUB, content schemas, `.rm` vector strokes) and deserializes metadata.

# How it Really Works

- When a cache directory is configured via `WithCacheDir`, `GetBlob` checks for `$CACHE_DIR/blobs/<hash>` before making network calls and writes new blobs atomically using temporary files.
- `ResolveByID` inspects `root.docSchema` directly to locate a single document's schema hash, reducing network calls from ~150 requests down to 3.
- Client instances are safe for concurrent use across multiple goroutines; a single client should be shared across the lifetime of an application.
- On HTTP 401 or 403 responses, the client automatically acquires a fresh session token and replays the failed request before returning an error.
- All network calls obey the caller's `context.Context` cancellation and deadlines.
- `UpdateDocumentFiles` requires the destination's previously inspected schema hash. It preserves unchanged file references, writes sorted document-v3 and root-v4 indexes, and broadcasts the generation-checked root commit. It downloads updated files directly from the cloud before and after commit to compare bytes and document associations.
- Write errors return an `UpdateResult` with confirmed uploads and state: `staged` before a successful root commit, `commit-unknown` when a commit attempt cannot be confirmed, `committed` when subsequent verification fails, or `verified` on success. A rejected generation check wraps `ErrGenerationConflict`; staged blobs remain unreferenced. Callers validate document-specific page structure and handwriting conflicts before invoking this file-level operation.

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
| `Client.ListItems` | `(ctx context.Context, opts ...ListOption) ([]*Item, error)` | Resolves all documents and collections in cloud storage |
| `Client.Resolve` | `(ctx context.Context, query string) (*Item, error)` | Resolves an item by UUID, folder path, or visible title |
| `Client.ResolveByID` | `(ctx context.Context, id string) (*Item, error)` | Fast O(1) resolution by document UUID |
| `Client.PairDevice` | `(ctx context.Context, code string) (string, error)` | Exchanges 8-character pairing code for a device token |
| `Client.RenewToken` | `(ctx context.Context) (string, error)` | Mints a new short-lived session bearer token |

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
