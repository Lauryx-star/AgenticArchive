# AgenticArchive

Local PDF search with OCR, designed for personal archives and small businesses.

This is an early prototype, not a finished release. It recursively indexes PDFs,
extracts page text, runs German/English OCR on pages with fewer than 20 letters,
and provides word/phrase search with page references. Indexing and OCR run locally.
An optional second container adds a research chat with PDF sources using OpenAI or local Ollama.

PR security scanning and manual merge protection setup:
[Security checks and intentional admin bypass](docs/security-checks.md).

## Run with Docker

Copy `.env.example` to `.env`, configure the archive directory and model, and
create the shared service secret (only on first setup):

```sh
cp .env.example .env
mkdir -p archive
mkdir -m 700 secrets
openssl rand -hex 32 > secrets/agent-service-key.txt
docker compose up --build -d
```

The template selects local Ollama; install/run it and download `qwen3:8b` first,
or select OpenAI and save its key in `secrets/openai-api-key.txt`. For archive-only
operation, leave `COMPOSE_PROFILES` and `AGENT_URL` empty. See [agent setup](docs/agent.md).

Open <http://localhost:8080> (or your `ARCHIVE_PORT`). The default Compose configuration exposes the UI on
the local machine only. For NAS access, explicitly configure a LAN-accessible
port binding or a reverse proxy with HTTPS. Web UI, search, stored text, PDFs and
administrative API operations require authentication. Before the first account
exists, the archive is locked. Open the UI and create your administrator account
using the one-time setup code:

```sh
docker compose exec -T agenticarchive cat /data/setup-code.txt
```

Choose your username and a password of at least 15 characters. There is no default
password. The setup code is removed after successful setup and cannot create a
second account. For local development the code is in `<data>/setup-code.txt`.

Source PDFs are mounted read-only. The index is stored in the persistent
`archive-data` volume. The process runs as UID/GID 10001; source folders and files
must be readable by that user. Do not use `docker compose down -v` unless you
intend to remove the index.

The container is limited to 512 MiB with no additional swap and one CPU. OCR is
serial and limited to a 2400-pixel longest image dimension. This is a test target,
not a guarantee for arbitrary PDFs. Scaling down may reduce OCR accuracy.
Limits also apply to extracted text (4 MiB/page, 16 MiB/document) and page count
(2000/document). Tool operations time out after two minutes each.

## Optional chat and agent access

The Chat tab sits between Search and Documents. All services use one `compose.yaml`.
Enable the optional agent with `COMPOSE_PROFILES=${LLM_PROVIDER}` and set `AGENT_URL` in `.env`;
select OpenAI or local Ollama with `LLM_PROVIDER`.
Copy [`.env.example`](.env.example) to `.env` for documented model and archive settings.
Each signed-in user chats with their own temporary read identity. Users may
also create named, revocable read tokens for independent MCP agents in their
account settings. The archive works without an agent container.

See [agent setup, MCP tools, data flow and current reporting limits](docs/agent.md).
Ten-year invoice summaries still require a dedicated, validated reporting workflow
before they can be treated as complete financial reports.

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

## Access protection

The first account is an administrator. Identity records already carry a stable
user ID and a role, and handlers enforce explicit permissions on the server.
`admin` can read documents, manage settings and users, and trigger scans/retries/OCR.
The `reader` role can read/search documents and open PDFs and stored text, but
cannot read deployment settings or change them, trigger scans or retry OCR.
The Settings view is available to every user. “My account” allows changing one's
own password, requiring the current password. Administrator-only sections retain
the archive settings and add user management: list accounts, create users with
an initial password, change roles and assign a new password to another user.
An administrator changes their own password through “My account”, rather than
bypassing current-password verification through the reset endpoint.
At least one administrator must remain, even during concurrent role changes.
Usernames are immutable and case-sensitive. Directory-specific permissions and
account deletion are not implemented yet.

After a password change or administrative reset, all sessions and agent tokens for the affected
user are revoked. A self-service change returns the user to login with a success
message. Administrative resets leave the administrator's session intact and need
no email delivery: share the assigned password directly with the intended user.
An in-flight login verified with the previous password cannot create a new session
after the change commits. Roles are resolved on every request; the UI refreshes
when its regular status check sees a changed role.

Passwords are salted and stored as PBKDF2-HMAC-SHA256 hashes (600,000 iterations),
never as plaintext. Accounts and hashed session tokens live in `access.db` in the
persistent data directory, separately from the document index. Back up this file
alongside `archive.db`; rebuilding the index should retain the account database.
Sessions last at most 12 hours, survive server restarts and are invalidated by
logout. Session cookies are HttpOnly and SameSite=Strict. Mutating API calls need
an `X-CSRF-Token` obtained from `GET /api/auth/me`; cross-origin browser mutations
are rejected. Login, setup, account creation and password changes/resets share serialized
password work globally limited to
10 attempts per minute. This basic limit resets on restart and applies collectively
to all clients, including clients behind a proxy.

HTTP is suitable for local loopback development. For LAN or remote access, use
an HTTPS reverse proxy and set `AUTH_SECURE_COOKIE=true` in `.env` (or use
`serve -secure-cookie`). The proxy must preserve the public Host header. Forwarded
headers are deliberately not used to decide whether cookies are secure. Enabling
this setting requires accessing the UI over HTTPS. Authentication does not itself
encrypt HTTP traffic. The application still binds only to loopback by default
in Compose.

To recover or change an administrator password, supply a private password file
through stdin (one optional final newline is ignored):

```sh
docker compose exec -T agenticarchive reset-admin -username admin < /path/to/private/password.txt
# Local equivalent:
./bin/agenticarchive reset-admin -data ./data -username admin < /path/to/private/password.txt
```

Use the username chosen during setup. This local operation requires access to
the server/data directory, replaces the password and revokes all sessions and agent tokens for
that administrator. It leaves documents and scan settings intact. Passwords are
not accepted as command-line arguments or printed. Store the input file privately
and remove it after use. CLI scan/search/source acceptance remain local operations
controlled by operating-system access, rather than web sessions.

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

All archive endpoints below require a valid session cookie. Settings endpoints
and all scan/retry operations additionally require the corresponding administrative
permission. Authentication endpoints are:

- `GET /api/auth/state` — public; reports only whether initial setup is needed.
- `POST /api/auth/setup` — JSON `username`, `password`, `setup_code`; first admin only.
- `POST /api/auth/login` — JSON `username`, `password`; sets session cookies.
- `GET /api/auth/me` — current user, permissions and CSRF token.
- `POST /api/auth/logout` — revokes the current session; requires CSRF token.
- `PUT /api/auth/password` — JSON `current_password`, `new_password`; changes the
  caller's own password and revokes their sessions and agent tokens.
- `GET /api/users` — administrators only; returns account IDs, usernames and roles.
- `POST /api/users` — administrators only; JSON `username`, `password`, `role`
  (`reader` or `admin`); creates an account, returning HTTP 201.
- `PUT /api/users/{id}/role` — administrators only; JSON `role`; preserves the last
  administrator and enforces the actor's current role inside the transaction.
- `PUT /api/users/{id}/password` — administrators only; JSON `password`; assigns a
  new password to another account and revokes that account's sessions and agent tokens.
- `GET /api/auth/tokens` — own active personal read-token metadata.
- `POST /api/auth/tokens` — JSON `name`, `days` (1–365); returns a secret once.
- `DELETE /api/auth/tokens/{id}` — revokes an owned token.
- `GET /api/chat/state` — reports whether an agent is configured.
- `POST /api/chat` — bounded alternating `messages` (`role`, `content`); returns
  answer text and read source pages. Requires the optional agent service.

`/mcp` uses personal bearer tokens instead of cookies; see [MCP details](docs/agent.md).

Every mutating authenticated endpoint requires the session's CSRF header.
User-management/password JSON rejects unknown fields; usernames are limited to
64 UTF-8 bytes and passwords to 15 or more Unicode characters, at most 1024 bytes.


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
- Consider directory-level permissions and further account management controls.
- Extend the optional research chat with validated reporting and additional LLM providers.
