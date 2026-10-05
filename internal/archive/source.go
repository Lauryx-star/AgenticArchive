package archive

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"syscall"
)

// ErrSourceUnavailable pauses processing without invalidating documents or checkpoints.
var ErrSourceUnavailable = errors.New("archive source unavailable or changed")

func sourceIdentity(root string) (string, error) {
	info, err := os.Stat(root)
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrSourceUnavailable, err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("%w: source is not a directory", ErrSourceUnavailable)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return "", fmt.Errorf("%w: filesystem identity not supported", ErrSourceUnavailable)
	}
	return fmt.Sprintf("%d:%d", stat.Dev, stat.Ino), nil
}

// CheckSource verifies an enrolled source without enrolling an unverified directory.
func (s *Store) CheckSource(ctx context.Context, root string) error {
	identity, err := sourceIdentity(root)
	if err != nil {
		return err
	}
	var expected string
	err = s.db.QueryRowContext(ctx, "SELECT identity FROM archive_source WHERE id=1").Scan(&expected)
	if err == sql.ErrNoRows {
		return nil
	}
	if err != nil {
		return err
	}
	if identity != expected {
		return fmt.Errorf("%w: directory identity differs; restore the original mount or explicitly run accept-source", ErrSourceUnavailable)
	}
	return nil
}

// AcceptSource deliberately binds this index to the currently mounted directory.
// It does not remove documents or enqueue work.
func (s *Store) AcceptSource(ctx context.Context, root string) error {
	identity, err := sourceIdentity(root)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, "INSERT INTO archive_source(id,identity) VALUES(1,?) ON CONFLICT(id) DO UPDATE SET identity=excluded.identity", identity)
	return err
}

func (s *Store) enrollSource(ctx context.Context, root string) error {
	identity, err := sourceIdentity(root)
	if err != nil {
		return err
	}
	var expected string
	err = s.db.QueryRowContext(ctx, "SELECT identity FROM archive_source WHERE id=1").Scan(&expected)
	if err == nil {
		return s.CheckSource(ctx, root)
	}
	if err != sql.ErrNoRows {
		return err
	}
	docs, err := s.Documents(ctx)
	if err != nil {
		return err
	}
	// Existing indexes must prove continuity before the first identity is recorded.
	matched := len(docs) == 0
	for _, d := range docs {
		if err := ctx.Err(); err != nil {
			return err
		}
		path, err := sourcePath(root, d.Path)
		if err != nil {
			continue
		}
		hash, err := fingerprint(path)
		if err == nil && hash == d.Fingerprint {
			matched = true
			break
		}
	}
	if !matched {
		return fmt.Errorf("%w: existing index has no matching PDF in this directory; restore the source or explicitly run accept-source", ErrSourceUnavailable)
	}
	after, err := sourceIdentity(root)
	if err != nil {
		return err
	}
	if identity != after {
		return fmt.Errorf("%w: directory changed during enrollment", ErrSourceUnavailable)
	}
	// Concurrent scanners must not overwrite an already enrolled identity.
	_, err = s.db.ExecContext(ctx, "INSERT INTO archive_source(id,identity) VALUES(1,?) ON CONFLICT(id) DO NOTHING", identity)
	if err != nil {
		return err
	}
	return s.CheckSource(ctx, root)
}
