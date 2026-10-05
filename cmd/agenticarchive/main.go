package main

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/Lauryx-star/AgenticArchive/internal/archive"
)

//go:embed ui/*
var assets embed.FS

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

func env(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}

func run() error {
	command := "serve"
	if len(os.Args) > 1 {
		command = os.Args[1]
	}
	flags := flag.NewFlagSet(command, flag.ContinueOnError)
	root := flags.String("root", env("ARCHIVE_ROOT", "./archive"), "read-only PDF source directory")
	data := flags.String("data", env("DATA_DIR", "./data"), "persistent index directory")
	listen := flags.String("listen", env("LISTEN_ADDR", "127.0.0.1:8080"), "HTTP listen address")
	interval := flags.Duration("interval", 15*time.Minute, "initial scan interval for new indexes; saved settings take precedence (0 disables periodic scans)")
	allowEmpty := flags.Bool("allow-empty", false, "explicitly allow removing all indexed documents from a verified empty source (scan only)")
	full := flags.Bool("full", false, "verify every file's fingerprint")
	query := flags.String("query", "", "search words or quoted phrases")
	page := flags.Int("page", 1, "search result page")
	if len(os.Args) > 1 {
		if err := flags.Parse(os.Args[2:]); err != nil {
			return err
		}
	}
	if command != "serve" && command != "scan" && command != "search" && command != "accept-source" {
		return fmt.Errorf("unknown command %q; use serve, scan, search, or accept-source", command)
	}
	if *allowEmpty && command != "scan" {
		return fmt.Errorf("allow-empty is only available for an explicit scan")
	}
	if *interval < 0 || *interval%time.Second != 0 {
		return fmt.Errorf("interval must be a nonnegative whole number of seconds")
	}
	absRoot, err := filepath.Abs(*root)
	if err != nil {
		return err
	}
	if err = os.MkdirAll(*data, 0750); err != nil {
		return err
	}
	store, err := archive.Open(filepath.Join(*data, "archive.db"))
	if err != nil {
		return err
	}
	defer store.Close()
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	scanner := &archive.Scanner{Store: store, Root: absRoot, Extractor: archive.PDFExtractor{Languages: "deu+eng", MaxDimension: 2400, Timeout: 2 * time.Minute}}
	switch command {
	case "accept-source":
		return store.AcceptSource(ctx, absRoot)
	case "scan":
		scanner.AllowEmpty = *allowEmpty
		err = scanner.Scan(ctx, *full)
		if err == nil {
			err = (&archive.Worker{Store: store, Root: absRoot, Extractor: scanner.Extractor}).Drain(ctx)
			if errors.Is(err, archive.ErrWorkerBusy) {
				err = nil
			}
		}
		json.NewEncoder(os.Stdout).Encode(scanner.Status())
		return err
	case "search":
		result, err := store.Search(ctx, archive.SearchOptions{Query: *query, Page: *page, Limit: 20})
		if err != nil {
			return err
		}
		return json.NewEncoder(os.Stdout).Encode(result)
	}
	scan := func(full bool) {
		if err := scanner.Scan(ctx, full); err != nil {
			log.Printf("scan: %v", err)
		}
	}
	var work sync.WaitGroup
	defer func() { cancel(); work.Wait() }()
	work.Add(1)
	go func() {
		defer work.Done()
		worker := archive.Worker{Store: store, Root: absRoot, Extractor: scanner.Extractor}
		if err := worker.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
			log.Printf("worker: %v", err)
		}
	}()
	savedInterval, err := store.ScanInterval(ctx, *interval)
	if err != nil {
		return err
	}
	schedule := newScanSchedule(savedInterval)
	work.Add(1)
	go func() { defer work.Done(); schedule.run(ctx, func() { scan(false) }) }()
	absData, err := filepath.Abs(*data)
	if err != nil {
		return err
	}
	mux := http.NewServeMux()
	settings := settingsHandler(store, schedule, absRoot, absData)
	mux.Handle("GET /api/settings", settings)
	mux.Handle("PUT /api/settings", settings)
	ui, _ := fs.Sub(assets, "ui")
	mux.Handle("GET /", http.FileServer(http.FS(ui)))
	mux.HandleFunc("GET /api/status", func(w http.ResponseWriter, r *http.Request) {
		docs, err := store.Documents(r.Context())
		if err != nil {
			apiError(w, 500, err)
			return
		}
		ready, failed := 0, 0
		failures := []archive.Document{}
		for _, d := range docs {
			if d.Status == "ready" {
				ready++
			} else if d.Status == "error" {
				failed++
				if len(failures) < 20 {
					failures = append(failures, d)
				}
			}
		}
		queue, err := store.Queue(r.Context())
		if err != nil {
			apiError(w, 500, err)
			return
		}
		sourceError := ""
		if err := store.CheckSource(r.Context(), absRoot); err != nil {
			sourceError = err.Error()
		}
		writeJSON(w, map[string]any{"source_error": sourceError, "scan": scanner.Status(), "queue": queue, "documents": len(docs), "ready": ready, "failed": failed, "failures": failures, "ocr_dimension": 2400, "ocr_workers": 1})
	})
	mux.HandleFunc("POST /api/documents/{id}/retry", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Sec-Fetch-Site") == "cross-site" {
			apiError(w, 403, fmt.Errorf("cross-site request rejected"))
			return
		}
		id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil {
			apiError(w, 400, err)
			return
		}
		if err = scanner.Retry(r.Context(), id, r.URL.Query().Get("ocr") == "true"); err != nil {
			apiError(w, 400, err)
			return
		}
		w.WriteHeader(http.StatusAccepted)
	})
	mux.HandleFunc("POST /api/scan", func(w http.ResponseWriter, r *http.Request) {
		// Browser clients may only trigger scans from the same origin.
		if r.Header.Get("Sec-Fetch-Site") == "cross-site" {
			apiError(w, 403, fmt.Errorf("cross-site request rejected"))
			return
		}
		if scanner.Status().Running {
			apiError(w, 409, fmt.Errorf("a scan is already running"))
			return
		}
		full := r.URL.Query().Get("full") == "true"
		work.Add(1)
		go func() { defer work.Done(); scan(full) }()
		w.WriteHeader(http.StatusAccepted)
	})
	mux.HandleFunc("GET /api/search", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		page, err := parsePositive(q.Get("page"), 1)
		if err != nil {
			apiError(w, 400, err)
			return
		}
		limit, err := parsePositive(q.Get("limit"), 20)
		if err != nil {
			apiError(w, 400, err)
			return
		}
		if len(q.Get("q")) > 1000 || page > 100000 {
			apiError(w, 400, fmt.Errorf("query or pagination limit exceeded"))
			return
		}
		result, err := store.Search(r.Context(), archive.SearchOptions{Query: q.Get("q"), Page: page, Limit: limit, After: q.Get("after"), Before: q.Get("before"), Sort: q.Get("sort")})
		if err != nil {
			apiError(w, 400, err)
			return
		}
		writeJSON(w, result)
	})
	mux.HandleFunc("GET /api/documents", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		page, err := parsePositive(q.Get("page"), 1)
		if err != nil {
			apiError(w, 400, err)
			return
		}
		limit, err := parsePositive(q.Get("limit"), 20)
		if err != nil {
			apiError(w, 400, err)
			return
		}
		result, err := store.ListDocuments(r.Context(), archive.DocumentListOptions{Path: q.Get("path"), Status: q.Get("status"), Sort: q.Get("sort"), Page: page, Limit: limit})
		if err != nil {
			apiError(w, 400, err)
			return
		}
		writeJSON(w, result)
	})
	mux.HandleFunc("GET /api/documents/{id}", func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil {
			apiError(w, 400, err)
			return
		}
		d, err := store.Document(r.Context(), id)
		if err != nil {
			apiError(w, 404, fmt.Errorf("document not found"))
			return
		}
		writeJSON(w, d)
	})
	mux.HandleFunc("GET /api/documents/{id}/pages/{number}", func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil {
			apiError(w, 400, err)
			return
		}
		n, err := strconv.Atoi(r.PathValue("number"))
		if err != nil {
			apiError(w, 400, err)
			return
		}
		p, err := store.Page(r.Context(), id, n)
		if err != nil {
			apiError(w, 404, fmt.Errorf("page not found"))
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		writeJSON(w, p)
	})
	mux.HandleFunc("GET /api/documents/{id}/pdf", func(w http.ResponseWriter, r *http.Request) {
		if err := store.CheckSource(r.Context(), absRoot); err != nil {
			apiError(w, http.StatusServiceUnavailable, err)
			return
		}
		id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil {
			apiError(w, 400, err)
			return
		}
		d, err := store.Document(r.Context(), id)
		if err != nil {
			apiError(w, 404, fmt.Errorf("document not found"))
			return
		}
		path, err := safePDFPath(absRoot, d.Path)
		if err != nil {
			apiError(w, 404, err)
			return
		}
		w.Header().Set("Content-Type", "application/pdf")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		http.ServeFile(w, r, path)
	})
	server := &http.Server{Addr: *listen, Handler: mux, ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 60 * time.Second}
	shutdownDone := make(chan struct{})
	go func() {
		defer close(shutdownDone)
		<-ctx.Done()
		shutdown, done := context.WithTimeout(context.Background(), 5*time.Second)
		defer done()
		server.Shutdown(shutdown)
	}()
	log.Printf("AgenticArchive listening on %s", *listen)
	err = server.ListenAndServe()
	if err == http.ErrServerClosed {
		<-shutdownDone
		return nil
	}
	return err
}

func parsePositive(raw string, fallback int) (int, error) {
	if raw == "" {
		return fallback, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 1 {
		return 0, fmt.Errorf("expected a positive integer")
	}
	return n, nil
}
func writeJSON(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	json.NewEncoder(w).Encode(value)
}
func apiError(w http.ResponseWriter, status int, err error) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
}

func safePDFPath(root, relative string) (string, error) {
	resolvedRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", err
	}
	path, err := filepath.EvalSymlinks(filepath.Join(root, filepath.FromSlash(relative)))
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(resolvedRoot, path)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return "", fmt.Errorf("document is outside the archive")
	}
	if !strings.EqualFold(filepath.Ext(path), ".pdf") {
		return "", fmt.Errorf("not a PDF")
	}
	return path, nil
}
