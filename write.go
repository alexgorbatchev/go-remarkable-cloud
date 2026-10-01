package cloud

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash/crc32"
	"net/http"
	"sort"
	"strings"
)

// FileUpdate replaces or adds one file in an existing document manifest.
type FileUpdate struct {
	Name string
	Data []byte
}

// UpdateDocumentOptions binds file updates to the root and document inspected by
// the caller. ExpectedRoot must be the snapshot used for all source/destination
// preflight; ExpectedHash is the destination document hash in that snapshot.
type UpdateDocumentOptions struct {
	ID           string
	ExpectedHash string
	ExpectedRoot RootState
	Files        []FileUpdate
}

// UpdateState identifies the last confirmed phase of a document update.
type UpdateState string

const (
	UpdateStaged        UpdateState = "staged"
	UpdateCommitUnknown UpdateState = "commit-unknown"
	UpdateCommitted     UpdateState = "committed"
	UpdateVerified      UpdateState = "verified"
)

// UpdateResult distinguishes staged uploads, uncertain commits, and verified commits.
// Uploaded names identify files whose uploaded bytes were confirmed by fresh downloads.
type UpdateResult struct {
	State    UpdateState
	Uploaded []string
	RootHash string
}

type documentUpdate struct {
	generation int64
	docHash    string
	docBytes   []byte
	rootHash   string
	rootBytes  []byte
}

// UpdateDocumentFiles stages and verifies file bytes before committing a generation-
// checked root update. expectedHash must be the document hash used for preflight.
// Errors return progress: staged data is unreferenced until the root commit succeeds.
func (c *Client) UpdateDocumentFiles(ctx context.Context, id, expectedHash string, files []FileUpdate) (*UpdateResult, error) {
	result := &UpdateResult{State: UpdateStaged}
	update, err := c.prepareDocumentUpdate(ctx, id, expectedHash, files)
	if err != nil {
		return result, err
	}
	return c.applyDocumentUpdate(ctx, id, update, files, result)
}

// UpdateDocumentFilesAtRoot rejects a changed caller root snapshot before uploads
// and commits against that snapshot's generation. Use it when preflight depends
// on other documents as well as the destination. Recompute preflight after a
// generation conflict; this operation never adopts a newer root or retries a commit.
// It returns the same progress states and fresh file verification as UpdateDocumentFiles.
func (c *Client) UpdateDocumentFilesAtRoot(ctx context.Context, opts UpdateDocumentOptions) (*UpdateResult, error) {
	result := &UpdateResult{State: UpdateStaged}
	if err := validateExpectedRoot(opts.ExpectedRoot); err != nil {
		return result, err
	}
	root, err := c.GetRootState(ctx)
	if err != nil {
		return result, err
	}
	if root.Hash != opts.ExpectedRoot.Hash || root.Generation != opts.ExpectedRoot.Generation {
		return result, fmt.Errorf("%w: root changed since update preflight", ErrGenerationConflict)
	}
	update, err := c.prepareDocumentUpdateFromRoot(ctx, opts.ID, opts.ExpectedHash, opts.Files, root)
	if err != nil {
		return result, err
	}
	return c.applyDocumentUpdate(ctx, opts.ID, update, opts.Files, result)
}

func (c *Client) applyDocumentUpdate(ctx context.Context, id string, update *documentUpdate, files []FileUpdate, result *UpdateResult) (*UpdateResult, error) {
	if err := c.stageDocumentFiles(ctx, id, update, files, &result.Uploaded); err != nil {
		return result, err
	}
	result.RootHash = update.rootHash
	result.State = UpdateCommitUnknown
	if err := c.commitRoot(ctx, update.rootHash, update.generation); err != nil {
		if errors.Is(err, ErrGenerationConflict) {
			result.State = UpdateStaged
		}
		return result, err
	}
	result.State = UpdateCommitted
	if err := c.verifyDocumentFiles(ctx, id, update.docHash, files); err != nil {
		return result, err
	}
	result.State = UpdateVerified
	return result, nil
}

func (c *Client) prepareDocumentUpdate(ctx context.Context, id, expectedHash string, files []FileUpdate) (*documentUpdate, error) {
	if err := validateDocumentUpdateInput(id, expectedHash, files); err != nil {
		return nil, err
	}
	root, err := c.GetRootState(ctx)
	if err != nil {
		return nil, err
	}
	return c.prepareDocumentUpdateFromRoot(ctx, id, expectedHash, files, root)
}

func (c *Client) prepareDocumentUpdateFromRoot(ctx context.Context, id, expectedHash string, files []FileUpdate, root *RootState) (*documentUpdate, error) {
	if err := validateDocumentUpdateInput(id, expectedHash, files); err != nil {
		return nil, err
	}
	rootManifest, err := c.freshManifest(ctx, root.Hash, "root.docSchema")
	if err != nil {
		return nil, err
	}
	entry := rootManifest.Find(id)
	if entry == nil || entry.Hash != expectedHash {
		return nil, fmt.Errorf("destination %s changed since preflight", id)
	}
	manifest, err := c.freshManifest(ctx, entry.Hash, id+".docSchema")
	if err != nil {
		return nil, err
	}
	if err := validateDocumentFiles(id, files); err != nil {
		return nil, err
	}
	for _, file := range files {
		hash := contentHash(file.Data)
		if old := manifest.Find(file.Name); old != nil {
			old.Hash = hash
			old.Size = int64(len(file.Data))
		} else {
			manifest.Entries = append(manifest.Entries, SchemaEntry{ID: file.Name, Hash: hash, Type: "0", Size: int64(len(file.Data))})
		}
	}
	docBytes, docHash, size, err := encodeCloudManifest(manifest.Entries, false)
	if err != nil {
		return nil, err
	}
	entry.Hash = docHash
	entry.Subfiles = 0
	for _, file := range manifest.Entries {
		if file.ID != "." {
			entry.Subfiles++
		}
	}
	entry.Size = size
	rootBytes, rootHash, _, err := encodeCloudManifest(rootManifest.Entries, true)
	if err != nil {
		return nil, err
	}
	return &documentUpdate{generation: root.Generation, docHash: docHash, docBytes: docBytes, rootHash: rootHash, rootBytes: rootBytes}, nil
}

func validateDocumentUpdateInput(id, expectedHash string, files []FileUpdate) error {
	if id == "" || expectedHash == "" || len(files) == 0 {
		return fmt.Errorf("document ID, expected hash, and files are required")
	}
	return nil
}

func validateExpectedRoot(root RootState) error {
	hash, err := hex.DecodeString(root.Hash)
	if err != nil || len(hash) != sha256.Size || root.Generation < 0 {
		return fmt.Errorf("valid expected root hash and generation are required")
	}
	return nil
}

func validateDocumentFiles(id string, files []FileUpdate) error {
	if len(files) == 0 {
		return fmt.Errorf("document files are required")
	}
	seen := make(map[string]bool)
	for _, file := range files {
		if !strings.HasPrefix(file.Name, id+"/") && !strings.HasPrefix(file.Name, id+".") {
			return fmt.Errorf("file %q does not belong to %s", file.Name, id)
		}
		if strings.ContainsAny(file.Name, "\r\n:") || seen[file.Name] {
			return fmt.Errorf("invalid or duplicate file %q", file.Name)
		}
		seen[file.Name] = true
	}
	return nil
}

func (c *Client) stageDocumentFiles(ctx context.Context, id string, update *documentUpdate, files []FileUpdate, uploaded *[]string) error {
	for _, file := range files {
		if err := c.putVerifiedBlob(ctx, contentHash(file.Data), file.Name, file.Data); err != nil {
			return fmt.Errorf("staging %s: %w", file.Name, err)
		}
		*uploaded = append(*uploaded, file.Name)
	}
	if err := c.putVerifiedBlob(ctx, update.docHash, id+".docSchema", update.docBytes); err != nil {
		return fmt.Errorf("staging document manifest: %w", err)
	}
	if err := c.putVerifiedBlob(ctx, update.rootHash, "root.docSchema", update.rootBytes); err != nil {
		return fmt.Errorf("staging root manifest: %w", err)
	}
	return nil
}

func (c *Client) verifyDocumentFiles(ctx context.Context, id, docHash string, files []FileUpdate) error {
	current, err := c.GetRootState(ctx)
	if err != nil {
		return fmt.Errorf("verifying committed root: %w", err)
	}
	currentManifest, err := c.freshManifest(ctx, current.Hash, "root.docSchema")
	if err != nil {
		return err
	}
	currentEntry := currentManifest.Find(id)
	if currentEntry == nil || currentEntry.Hash != docHash {
		return fmt.Errorf("committed destination association changed")
	}
	currentDoc, err := c.freshManifest(ctx, docHash, id+".docSchema")
	if err != nil {
		return err
	}
	for _, file := range files {
		entry := currentDoc.Find(file.Name)
		if entry == nil || entry.Hash != contentHash(file.Data) {
			return fmt.Errorf("verifying association for %s", file.Name)
		}
		data, err := c.GetBlobFresh(ctx, entry.Hash, file.Name)
		if err != nil {
			return err
		}
		if !bytes.Equal(data, file.Data) {
			return fmt.Errorf("verifying bytes for %s: mismatch", file.Name)
		}
	}
	return nil
}

func contentHash(data []byte) string {
	h := sha256.Sum256(data)
	return hex.EncodeToString(h[:])
}

func (c *Client) freshManifest(ctx context.Context, hash, name string) (*Manifest, error) {
	data, err := c.GetBlobFresh(ctx, hash, name)
	if err != nil {
		return nil, err
	}
	return ParseManifest(hash, bytes.NewReader(data))
}

// Root v4 hashes the serialized index; document v3 hashes ordered binary file hashes.
func encodeCloudManifest(entries []SchemaEntry, root bool) ([]byte, string, int64, error) {
	ordered := make([]SchemaEntry, 0, len(entries))
	var size int64
	for _, entry := range entries {
		if entry.ID == "." {
			continue
		}
		if entry.ID == "" || strings.ContainsAny(entry.ID, "\r\n:") || entry.Size < 0 {
			return nil, "", 0, fmt.Errorf("invalid schema entry %q", entry.ID)
		}
		ordered = append(ordered, entry)
		size += entry.Size
	}
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].ID < ordered[j].ID })
	var buf bytes.Buffer
	if root {
		fmt.Fprintf(&buf, "4\n0:.:%d:%d\n", len(ordered), size)
	} else {
		buf.WriteString("3\n")
	}
	hasher := sha256.New()
	for i, entry := range ordered {
		if i > 0 && ordered[i-1].ID == entry.ID {
			return nil, "", 0, fmt.Errorf("duplicate schema entry %s", entry.ID)
		}
		hash, err := hex.DecodeString(entry.Hash)
		if err != nil || len(hash) != sha256.Size {
			return nil, "", 0, fmt.Errorf("invalid hash for %s", entry.ID)
		}
		hasher.Write(hash)
		typ := entry.Type
		if typ == "" {
			typ = "0"
		}
		if root {
			typ = "0" // Schema v4 root records use the file type, including document indexes.
		}
		if strings.ContainsAny(typ, "\r\n:") {
			return nil, "", 0, fmt.Errorf("invalid entry type for %s", entry.ID)
		}
		fmt.Fprintf(&buf, "%s:%s:%s:%d:%d\n", entry.Hash, typ, entry.ID, entry.Subfiles, entry.Size)
	}
	hash := hex.EncodeToString(hasher.Sum(nil))
	if root {
		hash = contentHash(buf.Bytes())
	}
	return buf.Bytes(), hash, size, nil
}

func (c *Client) putVerifiedBlob(ctx context.Context, hash, name string, data []byte) error {
	url := strings.TrimRight(c.endpoints.StorageHost, "/") + "/sync/v3/files/" + hash
	var checksum [4]byte
	binary.BigEndian.PutUint32(checksum[:], crc32.Checksum(data, crc32.MakeTable(crc32.Castagnoli)))
	resp, err := c.executeWithAuth(ctx, func(token string) (*http.Request, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodPut, url, bytes.NewReader(data))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("rm-filename", name)
		req.Header.Set("x-goog-hash", "crc32c="+base64.StdEncoding.EncodeToString(checksum[:]))
		req.Header.Set("Content-Type", "application/octet-stream")
		if name == "root.docSchema" {
			req.Header.Set("Content-Type", "text/plain; charset=UTF-8")
		}
		return req, nil
	})
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusAccepted {
		return fmt.Errorf("upload failed with HTTP %d", resp.StatusCode)
	}
	downloaded, err := c.GetBlobFresh(ctx, hash, name)
	if err != nil {
		return err
	}
	if !bytes.Equal(downloaded, data) {
		return fmt.Errorf("uploaded bytes mismatch for %s", name)
	}
	return nil
}

func (c *Client) commitRoot(ctx context.Context, hash string, generation int64) error {
	data, err := json.Marshal(struct {
		Hash       string `json:"hash"`
		Generation int64  `json:"generation"`
		Broadcast  bool   `json:"broadcast"`
	}{hash, generation, true})
	if err != nil {
		return err
	}
	url := strings.TrimRight(c.endpoints.StorageHost, "/") + "/sync/v3/root"
	resp, err := c.executeWithAuth(ctx, func(token string) (*http.Request, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodPut, url, bytes.NewReader(data))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("rm-filename", "roothash")
		return req, nil
	})
	if err != nil {
		return fmt.Errorf("root commit outcome unknown: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusConflict || resp.StatusCode == http.StatusPreconditionFailed {
		return fmt.Errorf("%w: HTTP %d", ErrGenerationConflict, resp.StatusCode)
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("root commit failed with HTTP %d", resp.StatusCode)
	}
	var result RootState
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return fmt.Errorf("decoding root commit response: %w", err)
	}
	if result.Hash != hash || result.Generation <= generation {
		return fmt.Errorf("unexpected root commit response")
	}
	return nil
}
