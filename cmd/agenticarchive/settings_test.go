package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Lauryx-star/AgenticArchive/internal/archive"
)

func TestSettingsHTTPValidationPersistenceAndReadOnlyPaths(t *testing.T) {
	store, err := archive.Open(filepath.Join(t.TempDir(), "archive.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	root := t.TempDir()
	schedule := newScanSchedule(15 * time.Minute)
	handler := settingsHandler(store, schedule, root, "/index")
	mux := http.NewServeMux()
	mux.Handle("GET /api/settings", handler)
	mux.Handle("PUT /api/settings", handler)
	mux.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(404) })
	request := func(method, body, site string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "/api/settings", strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Sec-Fetch-Site", site)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		return w
	}
	w := request("PUT", `{"scan_interval_seconds":300}`, "same-origin")
	if w.Code != 200 || schedule.get() != 5*time.Minute {
		t.Fatalf("save failed: %d %s", w.Code, w.Body)
	}
	saved, err := store.ScanInterval(context.Background(), time.Hour)
	if err != nil || saved != 5*time.Minute {
		t.Fatal("setting not persisted")
	}
	for _, body := range []string{`{}`, `{"scan_interval_seconds":null}`, `{"scan_interval_seconds":-1}`, `{"scan_interval_seconds":61}`, `{"scan_interval_seconds":60.5}`, `{"scan_interval_seconds":300,"archive_root":"/other"}`, `{"scan_interval_seconds":300} {}`, strings.Repeat("x", 5000)} {
		if w := request("PUT", body, "same-origin"); w.Code != 400 {
			t.Fatalf("accepted invalid body: %d", w.Code)
		}
	}
	if w := request("PUT", `{"scan_interval_seconds":0}`, "cross-site"); w.Code != 403 {
		t.Fatal("cross-site mutation accepted")
	}
	if schedule.get() != 5*time.Minute {
		t.Fatal("failed update changed schedule")
	}
	w = request("GET", "", "")
	var settings appSettings
	if err := json.Unmarshal(w.Body.Bytes(), &settings); err != nil {
		t.Fatal(err)
	}
	if settings.ArchiveRoot != root || settings.DataDirectory != "/index" || !settings.SourceAvailable {
		t.Fatalf("wrong settings: %+v", settings)
	}
	if w := request("PUT", `{"scan_interval_seconds":0}`, "same-origin"); w.Code != 200 || schedule.get() != 0 {
		t.Fatal("disable failed")
	}
	if w := request(http.MethodDelete, "", ""); w.Code != 405 {
		t.Fatal("unsupported method accepted")
	}
	r := httptest.NewRequest("PUT", "/api/settings", strings.NewReader(`{"scan_interval_seconds":300}`))
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	if w.Code != 415 {
		t.Fatal("content type not checked")
	}
}

func TestScanScheduleCanEnableDisableAndCancelWhileDisabled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	schedule := newScanSchedule(0)
	calls := make(chan struct{}, 10)
	done := make(chan struct{})
	go func() { defer close(done); schedule.run(ctx, func() { schedule.set(0); calls <- struct{}{} }) }()
	await := func() {
		t.Helper()
		select {
		case <-calls:
		case <-time.After(2 * time.Second):
			t.Fatal("scheduled scan did not run")
		}
	}
	await() // Startup scan runs even when periodic scanning is disabled.
	schedule.set(10 * time.Millisecond)
	await() // Runtime enable works without restarting.
	select {
	case <-calls:
		t.Fatal("disabled schedule ran again")
	case <-time.After(50 * time.Millisecond):
	}
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("disabled schedule did not stop")
	}
}

func TestScanScheduleDoesNotAccumulateTicksDuringLongScan(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	schedule := newScanSchedule(10 * time.Millisecond)
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	done := make(chan struct{})
	calls := 0
	go func() {
		defer close(done)
		schedule.run(ctx, func() {
			calls++
			started <- struct{}{}
			if calls == 1 {
				<-release
			}
		})
	}()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("startup did not run")
	}
	// Changes coalesce while the single scan is busy; disable before allowing it to finish.
	schedule.set(20 * time.Millisecond)
	schedule.set(0)
	close(release)
	select {
	case <-started:
		t.Fatal("stale interval queued another scan")
	case <-time.After(50 * time.Millisecond):
	}
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("schedule did not stop")
	}
}
