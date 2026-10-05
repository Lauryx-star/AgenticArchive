package archive

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

var ErrObsolete = errors.New("document version is no longer current")

type Job struct {
	ID             int64  `json:"id"`
	DocumentID     int64  `json:"document_id"`
	Path           string `json:"path"`
	Fingerprint    string `json:"-"`
	State          string `json:"state"`
	ForceOCR       bool   `json:"force_ocr"`
	TotalPages     int    `json:"total_pages"`
	CompletedPages int    `json:"completed_pages"`
}
type QueueStatus struct {
	Pending   int  `json:"pending"`
	Running   int  `json:"running"`
	Failed    int  `json:"failed"`
	Completed int  `json:"completed"`
	Active    *Job `json:"active,omitempty"`
}

// Enqueue uses a database uniqueness constraint, not an in-memory seen list.
// Repeated scans never reset pending/running/failed work for the same version.
func (s *Store) Enqueue(ctx context.Context, d Document, retry, force bool) (bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	old, err := readDocument(tx.QueryRowContext(ctx, "SELECT "+documentColumns+" FROM documents WHERE path=?", d.Path))
	if err != nil && err != sql.ErrNoRows {
		return false, err
	}
	if err == nil && old.Fingerprint == d.Fingerprint && !retry {
		_, err = tx.ExecContext(ctx, "UPDATE documents SET size=?,modified=? WHERE id=?", d.Size, d.Modified, old.ID)
		if err != nil {
			return false, err
		}
		if old.Status == "ready" || old.Status == "queued" || old.Status == "processing" {
			return false, tx.Commit()
		}
		// Legacy failed documents have no job; create one to make them explicitly retryable.
		var count int
		if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM jobs WHERE document_id=? AND fingerprint=?", old.ID, d.Fingerprint).Scan(&count); err != nil {
			return false, err
		}
		if count > 0 {
			return false, tx.Commit()
		}
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO documents(path,size,modified,fingerprint,status,error,pages)
 VALUES(?,?,?,?,'queued','',0) ON CONFLICT(path) DO UPDATE SET
 size=excluded.size,modified=excluded.modified,fingerprint=excluded.fingerprint,status='queued',error=''`, d.Path, d.Size, d.Modified, d.Fingerprint)
	if err != nil {
		return false, err
	}
	if err = tx.QueryRowContext(ctx, "SELECT id FROM documents WHERE path=?", d.Path).Scan(&d.ID); err != nil {
		return false, err
	}
	if _, err = tx.ExecContext(ctx, "UPDATE jobs SET state='superseded' WHERE document_id=? AND fingerprint<>? AND state<>'done'", d.ID, d.Fingerprint); err != nil {
		return false, err
	}
	if _, err = tx.ExecContext(ctx, "DELETE FROM job_pages WHERE job_id IN (SELECT id FROM jobs WHERE document_id=? AND state='superseded')", d.ID); err != nil {
		return false, err
	}
	var jobID int64
	var state string
	var oldForce bool
	err = tx.QueryRowContext(ctx, "SELECT id,state,force_ocr FROM jobs WHERE document_id=? AND fingerprint=?", d.ID, d.Fingerprint).Scan(&jobID, &state, &oldForce)
	if err == sql.ErrNoRows {
		_, err = tx.ExecContext(ctx, "INSERT INTO jobs(document_id,fingerprint,state,force_ocr) VALUES(?,?,'pending',?)", d.ID, d.Fingerprint, force)
	} else if err == nil {
		// A reverted source version or explicit retry reuses the same durable job.
		if state == "done" || state == "superseded" || oldForce != force || force {
			if _, err = tx.ExecContext(ctx, "DELETE FROM job_pages WHERE job_id=?", jobID); err != nil {
				return false, err
			}
			if _, err = tx.ExecContext(ctx, "UPDATE jobs SET total_pages=0 WHERE id=?", jobID); err != nil {
				return false, err
			}
		}
		_, err = tx.ExecContext(ctx, "UPDATE jobs SET state='pending',force_ocr=?,error='' WHERE id=?", force, jobID)
	}
	if err != nil {
		return false, err
	}
	return true, tx.Commit()
}

func (s *Store) Queue(ctx context.Context) (QueueStatus, error) {
	var q QueueStatus
	err := s.db.QueryRowContext(ctx, `SELECT
 coalesce(sum(state='pending'),0),coalesce(sum(state='running'),0),
 coalesce(sum(state='failed'),0),coalesce(sum(state='done'),0) FROM jobs`).Scan(&q.Pending, &q.Running, &q.Failed, &q.Completed)
	if err != nil {
		return q, err
	}
	j := Job{}
	err = s.db.QueryRowContext(ctx, `SELECT j.id,j.document_id,d.path,j.fingerprint,j.state,j.force_ocr,j.total_pages,
 (SELECT count(*) FROM job_pages p WHERE p.job_id=j.id)
 FROM jobs j JOIN documents d ON d.id=j.document_id WHERE j.state='running' LIMIT 1`).Scan(&j.ID, &j.DocumentID, &j.Path, &j.Fingerprint, &j.State, &j.ForceOCR, &j.TotalPages, &j.CompletedPages)
	if err == nil {
		q.Active = &j
	} else if err != sql.ErrNoRows {
		return q, err
	}
	return q, nil
}

// Recovery may only run while holding the process-wide worker file lock.
func (s *Store) recoverJobs(ctx context.Context) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, "UPDATE jobs SET state='pending' WHERE state='running'"); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE documents SET status='queued' WHERE status='processing'
 AND EXISTS(SELECT 1 FROM jobs j WHERE j.document_id=documents.id AND j.fingerprint=documents.fingerprint AND j.state='pending')`); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) claim(ctx context.Context) (Job, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Job{}, err
	}
	defer tx.Rollback()
	var j Job
	err = tx.QueryRowContext(ctx, `SELECT j.id,j.document_id,d.path,j.fingerprint,j.force_ocr,j.total_pages
 FROM jobs j JOIN documents d ON d.id=j.document_id WHERE j.state='pending' AND j.fingerprint=d.fingerprint ORDER BY j.id LIMIT 1`).Scan(&j.ID, &j.DocumentID, &j.Path, &j.Fingerprint, &j.ForceOCR, &j.TotalPages)
	if err != nil {
		return j, err
	}
	if _, err = tx.ExecContext(ctx, "UPDATE jobs SET state='running' WHERE id=?", j.ID); err != nil {
		return j, err
	}
	if _, err = tx.ExecContext(ctx, "UPDATE documents SET status='processing' WHERE id=? AND fingerprint=?", j.DocumentID, j.Fingerprint); err != nil {
		return j, err
	}
	j.State = "running"
	return j, tx.Commit()
}

func currentJob(ctx context.Context, tx *sql.Tx, j Job) error {
	var n int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM jobs j JOIN documents d ON d.id=j.document_id
 WHERE j.id=? AND j.state='running' AND j.fingerprint=? AND d.fingerprint=j.fingerprint`, j.ID, j.Fingerprint).Scan(&n); err != nil {
		return err
	}
	if n != 1 {
		return ErrObsolete
	}
	return nil
}

func (s *Store) savedPages(ctx context.Context, j Job) ([]Page, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT number,text,ocr FROM job_pages WHERE job_id=? ORDER BY number", j.ID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	pages := []Page{}
	for rows.Next() {
		var p Page
		if err = rows.Scan(&p.Number, &p.Text, &p.OCR); err != nil {
			return nil, err
		}
		pages = append(pages, p)
	}
	return pages, rows.Err()
}
func (s *Store) savePage(ctx context.Context, j Job, p Page, total int) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = currentJob(ctx, tx, j); err != nil {
		return err
	}
	var size int
	if err = tx.QueryRowContext(ctx, "SELECT coalesce(sum(length(CAST(text AS BLOB))),0) FROM job_pages WHERE job_id=? AND number<>?", j.ID, p.Number).Scan(&size); err != nil {
		return err
	}
	if size+len(p.Text) > 16<<20 {
		return fmt.Errorf("document text exceeds 16 MiB limit")
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO job_pages(job_id,number,text,ocr) VALUES(?,?,?,?)
 ON CONFLICT(job_id,number) DO UPDATE SET text=excluded.text,ocr=excluded.ocr`, j.ID, p.Number, p.Text, p.OCR); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, "UPDATE jobs SET total_pages=? WHERE id=?", total, j.ID); err != nil {
		return err
	}
	return tx.Commit()
}
func (s *Store) finish(ctx context.Context, j Job, pages []Page) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = currentJob(ctx, tx, j); err != nil {
		return err
	}
	d, err := readDocument(tx.QueryRowContext(ctx, "SELECT "+documentColumns+" FROM documents WHERE id=?", j.DocumentID))
	if err != nil {
		return err
	}
	d.Status = "ready"
	d.Error = ""
	if err = replaceTx(ctx, tx, d, pages); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, "UPDATE jobs SET state='done',error='' WHERE id=?", j.ID); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, "DELETE FROM job_pages WHERE job_id=?", j.ID); err != nil {
		return err
	}
	return tx.Commit()
}
func (s *Store) failJob(ctx context.Context, j Job, cause error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = currentJob(ctx, tx, j); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, "UPDATE jobs SET state='failed',error=? WHERE id=?", cause.Error(), j.ID); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, "UPDATE documents SET status='error',error=? WHERE id=? AND fingerprint=?", cause.Error(), j.DocumentID, j.Fingerprint); err != nil {
		return err
	}
	return tx.Commit()
}
func (s *Store) releaseJob(j Job) error {
	ctx := context.Background()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = currentJob(ctx, tx, j); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, "UPDATE jobs SET state='pending' WHERE id=?", j.ID); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, "UPDATE documents SET status='queued' WHERE id=?", j.DocumentID); err != nil {
		return err
	}
	return tx.Commit()
}
