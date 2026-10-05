# Runtime settings validation

Implemented on 2026-10-05.

The Settings view displays the configured archive and index directories and source
availability. The source is deliberately read-only in the UI: Docker bind mounts
remain deployment configuration. Automatic reconciliation can be disabled or set
to 1–10080 whole minutes. Settings are stored in the SQLite index volume and take
precedence over the initial `serve -interval` value after restart/recreation.

Interval changes reset the scheduler's wait without restarting the server. A
running scan finishes before a new wait begins; the scheduler never accumulates
scan ticks. Disabling periodic scanning leaves startup reconciliation, manual
scanning, and the PDF worker available.

## Automated verification

- Store test: initial default, persistence through reopening, disabled setting,
  invalid values preserving the previous setting.
- HTTP tests: saved value updates the scheduler and database, read-only paths,
  JSON/body limits, missing/unknown fields, fractional/invalid intervals,
  content type, method handling, and cross-site mutation rejection.
- Routing tests include the UI fallback together with GET and PUT settings routes.
- Scheduler tests: startup scan while disabled, runtime enabling/disabling,
  cancellation while disabled, and no queued ticks during a long scan.
- Go race tests and vet passed. Docker builds run the Go tests as well.

The API accepts `GET /api/settings` and `PUT /api/settings` with JSON
`{"scan_interval_seconds":900}`. Zero disables periodic scanning. Other writable
values must be multiples of 60 seconds between 60 and 604800. Existing startup
configuration supports nonnegative whole seconds; sub-minute defaults may need
changing to at least one minute before saving through the UI.

## Browser and Docker verification

The synthetic instance saved a five-minute interval through the UI. After a
Docker restart with its original `-interval 0` startup flag, the UI still showed
five minutes. Disabling the interval through the UI succeeded. A subsequent
one-minute setting automatically imported a newly added synthetic PDF without a
manual scan. Mobile dark mode at 390 × 844 had no horizontal overflow. The real
instance's settings view was checked at desktop size 1100 × 900.

The real instance on port 8090 now uses `agenticarchive:settings` with its original
read-only source, existing index volume, 512 MiB limit, and 15-minute interval.
Its first scan reported 494 unchanged PDFs, zero queued/deleted/failed documents.
All 494 documents remain ready, and the known `Nießbrauchsrecht` query still
returns 12 matches. Settings report `/archive`, `/data`, 900 seconds, and an
available source.
