# Source guard validation

Implemented on 2026-10-05.

## Behavior

The database stores the archive root's device/inode identity. Scans check it
before discovery and before pruning; retries and workers check it before reading
and publishing. A missing or replaced root pauses queued work without deleting
checkpoints or marking those jobs as extraction failures. PDF downloads from a
replacement root return HTTP 503; indexed text remains searchable.

An existing index without an identity is enrolled only after a source PDF at its
indexed relative path matches its recorded SHA-256 fingerprint. A new empty index
can enroll immediately. Enrollment uses an insert-if-absent operation so that
concurrent scanners cannot overwrite each other's identity.

If a successful walk finds zero PDFs while documents remain indexed, deletion is
blocked. Intentional deletion of the last PDF requires a one-off CLI
`scan -allow-empty`. Ordinary deletions while other PDFs remain are automatic.
`accept-source` explicitly binds a deliberately moved/remounted source; it does
not itself change the document index. Neither override is exposed to automatic
scans or as a Web UI action.

## Verification

- Go race tests and vet passed; the Docker build also ran the Go tests.
- Restarted database retains the enrolled identity.
- Missing roots, replacement roots with same-named PDFs, and forced retries
  against replacement roots are blocked without changing searchable content.
- Restoration of the original identity is recognized automatically.
- Ordinary deletion works; an empty archive preserves documents until explicitly
  confirmed.
- Legacy index enrollment requires matching PDF content.
- A source replacement during extraction requeues the job and retains page
  checkpoints.
- Separate Docker fixture index: first import queued one PDF; a new container
  recognized it as unchanged. Replacing the source with an empty directory
  returned an error with zero deletions. Docker Desktop assigned another identity
  after restoring the host directory; explicit `accept-source` followed by a scan
  returned one unchanged document, zero queued jobs, and zero deletions.

The real test instance on port 8090 uses `agenticarchive:source-guard`, the existing
read-only source and index volume, a 15-minute scan interval, and the existing
512 MiB memory limit. Its upgrade check is recorded below.

## Limits

Device/inode identities can change following NAS remounts or Docker Desktop host
folder remapping. The guard then fails closed and requires explicit acceptance.
It does not fully detect partial, readable failures within nested mounts that
retain the root identity. Source PDFs remain read-only; no marker is written.

## Existing archive upgrade result

The first scan enrolled the unchanged real archive and reported 494 unchanged
PDFs, zero queued jobs, zero deletions, and zero failures. All 494 documents remain
ready. The source check has no error and the worker queue is empty. The known
`Nießbrauchsrecht` query still returns 12 documents. No full re-import was needed.
