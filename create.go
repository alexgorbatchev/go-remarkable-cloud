package cloud

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"strings"
)

// CreateDocumentOptions supplies a new document's identity and already encoded files.
// ExpectedRoot must be the snapshot used by the caller for folder/title preflight.
// OnProgress runs synchronously before staging, before the commit request, after a
// confirmed commit, and after verification. An error stops the operation. In
// particular, failure to persist commit-unknown progress prevents the commit.
type CreateDocumentOptions struct {
	ID           string
	ExpectedRoot RootState
	Files        []FileUpdate
	OnProgress   func(CreateResult) error
}

// CreateResult identifies the document and intended commit even when the outcome
// is uncertain. Generation is the preflight generation sent in the commit request.
// Uploaded contains files whose staged bytes were freshly downloaded and confirmed.
type CreateResult struct {
	State        UpdateState `json:"state"`
	ID           string      `json:"id"`
	DocumentHash string      `json:"documentHash"`
	RootHash     string      `json:"rootHash"`
	Generation   int64       `json:"generation"`
	Uploaded     []string    `json:"uploaded"`
}

// CreateDocument stages files and adds their document to a generation-checked root
// without replacing an existing ID. It verifies the association and every file's
// bytes through fresh downloads. The caller constructs valid metadata/content and
// handles title collisions and native page initialization; this method encodes no
// PDF or native page structures. Inspect ID and DocumentHash before retrying an
// uncertain commit, since the server may have committed it despite an error.
func (c *Client) CreateDocument(ctx context.Context, opts CreateDocumentOptions) (*CreateResult, error) {
	result := &CreateResult{State: UpdateStaged, ID: opts.ID, Generation: opts.ExpectedRoot.Generation}
	update, err := c.prepareDocumentCreation(ctx, opts)
	if err != nil {
		return result, err
	}
	result.DocumentHash = update.docHash
	result.RootHash = update.rootHash
	if err := reportCreationProgress(opts.OnProgress, result); err != nil {
		return result, err
	}
	if err := c.stageDocumentFiles(ctx, opts.ID, update, opts.Files, &result.Uploaded); err != nil {
		return result, err
	}
	result.State = UpdateCommitUnknown
	if err := reportCreationProgress(opts.OnProgress, result); err != nil {
		// The commit request has not been sent, so its outcome is still known.
		result.State = UpdateStaged
		return result, err
	}
	if err := c.commitRoot(ctx, update.rootHash, update.generation); err != nil {
		if errors.Is(err, ErrGenerationConflict) {
			result.State = UpdateStaged
		}
		return result, err
	}
	result.State = UpdateCommitted
	if err := reportCreationProgress(opts.OnProgress, result); err != nil {
		return result, err
	}
	if err := c.verifyDocumentFiles(ctx, opts.ID, update.docHash, opts.Files); err != nil {
		return result, err
	}
	result.State = UpdateVerified
	return result, reportCreationProgress(opts.OnProgress, result)
}

func reportCreationProgress(report func(CreateResult) error, result *CreateResult) error {
	if report == nil {
		return nil
	}
	progress := *result
	progress.Uploaded = slices.Clone(result.Uploaded)
	if err := report(progress); err != nil {
		return fmt.Errorf("recording creation progress: %w", err)
	}
	return nil
}

func (c *Client) prepareDocumentCreation(ctx context.Context, opts CreateDocumentOptions) (*documentUpdate, error) {
	if opts.ID == "" || strings.ContainsAny(opts.ID, "\r\n:/\\.") {
		return nil, fmt.Errorf("invalid document ID %q", opts.ID)
	}
	hash, err := hex.DecodeString(opts.ExpectedRoot.Hash)
	if err != nil || len(hash) != sha256.Size || opts.ExpectedRoot.Generation < 0 {
		return nil, fmt.Errorf("valid expected root hash and generation are required")
	}
	if err := validateDocumentFiles(opts.ID, opts.Files); err != nil {
		return nil, err
	}
	root, err := c.GetRootState(ctx)
	if err != nil {
		return nil, err
	}
	if root.Hash != opts.ExpectedRoot.Hash || root.Generation != opts.ExpectedRoot.Generation {
		return nil, fmt.Errorf("%w: root changed since creation preflight", ErrGenerationConflict)
	}
	manifest, err := c.freshManifest(ctx, root.Hash, "root.docSchema")
	if err != nil {
		return nil, err
	}
	if manifest.Find(opts.ID) != nil {
		return nil, fmt.Errorf("destination %s already exists", opts.ID)
	}
	entries := make([]SchemaEntry, 0, len(opts.Files))
	for _, file := range opts.Files {
		entries = append(entries, SchemaEntry{ID: file.Name, Hash: contentHash(file.Data), Type: "0", Size: int64(len(file.Data))})
	}
	docBytes, docHash, size, err := encodeCloudManifest(entries, false)
	if err != nil {
		return nil, err
	}
	manifest.Entries = append(manifest.Entries, SchemaEntry{ID: opts.ID, Hash: docHash, Type: "0", Subfiles: len(entries), Size: size})
	rootBytes, rootHash, _, err := encodeCloudManifest(manifest.Entries, true)
	if err != nil {
		return nil, err
	}
	return &documentUpdate{generation: root.Generation, docHash: docHash, docBytes: docBytes, rootHash: rootHash, rootBytes: rootBytes}, nil
}
