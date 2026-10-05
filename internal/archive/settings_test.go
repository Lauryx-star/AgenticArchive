package archive

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func TestScanIntervalPersistsAndStartupDefaultDoesNotResetIt(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "archive.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	interval, err := s.ScanInterval(ctx, 15*time.Minute)
	if err != nil || interval != 15*time.Minute {
		t.Fatalf("seed: %v %v", interval, err)
	}
	if err := s.SetScanInterval(ctx, 300); err != nil {
		t.Fatal(err)
	}
	s.Close()
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	interval, err = s.ScanInterval(ctx, time.Hour)
	if err != nil || interval != 5*time.Minute {
		t.Fatalf("restart: %v %v", interval, err)
	}
	if err := s.SetScanInterval(ctx, 0); err != nil {
		t.Fatal(err)
	}
	interval, err = s.ScanInterval(ctx, 15*time.Minute)
	if err != nil || interval != 0 {
		t.Fatal("disabled interval reset")
	}
	for _, seconds := range []int64{-1, 1, 59, 61, 604801, 9223372036854775807} {
		if err := s.SetScanInterval(ctx, seconds); err == nil {
			t.Fatalf("invalid interval accepted: %d", seconds)
		}
	}
	interval, err = s.ScanInterval(ctx, time.Hour)
	if err != nil || interval != 0 {
		t.Fatal("invalid update changed settings")
	}
}
