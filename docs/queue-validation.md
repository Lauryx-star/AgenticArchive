# Durable import queue — implementation and validation

## Behavior

- Directory scanning discovers4 versions and enqueues them independently of OCR.
- SQLite enforces `UNIQUE(document_id, fingerprint)` across concurrent discoveries.
- Repeated intervals neither duplicate jobs nor reset completed-page checkpoints.
- Completed pages are committed independently to `job_pages`; final text and FTS
  replacement are published together in a transaction.
- A per-index OS file lock permits one worker, including across processes. Process
  death releases it, and the next worker requeues interrupted jobs with their
  checkpoints intact.
- Changed versions supersede older work; deleted documents cascade-delete their
  jobs. Both checkpoints and publication verify the current database version.
- Each job reads a fingerprint-verified immutable disk copy and checks the original
  again before publication. Per-index scratch directories are cleaned on recovery.
- Normal failed-job retries retain same-version checkpoints; forced OCR starts
  fresh checkpoints while reusing the existing job. Failed jobs are not added
  again by every scan interval.
- Pending replacements do not expose stale search hits or old page text.
- Identical content at separate source paths retains separate document identities.

## Automated checks

Local Go tests, race-detector tests and `go vet` passed. Docker build tests passed.
The suite covers:

1. One hundred new PDFs, a blocked worker and ten overlapping further scans:
   exactly one hundred jobs remain, with 99 pending and one running.
2. Simultaneous discoveries through separate SQLite connections: one job.
3. Restart recovery: saved page 1 is retained; only pages 2 and 3 are extracted.
4. Cancellation: work is requeued without losing completed pages.
5. Source mutation during processing, with and without a concurrent scan:
   obsolete text cannot be published; the newest version is queued.
6. Deletion during processing: the document cannot be resurrected.
7. Explicit retry, persistent force-OCR settings and absence of automatic retry loops.
8. Exclusive worker ownership and separate paths with identical content.
9. Reverted source versions reuse their original job instead of duplicating it.
10. Old page text is unavailable while a replacement is queued.

## Real process-abort test

A synthetic 20-page PDF was reprocessed with forced German/English OCR in a
512 MiB ARM64 container. Automatic directory scans ran every second. Five
additional manual scans were triggered during processing.

The container was killed with SIGKILL after three completed pages were observed.
After restart, the same job ID reported four retained completed pages (another
page was checkpointed between observation and termination). The remaining pages
were processed and all twenty pages were published. The final page was verified
as OCR text.

The four source PDFs still corresponded to exactly four jobs: three complete
and one deliberately invalid fixture failed. No page checkpoints or scratch
directories remained after completion. SQLite quick-check returned `ok`, and
cgroup memory events reported no OOM events or kills.

The browser verified queue counts, the forced-OCR action and visible page progress
(six of twenty pages complete) while processing synthetic data.

## Existing-index upgrade

The local real-archive test instance at <http://127.0.0.1:8090> was upgraded using
its existing persistent volume. All 494 ready documents remained available, the
previous checked keyword still returned 12 documents, and startup queued zero
re-import jobs. Its automatic directory interval is now 15 minutes.

The added tables are initialized on opening the database; existing documents,
pages and FTS entries are retained. There is no cross-document OCR cache yet.
The mount-identity and access-control limitations described in the README remain.
