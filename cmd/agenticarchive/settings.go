package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/http"
	"sync"
	"time"

	"github.com/Lauryx-star/AgenticArchive/internal/archive"
)

type scanSchedule struct {
	mu       sync.Mutex
	interval time.Duration
	changed  chan struct{}
}

func newScanSchedule(interval time.Duration) *scanSchedule {
	return &scanSchedule{interval: interval, changed: make(chan struct{}, 1)}
}
func (s *scanSchedule) get() time.Duration { s.mu.Lock(); defer s.mu.Unlock(); return s.interval }
func (s *scanSchedule) set(interval time.Duration) {
	s.mu.Lock()
	s.interval = interval
	s.mu.Unlock()
	select {
	case s.changed <- struct{}{}:
	default:
	}
}

// Only one scan runs at a time. Changes reset the wait; elapsed ticks never queue scans.
func (s *scanSchedule) run(ctx context.Context, scan func()) {
	if ctx.Err() != nil {
		return
	}
	scan() // Startup reconciliation remains independent of periodic scanning.
	for {
		interval := s.get()
		var timer *time.Timer
		var tick <-chan time.Time
		if interval > 0 {
			timer = time.NewTimer(interval)
			tick = timer.C
		}
		select {
		case <-ctx.Done():
			if timer != nil {
				timer.Stop()
			}
			return
		case <-s.changed:
			if timer != nil {
				timer.Stop()
			}
		case <-tick:
			select {
			case <-s.changed:
				continue
			default:
			}
			if ctx.Err() != nil {
				return
			}
			scan()
		}
	}
}

type appSettings struct {
	ScanIntervalSeconds int64  `json:"scan_interval_seconds"`
	ArchiveRoot         string `json:"archive_root"`
	DataDirectory       string `json:"data_directory"`
	SourceAvailable     bool   `json:"source_available"`
}

func settingsHandler(store *archive.Store, schedule *scanSchedule, root, data string) http.Handler {
	var writes sync.Mutex
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
		case http.MethodPut:
			if r.Header.Get("Sec-Fetch-Site") == "cross-site" {
				apiError(w, 403, fmt.Errorf("cross-site request rejected"))
				return
			}
			media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
			if err != nil || media != "application/json" {
				apiError(w, 415, fmt.Errorf("application/json required"))
				return
			}
			var input struct {
				ScanIntervalSeconds *int64 `json:"scan_interval_seconds"`
			}
			decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
			decoder.DisallowUnknownFields()
			if err = decoder.Decode(&input); err != nil {
				apiError(w, 400, fmt.Errorf("invalid settings JSON"))
				return
			}
			if input.ScanIntervalSeconds == nil {
				apiError(w, 400, fmt.Errorf("scan_interval_seconds is required"))
				return
			}
			if err = decoder.Decode(new(any)); err != io.EOF {
				apiError(w, 400, fmt.Errorf("only one settings object is allowed"))
				return
			}
			writes.Lock()
			err = store.SetScanInterval(r.Context(), *input.ScanIntervalSeconds)
			if err == nil {
				schedule.set(time.Duration(*input.ScanIntervalSeconds) * time.Second)
			}
			writes.Unlock()
			if err != nil {
				apiError(w, 400, err)
				return
			}
		default:
			w.Header().Set("Allow", "GET, PUT")
			apiError(w, 405, fmt.Errorf("method not allowed"))
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		writeJSON(w, appSettings{ScanIntervalSeconds: int64(schedule.get() / time.Second), ArchiveRoot: root, DataDirectory: data, SourceAvailable: store.CheckSource(r.Context(), root) == nil})
	})
}
