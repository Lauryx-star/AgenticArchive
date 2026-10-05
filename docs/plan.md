# Product and implementation plan

## Agreed direction

- Working name: AgenticArchive.
- Audience: individuals and very small businesses; initial archive approximately 5 GiB.
- Recursive PDF indexing, including photographed documents saved as PDF.
- Fully local text extraction, German/English OCR and keyword/phrase search.
- English codebase, identifiers, comments and technical documentation.
- Mobile-first German web UI with automatic, light and dark modes.
- Docker deployment; initial target Synology DS218+, `linux/amd64`.
- 512 MiB is a resource-validation target, not a measured minimum requirement.
- Read-only originals; persistent, rebuildable index.
- Incremental/manual scans, modification-date filters, paging and page-level sources.
- GitHub source and container registry publishing later; no release is published yet.
- Copyleft desired, including commercial use; AGPLv3 proposed, existing GPLv3 retained.

## Prototype acceptance

1. Extract and search a text PDF.
2. OCR and search an image-only PDF.
3. Record extraction failures without stopping other documents.
4. Update and remove documents after an incremental scan.
5. Preserve the index across restarts and unavailable source directories.
6. Show grouped hits, page references, date filters and paging in the API/UI.
7. Measure real container memory with serial OCR under a 512 MiB limit.

## Scope boundaries

The import queue now stores completed pages and resumes after interruption.
Database uniqueness protects against repeated interval scans and concurrent
discovery. OCR selection still uses a simple letter-count heuristic; forced OCR
and explicit retry are available, but selective OCR on mixed pages needs further
work. Synthetic scan fixtures do not establish accuracy on phone photos.

Creation-date support depends on filesystem capabilities. Semantic search,
document dates, authentication and an agent adapter are separate milestones.
An agent must receive source paths and page numbers, and later treat document
content as untrusted evidence rather than instructions.

## Development milestones

1. **Prototype:** Go, SQLite FTS5, Poppler, Tesseract, CLI/API, small responsive UI.
2. **Reliability:** real archive sampling, resource measurements, mount identity,
   resumable processing, explicit OCR retry/override and document status details.
3. **First release:** finalize license, secured deployment guidance, tested AMD64
   image, GitHub CI and container publishing.
4. **Agent integration:** search/retrieval adapter and optional local/remote model
   providers; natural-language document selection with verifiable sources.
