package archive

import (
	"context"
	"fmt"
	"time"
)

// ScanInterval seeds a new index with the startup default; saved settings win on restart.
func (s *Store) ScanInterval(ctx context.Context, fallback time.Duration) (time.Duration, error) {
	if fallback < 0 || fallback%time.Second != 0 {
		return 0, fmt.Errorf("scan interval must be a nonnegative whole number of seconds")
	}
	_, err := s.db.ExecContext(ctx, "INSERT INTO settings(key,value) VALUES('scan_interval_seconds',?) ON CONFLICT(key) DO NOTHING", int64(fallback/time.Second))
	if err != nil {
		return 0, err
	}
	var seconds int64
	if err = s.db.QueryRowContext(ctx, "SELECT value FROM settings WHERE key='scan_interval_seconds'").Scan(&seconds); err != nil {
		return 0, err
	}
	if seconds < 0 || seconds > int64((1<<63-1)/time.Second) {
		return 0, fmt.Errorf("invalid stored scan interval")
	}
	return time.Duration(seconds) * time.Second, nil
}

func (s *Store) SetScanInterval(ctx context.Context, seconds int64) error {
	if seconds != 0 && (seconds < 60 || seconds > 604800 || seconds%60 != 0) {
		return fmt.Errorf("scan interval must be 0 or a whole number of minutes from 1 to 10080")
	}
	_, err := s.db.ExecContext(ctx, "INSERT INTO settings(key,value) VALUES('scan_interval_seconds',?) ON CONFLICT(key) DO UPDATE SET value=excluded.value", seconds)
	return err
}
