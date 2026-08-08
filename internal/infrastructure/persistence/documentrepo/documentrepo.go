package documentrepo

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"agi-assistant/internal/domain/document"
)

type Store struct {
	db      *sql.DB
	baseDir string
	mu      sync.Mutex
}

type Repo = document.LibraryRepo

func NewStore(db *sql.DB, baseDir string) *Store {
	if baseDir == "" {
		baseDir = ".data/documents"
	}
	return &Store{db: db, baseDir: baseDir}
}

func (s *Store) Write(req document.WriteRequest) (document.WriteResult, error) {
	req = document.NormalizeWriteRequest(req)
	if req.Title == "" {
		return document.WriteResult{}, fmt.Errorf("title is required")
	}
	if req.ContentMD == "" {
		return document.WriteResult{}, fmt.Errorf("content_md is required")
	}
	if s.db != nil {
		return s.writePG(req)
	}
	return s.writeLocal(req)
}

func (s *Store) List() ([]document.Document, error) {
	if s.db != nil {
		return s.listPG()
	}
	idx, err := s.loadLocalIndex()
	if err != nil {
		return nil, err
	}
	docs := append([]document.Document(nil), idx.Documents...)
	sort.Slice(docs, func(i, j int) bool { return docs[i].UpdatedAt.After(docs[j].UpdatedAt) })
	return docs, nil
}

func (s *Store) Get(documentID string) (document.Document, document.DocumentVersion, error) {
	if documentID == "" {
		return document.Document{}, document.DocumentVersion{}, fmt.Errorf("document_id is required")
	}
	if s.db != nil {
		return s.getPG(documentID)
	}
	doc, err := s.getLocalDocument(documentID)
	if err != nil {
		return document.Document{}, document.DocumentVersion{}, err
	}
	ver, err := s.GetVersion(doc.LatestVersionID)
	if err != nil {
		return document.Document{}, document.DocumentVersion{}, err
	}
	return doc, ver, nil
}

func (s *Store) GetVersion(versionID string) (document.DocumentVersion, error) {
	if versionID == "" {
		return document.DocumentVersion{}, fmt.Errorf("version_id is required")
	}
	if s.db != nil {
		return s.getVersionPG(versionID)
	}
	return s.getVersionLocal(versionID)
}

func (s *Store) writePG(req document.WriteRequest) (document.WriteResult, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return document.WriteResult{}, err
	}
	defer tx.Rollback()

	now := time.Now().UTC()
	created := req.DocumentID == ""
	docID := req.DocumentID
	version := 1
	if created {
		docID = document.NewID("doc")
		if _, err := tx.Exec(
			`INSERT INTO documents (id, title, doc_type, source, status, created_by, created_at, updated_at)
			 VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
			docID, req.Title, req.DocType, req.Source, document.DocumentStatusActive, req.CreatedBy, now, now,
		); err != nil {
			return document.WriteResult{}, err
		}
	} else {
		if err := tx.QueryRow(`SELECT COALESCE(MAX(version), 0) + 1 FROM document_versions WHERE document_id = $1`, docID).Scan(&version); err != nil {
			return document.WriteResult{}, err
		}
		if _, err := tx.Exec(
			`UPDATE documents SET title = $1, doc_type = $2, source = $3, status = $4, updated_at = $5 WHERE id = $6`,
			req.Title, req.DocType, req.Source, document.DocumentStatusActive, now, docID,
		); err != nil {
			return document.WriteResult{}, err
		}
	}

	versionID := document.NewID("ver")
	metaJSON, _ := json.Marshal(req.Metadata)
	if _, err := tx.Exec(
		`INSERT INTO document_versions (id, document_id, version, content_md, summary, metadata, created_at)
		 VALUES ($1, $2, $3, $4, NULLIF($5, ''), $6, $7)`,
		versionID, docID, version, req.ContentMD, req.Summary, metaJSON, now,
	); err != nil {
		return document.WriteResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return document.WriteResult{}, err
	}

	doc, ver, err := s.getPG(docID)
	if err != nil {
		return document.WriteResult{}, err
	}
	return document.WriteResult{Document: doc, Version: ver, Created: created}, nil
}

func (s *Store) listPG() ([]document.Document, error) {
	rows, err := s.db.Query(
		`SELECT d.id, d.title, d.doc_type, d.source, d.status, d.created_by, d.created_at, d.updated_at,
		        COALESCE(v.version, 0), COALESCE(v.id, '')
		   FROM documents d
		   LEFT JOIN LATERAL (
		     SELECT id, version FROM document_versions WHERE document_id = d.id ORDER BY version DESC LIMIT 1
		   ) v ON true
		  WHERE d.status <> 'deleted'
		  ORDER BY d.updated_at DESC`,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var docs []document.Document
	for rows.Next() {
		var d document.Document
		if err := rows.Scan(&d.ID, &d.Title, &d.DocType, &d.Source, &d.Status, &d.CreatedBy, &d.CreatedAt, &d.UpdatedAt, &d.LatestVersion, &d.LatestVersionID); err == nil {
			docs = append(docs, d)
		}
	}
	return docs, rows.Err()
}

func (s *Store) getPG(documentID string) (document.Document, document.DocumentVersion, error) {
	var d document.Document
	if err := s.db.QueryRow(
		`SELECT d.id, d.title, d.doc_type, d.source, d.status, d.created_by, d.created_at, d.updated_at,
		        COALESCE(v.version, 0), COALESCE(v.id, '')
		   FROM documents d
		   LEFT JOIN LATERAL (
		     SELECT id, version FROM document_versions WHERE document_id = d.id ORDER BY version DESC LIMIT 1
		   ) v ON true
		  WHERE d.id = $1`, documentID,
	).Scan(&d.ID, &d.Title, &d.DocType, &d.Source, &d.Status, &d.CreatedBy, &d.CreatedAt, &d.UpdatedAt, &d.LatestVersion, &d.LatestVersionID); err != nil {
		return document.Document{}, document.DocumentVersion{}, err
	}
	v, err := s.getVersionPG(d.LatestVersionID)
	return d, v, err
}

func (s *Store) getVersionPG(versionID string) (document.DocumentVersion, error) {
	var v document.DocumentVersion
	var metaJSON []byte
	if err := s.db.QueryRow(
		`SELECT id, document_id, version, content_md, COALESCE(summary, ''), COALESCE(metadata, '{}'::jsonb), created_at
		   FROM document_versions WHERE id = $1`, versionID,
	).Scan(&v.ID, &v.DocumentID, &v.Version, &v.ContentMD, &v.Summary, &metaJSON, &v.CreatedAt); err != nil {
		return document.DocumentVersion{}, err
	}
	_ = json.Unmarshal(metaJSON, &v.Metadata)
	return v, nil
}

type localIndex struct {
	Documents []document.Document `json:"documents"`
}

func (s *Store) writeLocal(req document.WriteRequest) (document.WriteResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	idx, err := s.loadLocalIndexLocked()
	if err != nil {
		return document.WriteResult{}, err
	}
	now := time.Now().UTC()
	created := req.DocumentID == ""
	docID := req.DocumentID
	docIdx := -1
	for i, d := range idx.Documents {
		if d.ID == docID {
			docIdx = i
			break
		}
	}
	if created {
		docID = document.NewID("doc")
		idx.Documents = append(idx.Documents, document.Document{
			ID:        docID,
			Title:     req.Title,
			DocType:   req.DocType,
			Source:    req.Source,
			Status:    document.DocumentStatusActive,
			CreatedBy: req.CreatedBy,
			CreatedAt: now,
			UpdatedAt: now,
		})
		docIdx = len(idx.Documents) - 1
	} else if docIdx < 0 {
		return document.WriteResult{}, fmt.Errorf("document not found: %s", docID)
	}

	doc := idx.Documents[docIdx]
	version := doc.LatestVersion + 1
	versionID := document.NewID("ver")
	ver := document.DocumentVersion{
		ID:         versionID,
		DocumentID: docID,
		Version:    version,
		ContentMD:  req.ContentMD,
		Summary:    req.Summary,
		Metadata:   req.Metadata,
		CreatedAt:  now,
	}

	doc.Title = req.Title
	doc.DocType = req.DocType
	doc.Source = req.Source
	doc.Status = document.DocumentStatusActive
	doc.UpdatedAt = now
	doc.LatestVersion = version
	doc.LatestVersionID = versionID
	idx.Documents[docIdx] = doc

	dir := filepath.Join(s.baseDir, docID)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return document.WriteResult{}, err
	}
	if err := writeJSON(filepath.Join(dir, versionID+".json"), ver); err != nil {
		return document.WriteResult{}, err
	}
	if err := os.WriteFile(filepath.Join(dir, versionID+".md"), []byte(req.ContentMD), 0600); err != nil {
		return document.WriteResult{}, err
	}
	if err := s.saveLocalIndexLocked(idx); err != nil {
		return document.WriteResult{}, err
	}
	return document.WriteResult{Document: doc, Version: ver, Created: created}, nil
}

func (s *Store) loadLocalIndex() (localIndex, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.loadLocalIndexLocked()
}

func (s *Store) loadLocalIndexLocked() (localIndex, error) {
	path := filepath.Join(s.baseDir, "index.json")
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return localIndex{}, nil
	}
	if err != nil {
		return localIndex{}, err
	}
	var idx localIndex
	if err := json.Unmarshal(data, &idx); err != nil {
		return localIndex{}, err
	}
	return idx, nil
}

func (s *Store) saveLocalIndexLocked(idx localIndex) error {
	if err := os.MkdirAll(s.baseDir, 0700); err != nil {
		return err
	}
	return writeJSON(filepath.Join(s.baseDir, "index.json"), idx)
}

func (s *Store) getLocalDocument(documentID string) (document.Document, error) {
	idx, err := s.loadLocalIndex()
	if err != nil {
		return document.Document{}, err
	}
	for _, d := range idx.Documents {
		if d.ID == documentID {
			return d, nil
		}
	}
	return document.Document{}, fmt.Errorf("document not found: %s", documentID)
}

func (s *Store) getVersionLocal(versionID string) (document.DocumentVersion, error) {
	idx, err := s.loadLocalIndex()
	if err != nil {
		return document.DocumentVersion{}, err
	}
	for _, d := range idx.Documents {
		path := filepath.Join(s.baseDir, d.ID, versionID+".json")
		data, err := os.ReadFile(path)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return document.DocumentVersion{}, err
		}
		var v document.DocumentVersion
		if err := json.Unmarshal(data, &v); err != nil {
			return document.DocumentVersion{}, err
		}
		if b, err := os.ReadFile(filepath.Join(s.baseDir, d.ID, versionID+".md")); err == nil {
			v.ContentMD = string(b)
		}
		return v, nil
	}
	return document.DocumentVersion{}, fmt.Errorf("version not found: %s", versionID)
}

func writeJSON(path string, v interface{}) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0600)
}
