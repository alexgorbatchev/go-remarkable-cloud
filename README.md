# go-remarkable-cloud

[![Go Reference](https://pkg.go.dev/badge/github.com/alexgorbatchev/go-remarkable-cloud.svg)](https://pkg.go.dev/github.com/alexgorbatchev/go-remarkable-cloud)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](https://opensource.org/licenses/MIT)

A pure, self-contained Go client library for the **reMarkable Cloud Sync v3 API**. Built using only the Go standard library (`net/http`, `encoding/json`, `crypto/rand`) with zero external runtime dependencies.

## Features

- **Full Sync v3 Support**: Interacts directly with the reMarkable Cloud Sync v3 storage engine (`/sync/v3/root`, `/sync/v3/files/{hash}`).
- **Zero Dependencies**: Pure Go with no external modules or Python dependencies.
- **Config Parser & Writer**: Fully compatible with existing `~/.rmapi` token configuration files (including `RMAPI_CONFIG` and `XDG_CONFIG_HOME` resolution).
- **Automated Auth Lifecycle**: Supports 8-digit device code pairing and transparent user token renewal on initialization and HTTP 401 responses.
- **Dynamic Service Discovery**: Integrates with reMarkable's discovery service with automatic fallback to high-availability endpoints.
- **Item Hierarchy Resolver**: List documents and collections, and resolve items by **UUID**, **hierarchical path** (`"Work/Projects/Meeting Notes"`), or **visibleName**.
- **Manifest & Blob Retrieval**: Download and parse line-delimited schema records, raw file blobs (PDF, EPUB, stroke `.rm` files), and structured `.content` metadata.

---

## Installation

```bash
go get github.com/alexgorbatchev/go-remarkable-cloud
```

Requires Go 1.22+.

---

## Quick Start

### 1. Configuration & Client Setup

If you already have `~/.rmapi` configured from `rmapi`, load it directly:

```go
package main

import (
	"context"
	"fmt"
	"log"

	cloud "github.com/alexgorbatchev/go-remarkable-cloud"
)

func main() {
	// Automatically loads from ~/.rmapi or $RMAPI_CONFIG
	client, err := cloud.NewClient(cloud.WithDefaultConfigFile())
	if err != nil {
		log.Fatalf("failed to initialize client: %v", err)
	}

	ctx := context.Background()
	root, err := client.GetRootState(ctx)
	if err != nil {
		log.Fatalf("failed to fetch root state: %v", err)
	}

	fmt.Printf("Root Generation: %d (Schema Hash: %s)\n", root.Generation, root.Hash)
}
```

You can also pass tokens directly in code:

```go
client, err := cloud.NewClient(
	cloud.WithConfig(&cloud.Config{
		DeviceToken: "your-device-token",
		UserToken:   "your-user-token", // Optional: automatically minted if omitted
	}),
)
```

---

### 2. Device Pairing (New Setup)

To pair a new device using an 8-character one-time code from [my.remarkable.com/pair/app](https://my.remarkable.com/pair/app):

```go
package main

import (
	"context"
	"fmt"
	"log"

	cloud "github.com/alexgorbatchev/go-remarkable-cloud"
)

func main() {
	ctx := context.Background()

	// 1. Initialize client
	client, err := cloud.NewClient(cloud.WithConfigFile("~/.rmapi"))
	if err != nil {
		log.Fatal(err)
	}

	// 2. Pair with code
	userToken, err := client.PairDevice(ctx, "abcdefgh")
	if err != nil {
		log.Fatalf("pairing failed: %v", err)
	}

	fmt.Println("Device paired successfully! User token minted.")
}
```

---

### 3. Service Discovery

Discover backend endpoints dynamically with automated fallback:

```go
endpoints, err := cloud.DiscoverEndpoints(ctx, nil, "")
if err != nil {
	// Fallback endpoints are still populated safely
	fmt.Printf("Discovery failed, falling back to: %s\n", endpoints.RawHost)
} else {
	fmt.Printf("Discovered Storage Host: %s\n", endpoints.StorageHost)
}
```

---

### 4. Resolving Documents & Collections

Resolve items using exact UUIDs, visible display names, or folder paths:

```go
ctx := context.Background()

// List all active documents and folders
items, err := client.ListItems(ctx)
if err != nil {
	log.Fatal(err)
}
for _, item := range items {
	fmt.Printf("[%s] %s (%s)\n", item.Metadata.Type, item.Metadata.VisibleName, item.ID)
}

// Resolve by exact hierarchical path
doc, err := client.ResolveByPath(ctx, "Work/2026/Daily Planner")
if err != nil {
	log.Fatalf("document not found: %v", err)
}

// Or resolve flexibly (checks UUID -> Path -> Name)
item, err := client.Resolve(ctx, "Daily Planner")
if err != nil {
	log.Fatal(err)
}
```

---

### 5. Fetching Manifests, Blobs, and Content

Once an `Item` is resolved, inspect its manifest and download contents:

```go
// 1. Download and parse document schema manifest
manifest, err := doc.GetManifest(ctx)
if err != nil {
	log.Fatal(err)
}

// 2. Download and parse {id}.content metadata
content, err := doc.GetContent(ctx)
if err != nil {
	log.Fatal(err)
}
fmt.Printf("FileType: %s, PageCount: %d\n", content.FileType, content.PageCount)

// 3. Download raw blobs (e.g. background PDF or stroke .rm file)
pdfEntry := manifest.FindSuffix(".pdf")
if pdfEntry != nil {
	pdfBytes, err := client.GetBlob(ctx, pdfEntry.Hash, pdfEntry.ID)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("Downloaded PDF: %d bytes\n", len(pdfBytes))
}
```

---

## Development & Testing

This project uses [`just`](https://github.com/casey/just) for task automation. All tests run completely offline using `net/http/httptest`.

```bash
# Run unit tests
just test

# Run linter
just lint

# Run both linter and test suite
just check
```

---

## Attribution & Acknowledgements

This library builds upon protocol reverse engineering, research, and insights established by the open-source reMarkable community:

- **[juruen/rmapi](https://github.com/juruen/rmapi)**: Pioneering Go CLI and client for the reMarkable Cloud API; defined the standard `~/.rmapi` config layout and device pairing protocol.
- **[ddvk/rmapi](https://github.com/ddvk/rmapi)**: Continued maintenance, protocol updates, and storage schema discovery for modern reMarkable software.
- **[j6k4m8/remarkapy](https://github.com/j6k4m8/remarkapy)**: Clean, typed Python library and reverse-engineering of Sync v3 endpoints, docSchema line-delimited records, and Paper Pro support.
- **[splitbrain/ReMarkableAPI](https://github.com/splitbrain/ReMarkableAPI)**: Foundational documentation of cloud authentication, token endpoints, and discovery mechanics.
- **[SamMorrowDrums/remarkable-mcp](https://github.com/SamMorrowDrums/remarkable-mcp)**: Modern cloud tool workflows and integration patterns for reMarkable devices.

---

## License

MIT License. See [LICENSE](LICENSE) for full details.
