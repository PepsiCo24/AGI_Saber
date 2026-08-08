package chat

import (
	"fmt"

	"agi-assistant/internal/domain/document"
	"agi-assistant/internal/domain/rag"
)

type DocumentWriteResult struct {
	document.WriteResult
	Ingest *rag.IngestResult `json:"ingest,omitempty"`
}

func (a *UnifiedAgent) WriteDocument(req document.WriteRequest, ingestToRAG bool) (DocumentWriteResult, error) {
	if a.repos.docs == nil {
		return DocumentWriteResult{}, fmt.Errorf("document library not configured")
	}
	wr, err := a.repos.docs.Write(req)
	if err != nil {
		return DocumentWriteResult{}, err
	}
	out := DocumentWriteResult{WriteResult: wr}
	if ingestToRAG {
		ingested := a.rag.IngestWithMetadata(wr.Version.ContentMD, rag.IngestMetadata{
			DocumentID: wr.Document.ID,
			VersionID:  wr.Version.ID,
			Section:    wr.Document.DocType,
		})
		out.Ingest = &ingested
	}
	return out, nil
}

func (a *UnifiedAgent) ListDocuments() ([]document.Document, error) {
	if a.repos.docs == nil {
		return nil, fmt.Errorf("document library not configured")
	}
	return a.repos.docs.List()
}

func (a *UnifiedAgent) GetDocument(documentID string) (document.Document, document.DocumentVersion, error) {
	if a.repos.docs == nil {
		return document.Document{}, document.DocumentVersion{}, fmt.Errorf("document library not configured")
	}
	return a.repos.docs.Get(documentID)
}

func (a *UnifiedAgent) IngestDocument(documentID, versionID string) (rag.IngestResult, error) {
	if a.repos.docs == nil {
		return rag.IngestResult{}, fmt.Errorf("document library not configured")
	}
	var ver document.DocumentVersion
	var err error
	if versionID != "" {
		ver, err = a.repos.docs.GetVersion(versionID)
	} else {
		_, ver, err = a.repos.docs.Get(documentID)
	}
	if err != nil {
		return rag.IngestResult{}, err
	}
	if documentID == "" {
		documentID = ver.DocumentID
	}
	return a.rag.IngestWithMetadata(ver.ContentMD, rag.IngestMetadata{
		DocumentID: documentID,
		VersionID:  ver.ID,
		Section:    "document",
	}), nil
}
