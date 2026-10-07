# AgenticArchive

Local PDF search with OCR, designed for personal archives and small businesses.

This is an early prototype, not a finished release. It recursively indexes PDFs,
extracts page text, runs German/English OCR on pages with fewer than 20 letters,
and provides word/phrase search with page references. Everything runs locally.

PR security scanning and manual merge protection setup (German):
[Sicherheitsprüfungen und bewusster Admin-Bypass](docs/security-checks.md).

## Run with Docker

Create an archive directory or point `ARCHIVE_PATH` at an existing directory:

```sh
mkdir -p archive
ARCHIVE_PATH=/absolute/path/to/pdfs docker compose up --build -d
```

Open <http://localhost:8080>. The default Compose configuration exposes the UI on
the local machine only. For NAS access, explicitly configure a LAN-accessible
port binding or a reverse proxy. This prototype has no login; use a trusted network.

Source PDFs are mounted read-only. The index is stored in the persistent
`archive-data` volume. The process runs as UID/GID 10001; source folders and files
must be readable by that user. Do not use `docker compose down -v` unless you
intend to remove the index.

The container is limited to 512 MiB with no additional swap and one CPU. OCR is
serial and limited to a 2400-pixel longest image dimension. This is a test target,
not a guarantee for arbitrary PDFs. Scaling down may reduce OCR accuracy.
Limits also apply to extracted text (4 MiB/page, 16 MiB/document) and page count
(2000/document). Tool operations time out after two minutes each.

## Local development

Install Go 1.26 or newer, a C compiler, SQLite build prerequisites, Poppler
(`pdfinfo`, `pdftotext`, `pdftoppm`) and Tesseract with `deu` and `eng` language data.
The application uses the CGO SQLite driver; the `sqlite_fts5` build tag is required.

```sh
go test -tags sqlite_fts5 ./...
go build -tags sqlite_fts5 -o bin/agenticarchive ./cmd/agenticarchive
./bin/agenticarchive serve -root ./archive -data ./data
```

The codebase, comments and technical documentation are English. UI text is German.
The UI uses a mobile-first layout and automatic/light/dark appearance settings.
Search excerpts highlight matched words and quoted phrases with a text marker.
The search API retains plain `snippet` text and also returns `snippet_parts`
(text plus match flag). Document content is rendered as text, never HTML.
The Documents view lists all imported PDFs, including queued and failed ones,
with 20 documents per page. It supports literal file/path filtering, status
filters, and sorting by file path or last modification. Ready documents show
page and OCR counts; PDFs without recognized text are labeled explicitly.
PDF links and retry/forced-OCR actions are available directly from the list.
Ready documents also offer a stored-text viewer. It displays the exact indexed
text one page at a time, preserves line breaks, identifies OCR pages, and supports
previous/next navigation or jumping to a specific page. It reads the existing
page API without extracting the source PDF again or loading the entire document.

The Settings view shows the configured archive/index paths and lets you enable,
disable, or change automatic reconciliation (1–10080 whole minutes). Changes apply
without restarting and are saved in the index volume. The `serve -interval` flag
only seeds a new index; saved settings take precedence after container recreation.
Disabling periodic scans does not disable startup reconciliation, manual scans,
or processing already queued PDFs. The next periodic wait starts after a scan
finishes, and changing the interval resets that wait. A running scan is allowed
to finish. Container paths are read-only in the UI; change source bind mounts
through Docker configuration. Appearance remains a per-browser preference.

## Command line

```sh
./bin/agenticarchive scan -root ./archive -data ./data
./bin/agenticarchive scan -root ./archive -data ./data -full
./bin/agenticarchive search -data ./data -query '"liability insurance"'
./bin/agenticarchive serve -root ./archive -interval 15m
```

Incremental scans use path, size and modification time to identify candidates,
then SHA-256 to avoid re-extracting unchanged content. Full scans verify every
fingerprint, including files whose size and timestamp were preserved. Failed
jobs remain visible until explicitly retried or the source changes. Missing documents are removed only after a complete
directory walk. Symbolic links are not followed.

## Durable import queue

Directory discovery and PDF processing run independently. SQLite enforces one
job per document path identity and content fingerprint. Repeated automatic or
manual scans do not duplicate or reset queued/running jobs. Failed jobs are not
silently retried every interval. Identical PDFs at different paths remain separate
documents; cross-document OCR caching is not implemented yet.

Each completed page is checkpointed in the persistent index. After a restart,
the worker resumes at the remaining pages. An operating-system file lock permits
only one worker per index and is released automatically after process death.
This deployment supports Linux containers and local macOS development.

Processing uses an immutable temporary copy on disk, verifies its fingerprint,
and checks the source again before publication. New source versions supersede old
work. Page checkpoints and final index publication also verify the current
database version, so an obsolete worker cannot overwrite a newer queued version
or recreate a deleted document. Partial text is not published as search results.
Temporary processing files are kept in the reserved `<database-path>.work`
directory and cleared by the next worker after an interrupted run.

The UI shows waiting jobs and completed pages for the active document. Failed
documents can be retried, and search hits offer a forced OCR action. A normal retry
retains checkpoints for the same version; changing OCR mode or explicitly forcing
OCR starts fresh checkpoints while reusing the job identity. Existing ready indexes
are retained during upgrade and do not require re-import.

The index records the source directory's filesystem device and inode. Missing or
replaced roots stop discovery, pause the worker with its checkpoints intact, and
block PDF downloads from the replacement source. Searchable indexed text remains
available. Restoring a mount with the original filesystem identity resumes processing
automatically. A restored mount with a new identity requires explicit acceptance.
Existing indexes enroll only after finding an identical PDF at its indexed path.

An unexpectedly empty archive never automatically deletes all indexed documents.
After verifying that removing every PDF was intentional, run a one-off scan:

```sh
./bin/agenticarchive scan -root ./archive -data ./data -allow-empty
```

If the archive was deliberately relocated, or its filesystem identity changed
following a NAS reboot/remount, stop the server, verify the mounted source, and
explicitly accept its identity before restarting:

```sh
./bin/agenticarchive accept-source -root ./archive -data ./data
```

For Docker, use the same source bind and index volume with the `accept-source`
command, e.g. `docker compose run --rm agenticarchive accept-source`. Accepting an identity does not itself remove or re-import files;
the next scan reconciles the accepted source. Keep `-allow-empty` restricted to
one-off CLI scans; it is unavailable for periodic scans or the Web UI.

This guard is conservative: device/inode identifiers can change across remounts,
and cannot identify every malfunction within a nested network mount. A partial
but readable listing with the same root identity can still resemble real deletion.
The source directory remains read-only; no marker file is written into it.

## API

- `GET /api/search?q=insurance&page=1&limit=20&sort=relevance`
  (`after`/`before`: inclusive UTC file modification dates, `YYYY-MM-DD`;
  `sort`: `relevance` or `modified`; maximum page size: 100)
- `GET /api/documents?page=1&limit=20&status=ready&sort=modified&path=insurance`
- `GET /api/documents/{id}`
- `GET /api/documents/{id}/pages/{number}`
- `GET /api/documents/{id}/pdf`
- `GET /api/status`
- `GET /api/settings`
- `PUT /api/settings` with JSON `{"scan_interval_seconds":900}` (0 disables periodic scans; other values must be whole minutes between 60 and 604800 seconds)
- `POST /api/scan` (add `?full=true` for fingerprint verification)
- `POST /api/documents/{id}/retry` (add `?ocr=true` to force OCR on every page)

`GET /api/status` includes a `queue` object with pending/running/failed/done
counts and active-document page progress. Scan `queued` counts refer to newly
scheduled jobs; the CLI `scan` command discovers and drains jobs if no server
worker already owns the index. The server continuously processes newly queued jobs.

Search terms are literal: separate words are combined with AND; double quotes
group phrases. Results are grouped by document and show its best matching page.
The prototype does not perform semantic search or extract document issue dates.
Creation dates are not currently implemented because they are not portable across
the intended filesystems. PDF page navigation depends on the browser's PDF viewer.

## Reproducible OCR smoke test

With Python 3 and Poppler available:

```sh
python3 scripts/create-fixtures.py /tmp/agenticarchive-fixtures
./bin/agenticarchive scan -root /tmp/agenticarchive-fixtures -data /tmp/agenticarchive-test-data
./bin/agenticarchive search -data /tmp/agenticarchive-test-data -query Hausrat
```

The fixture set includes a text PDF, an image-only PDF and a deliberately broken
PDF. These are synthetic documents, not real private archive content.

## License

The existing repository license is GPLv3; it has been preserved. AGPLv3 is proposed
for the eventual release so modified hosted versions also offer their source to
their users. The license decision must be finalized before publishing a release.
The UI includes a link to the original project; this is distinct from license
requirements and is not a custom mandatory attribution clause.

## Next steps

- Validate OCR accuracy and memory usage on representative phone-photo PDFs.
- Test `linux/amd64` on a Synology DS218+ (the development Mac uses ARM64).
- Expand document management and OCR quality controls beyond forced OCR/retry.
- Add authentication for deployments outside a trusted local environment.
- Add optional agent access and provider-independent LLM integration later.
