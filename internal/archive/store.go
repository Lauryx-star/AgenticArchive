package archive

import (
	"context"
	"crypto/rand"
	"database/sql"
	"fmt"
	"net/url"
	"path/filepath"
	"strings"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

type Store struct {
	db   *sql.DB
	path string
}

type Document struct {
	ID          int64  `json:"id"`
	Path        string `json:"path"`
	Size        int64  `json:"size"`
	Modified    string `json:"modified"`
	Fingerprint string `json:"-"`
	Status      string `json:"status"`
	Error       string `json:"error,omitempty"`
	Pages       int    `json:"pages"`
}

type Page struct {
	Number int    `json:"number"`
	Text   string `json:"text"`
	OCR    bool   `json:"ocr"`
}

type SnippetPart struct {
	Text  string `json:"text"`
	Match bool   `json:"match"`
}

type Hit struct {
	DocumentID   int64         `json:"document_id"`
	Path         string        `json:"path"`
	Modified     string        `json:"modified"`
	Page         int           `json:"page"`
	Snippet      string        `json:"snippet"`
	SnippetParts []SnippetPart `json:"snippet_parts"`
}

type SearchOptions struct {
	Query, After, Before, Sort string
	Page, Limit                int
}
type SearchResult struct {
	Hits  []Hit `json:"hits"`
	Total int   `json:"total"`
	Page  int   `json:"page"`
	Limit int   `json:"limit"`
}

func Open(path string) (*Store, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	u := url.URL{Scheme: "file", Path: filepath.ToSlash(abs)}
	db, err := sql.Open("sqlite3", u.String()+"?_foreign_keys=on&_busy_timeout=5000&_journal_mode=WAL&_txlock=immediate")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	_, err = db.Exec(`
PRAGMA cache_size=-4096;
CREATE TABLE IF NOT EXISTS settings (key TEXT PRIMARY KEY, value INTEGER NOT NULL);
CREATE TABLE IF NOT EXISTS archive_source (
 id INTEGER PRIMARY KEY CHECK(id=1), identity TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS documents (
 id INTEGER PRIMARY KEY, path TEXT NOT NULL UNIQUE, size INTEGER NOT NULL,
 modified TEXT NOT NULL, fingerprint TEXT NOT NULL, status TEXT NOT NULL,
 error TEXT NOT NULL DEFAULT '', pages INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE IF NOT EXISTS pages (
 id INTEGER PRIMARY KEY, document_id INTEGER NOT NULL REFERENCES documents(id) ON DELETE CASCADE,
 number INTEGER NOT NULL, text TEXT NOT NULL, ocr INTEGER NOT NULL,
 UNIQUE(document_id, number)
);
CREATE VIRTUAL TABLE IF NOT EXISTS page_search USING fts5(text, path, tokenize='unicode61');
CREATE TRIGGER IF NOT EXISTS pages_delete AFTER DELETE ON pages BEGIN
 DELETE FROM page_search WHERE rowid=old.id;
END;
CREATE TABLE IF NOT EXISTS jobs (
 id INTEGER PRIMARY KEY AUTOINCREMENT,
 document_id INTEGER NOT NULL REFERENCES documents(id) ON DELETE CASCADE,
 fingerprint TEXT NOT NULL, state TEXT NOT NULL,
 force_ocr INTEGER NOT NULL DEFAULT 0, total_pages INTEGER NOT NULL DEFAULT 0,
 error TEXT NOT NULL DEFAULT '', UNIQUE(document_id,fingerprint)
);
CREATE TABLE IF NOT EXISTS job_pages (
 job_id INTEGER NOT NULL REFERENCES jobs(id) ON DELETE CASCADE,
 number INTEGER NOT NULL, text TEXT NOT NULL, ocr INTEGER NOT NULL,
 PRIMARY KEY(job_id,number)
);
`)
	if err != nil {
		db.Close()
		return nil, fmt.Errorf("initialize database (FTS5 required): %w", err)
	}
	return &Store{db: db, path: abs}, nil
}

func (s *Store) Close() error { return s.db.Close() }

const documentColumns = "id,path,size,modified,fingerprint,status,error,pages"

func readDocument(row interface{ Scan(...any) error }) (Document, error) {
	var d Document
	err := row.Scan(&d.ID, &d.Path, &d.Size, &d.Modified, &d.Fingerprint, &d.Status, &d.Error, &d.Pages)
	return d, err
}
func (s *Store) ByPath(ctx context.Context, path string) (Document, error) {
	return readDocument(s.db.QueryRowContext(ctx, "SELECT "+documentColumns+" FROM documents WHERE path=?", path))
}
func (s *Store) Document(ctx context.Context, id int64) (Document, error) {
	return readDocument(s.db.QueryRowContext(ctx, "SELECT "+documentColumns+" FROM documents WHERE id=?", id))
}
func (s *Store) Documents(ctx context.Context) ([]Document, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT "+documentColumns+" FROM documents ORDER BY path")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	docs := []Document{}
	for rows.Next() {
		d, err := readDocument(rows)
		if err != nil {
			return nil, err
		}
		docs = append(docs, d)
	}
	return docs, rows.Err()
}
func (s *Store) Page(ctx context.Context, id int64, number int) (Page, error) {
	var p Page
	err := s.db.QueryRowContext(ctx, "SELECT p.number,p.text,p.ocr FROM pages p JOIN documents d ON d.id=p.document_id WHERE p.document_id=? AND p.number=? AND d.status='ready'", id, number).Scan(&p.Number, &p.Text, &p.OCR)
	return p, err
}

// Replace commits a whole document atomically; failed extraction never exposes partial text.
func (s *Store) Replace(ctx context.Context, d Document, pages []Page) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = replaceTx(ctx, tx, d, pages); err != nil {
		return err
	}
	return tx.Commit()
}

func replaceTx(ctx context.Context, tx *sql.Tx, d Document, pages []Page) error {
	var err error
	_, err = tx.ExecContext(ctx, `INSERT INTO documents(path,size,modified,fingerprint,status,error,pages)
VALUES(?,?,?,?,?,?,?) ON CONFLICT(path) DO UPDATE SET size=excluded.size,modified=excluded.modified,
fingerprint=excluded.fingerprint,status=excluded.status,error=excluded.error,pages=excluded.pages`,
		d.Path, d.Size, d.Modified, d.Fingerprint, d.Status, d.Error, len(pages))
	if err != nil {
		return err
	}
	if err = tx.QueryRowContext(ctx, "SELECT id FROM documents WHERE path=?", d.Path).Scan(&d.ID); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, "DELETE FROM pages WHERE document_id=?", d.ID); err != nil {
		return err
	}
	for _, p := range pages {
		res, err := tx.ExecContext(ctx, "INSERT INTO pages(document_id,number,text,ocr) VALUES(?,?,?,?)", d.ID, p.Number, p.Text, p.OCR)
		if err != nil {
			return err
		}
		id, err := res.LastInsertId()
		if err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, "INSERT INTO page_search(rowid,text,path) VALUES(?,?,?)", id, p.Text, d.Path); err != nil {
			return err
		}
	}
	return nil
}
func (s *Store) Delete(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, "DELETE FROM documents WHERE id=?", id)
	return err
}

// SearchExpression treats input as literal words and quoted phrases, never raw FTS syntax.
func SearchExpression(input string) (string, error) {
	var parts []string
	for input = strings.TrimSpace(input); input != ""; input = strings.TrimSpace(input) {
		var term string
		if input[0] == '"' {
			end := strings.IndexByte(input[1:], '"')
			if end < 0 {
				return "", fmt.Errorf("close the quoted phrase")
			}
			term = input[1 : end+1]
			input = input[end+2:]
		} else {
			end := strings.IndexAny(input, " \t\r\n")
			if end < 0 {
				end = len(input)
			}
			term = input[:end]
			input = input[end:]
		}
		if strings.TrimSpace(term) != "" {
			parts = append(parts, `"`+strings.ReplaceAll(term, `"`, `""`)+`"`)
		}
	}
	if len(parts) == 0 {
		return "", fmt.Errorf("enter a search term")
	}
	return strings.Join(parts, " AND "), nil
}

func (s *Store) Search(ctx context.Context, o SearchOptions) (SearchResult, error) {
	result := SearchResult{Hits: []Hit{}, Page: o.Page, Limit: o.Limit}
	if o.Page < 1 || o.Limit < 1 || o.Limit > 100 {
		return result, fmt.Errorf("invalid pagination")
	}
	expr, err := SearchExpression(o.Query)
	if err != nil {
		return result, err
	}
	where := " WHERE page_search MATCH ? AND d.status='ready'"
	args := []any{expr}
	for _, filter := range []struct{ value, operator string }{{o.After, ">="}, {o.Before, "<="}} {
		if filter.value == "" {
			continue
		}
		if _, err = time.Parse("2006-01-02", filter.value); err != nil {
			return result, fmt.Errorf("invalid date")
		}
		where += " AND substr(d.modified,1,10) " + filter.operator + " ?"
		args = append(args, filter.value)
	}
	from := " FROM page_search JOIN pages p ON p.id=page_search.rowid JOIN documents d ON d.id=p.document_id"
	if err = s.db.QueryRowContext(ctx, "SELECT count(DISTINCT d.id)"+from+where, args...).Scan(&result.Total); err != nil {
		return result, err
	}
	order := "score ASC,document_id ASC"
	if o.Sort == "modified" {
		order = "modified DESC,document_id ASC"
	} else if o.Sort != "" && o.Sort != "relevance" {
		return result, fmt.Errorf("invalid sort")
	}
	// FTS supplies the exact matched spans, including normalized tokens and phrases.
	// Random delimiters keep document text separate from renderer markup.
	marker := rand.Text()
	start, end := "\x1e"+marker+":start\x1f", "\x1e"+marker+":end\x1f"
	query := `WITH matched AS MATERIALIZED (
 SELECT p.id AS page_id,d.id AS document_id,d.path,d.modified,p.number,
 snippet(page_search,0,?,?,' … ',32) AS excerpt,bm25(page_search) AS score` + from + where + `)
 SELECT document_id,path,modified,number,excerpt FROM matched m
 WHERE page_id=(SELECT page_id FROM matched x WHERE x.document_id=m.document_id ORDER BY score,number LIMIT 1)
 ORDER BY ` + order + ` LIMIT ? OFFSET ?`
	args = append([]any{start, end}, args...)
	args = append(args, o.Limit, (o.Page-1)*o.Limit)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return result, err
	}
	defer rows.Close()
	for rows.Next() {
		var h Hit
		if err = rows.Scan(&h.DocumentID, &h.Path, &h.Modified, &h.Page, &h.Snippet); err != nil {
			return result, err
		}
		h.Snippet, h.SnippetParts = splitSnippet(h.Snippet, start, end)
		result.Hits = append(result.Hits, h)
	}
	return result, rows.Err()
}

// splitSnippet returns plain text for API clients and safe text spans for the UI.
func splitSnippet(text, start, end string) (string, []SnippetPart) {
	parts := []SnippetPart{}
	var plain strings.Builder
	add := func(value string, match bool) {
		if value != "" {
			parts = append(parts, SnippetPart{Text: value, Match: match})
			plain.WriteString(value)
		}
	}
	for text != "" {
		begin := strings.Index(text, start)
		if begin < 0 {
			add(text, false)
			break
		}
		remaining := text[begin+len(start):]
		finish := strings.Index(remaining, end)
		if finish < 0 {
			add(text, false)
			break
		}
		add(text[:begin], false)
		add(remaining[:finish], true)
		text = remaining[finish+len(end):]
	}
	return plain.String(), parts
}
