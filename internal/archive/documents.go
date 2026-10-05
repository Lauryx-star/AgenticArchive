package archive

import (
	"context"
	"fmt"
	"strings"
)

type DocumentListOptions struct {
	Path, Status, Sort string
	Page, Limit        int
}

type DocumentSummary struct {
	Document
	OCRPages  int `json:"ocr_pages"`
	TextPages int `json:"text_pages"`
}

type DocumentList struct {
	Documents []DocumentSummary `json:"documents"`
	Total     int               `json:"total"`
	Page      int               `json:"page"`
	Limit     int               `json:"limit"`
}

// ListDocuments reads only one requested page, including documents not yet searchable.
func (s *Store) ListDocuments(ctx context.Context, o DocumentListOptions) (DocumentList, error) {
	result := DocumentList{Documents: []DocumentSummary{}, Page: o.Page, Limit: o.Limit}
	if o.Page < 1 || o.Page > 100000 || o.Limit < 1 || o.Limit > 100 || len(o.Path) > 1000 {
		return result, fmt.Errorf("invalid document pagination or path filter")
	}
	switch o.Status {
	case "", "ready", "queued", "processing", "error":
	default:
		return result, fmt.Errorf("invalid document status")
	}
	order := "d.path COLLATE NOCASE,d.id"
	switch o.Sort {
	case "", "path":
	case "modified":
		order = "d.modified DESC,d.path COLLATE NOCASE,d.id"
	default:
		return result, fmt.Errorf("invalid document sort")
	}
	where := []string{"1=1"}
	args := []any{}
	if o.Path != "" {
		where = append(where, "instr(lower(d.path),lower(?))>0")
		args = append(args, o.Path)
	}
	if o.Status != "" {
		where = append(where, "d.status=?")
		args = append(args, o.Status)
	}
	clause := strings.Join(where, " AND ")
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return result, err
	}
	defer tx.Rollback()
	if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM documents d WHERE "+clause, args...).Scan(&result.Total); err != nil {
		return result, err
	}
	args = append(args, o.Limit, (o.Page-1)*o.Limit)
	rows, err := tx.QueryContext(ctx, `SELECT d.id,d.path,d.size,d.modified,d.fingerprint,d.status,d.error,d.pages,
 (SELECT count(*) FROM pages p WHERE p.document_id=d.id AND p.ocr=1),
 (SELECT count(*) FROM pages p WHERE p.document_id=d.id AND trim(p.text)!='')
 FROM documents d WHERE `+clause+" ORDER BY "+order+" LIMIT ? OFFSET ?", args...)
	if err != nil {
		return result, err
	}
	for rows.Next() {
		var d DocumentSummary
		if err = rows.Scan(&d.ID, &d.Path, &d.Size, &d.Modified, &d.Fingerprint, &d.Status, &d.Error, &d.Pages, &d.OCRPages, &d.TextPages); err != nil {
			rows.Close()
			return result, err
		}
		result.Documents = append(result.Documents, d)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return result, err
	}
	return result, tx.Commit()
}
