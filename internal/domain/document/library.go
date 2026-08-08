package document

import (
	"crypto/rand"
	"encoding/hex"
	"strings"
	"time"
)

const (
	DocumentStatusActive = "active"
	DocumentSourceAgent  = "agent_generated"
	DocumentSourceUpload = "user_upload"
)

// Document is the stable library-level record for a local knowledge artifact.
type Document struct {
	ID              string    `json:"id"`
	Title           string    `json:"title"`
	DocType         string    `json:"doc_type"`
	Source          string    `json:"source"`
	Status          string    `json:"status"`
	CreatedBy       string    `json:"created_by"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
	LatestVersion   int       `json:"latest_version"`
	LatestVersionID string    `json:"latest_version_id,omitempty"`
}

// DocumentVersion stores the full markdown content for one immutable version.
type DocumentVersion struct {
	ID         string                 `json:"id"`
	DocumentID string                 `json:"document_id"`
	Version    int                    `json:"version"`
	ContentMD  string                 `json:"content_md"`
	Summary    string                 `json:"summary,omitempty"`
	Metadata   map[string]interface{} `json:"metadata,omitempty"`
	CreatedAt  time.Time              `json:"created_at"`
}

// WriteRequest describes a new document write or version update.
type WriteRequest struct {
	DocumentID string                 `json:"document_id,omitempty"`
	Title      string                 `json:"title"`
	DocType    string                 `json:"doc_type"`
	Source     string                 `json:"source"`
	CreatedBy  string                 `json:"created_by"`
	ContentMD  string                 `json:"content_md"`
	Summary    string                 `json:"summary,omitempty"`
	Metadata   map[string]interface{} `json:"metadata,omitempty"`
}

// WriteResult returns the document and created version.
type WriteResult struct {
	Document Document        `json:"document"`
	Version  DocumentVersion `json:"version"`
	Created  bool            `json:"created"`
}

// LibraryRepo is the storage boundary for the local document library.
type LibraryRepo interface {
	Write(req WriteRequest) (WriteResult, error)
	List() ([]Document, error)
	Get(documentID string) (Document, DocumentVersion, error)
	GetVersion(versionID string) (DocumentVersion, error)
}

func NormalizeWriteRequest(req WriteRequest) WriteRequest {
	req.Title = strings.TrimSpace(req.Title)
	req.DocType = strings.TrimSpace(req.DocType)
	req.Source = strings.TrimSpace(req.Source)
	req.CreatedBy = strings.TrimSpace(req.CreatedBy)
	req.ContentMD = strings.TrimSpace(req.ContentMD)
	req.Summary = strings.TrimSpace(req.Summary)
	if req.DocType == "" {
		req.DocType = "note"
	}
	if req.Source == "" {
		req.Source = DocumentSourceAgent
	}
	if req.CreatedBy == "" {
		req.CreatedBy = "agent"
	}
	if req.Metadata == nil {
		req.Metadata = map[string]interface{}{}
	}
	return req
}

func NewID(prefix string) string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return prefix + "_" + strings.ReplaceAll(time.Now().UTC().Format("20060102150405.000000000"), ".", "")
	}
	return prefix + "_" + hex.EncodeToString(b[:])
}
