# Document overview validation

Implemented on 2026-10-05.

The Documents view lists ready, queued, processing and failed PDFs independently
of full-text search. The backend returns one page (20 documents by default, up to
100 through the API), a matching total, and per-document page/OCR/text counts.
Filters match literal file/path substrings and document status. Sorting uses file
path or modification time with stable tie-breakers. List queries use a database
transaction so totals and page contents share a consistent snapshot.

## Verification

- Go race tests and vet passed. Docker builds run the Go tests.
- Backend tests cover 25-document pagination, stable ordering, combined filters,
  page/OCR/text counts, pending/processing/error statuses, out-of-range empty
  pages, literal wildcard characters and invalid sort/status/pagination values.
- A separate Docker preview imported 27 synthetic PDFs: 26 ready, one intentionally
  invalid PDF failed. UI navigation showed 20 documents on page one and seven on
  page two; the error filter showed the single failed document and retry actions.
- A path filter returned nine matching documents; a nonexistent path produced
  the empty-result message.
- Mobile dark mode checked at 390 × 844: document viewport width and scroll width
  both 390 pixels, without horizontal overflow. Desktop light mode checked at
  1100 × 900.
- The existing search remains a separate view. No original PDF is modified by
  listing, filtering, sorting, or pagination.

Modification dates describe the file, not a date extracted from its content.
The path filter uses SQLite's built-in lowercasing (ASCII case-insensitive;
non-ASCII characters are matched literally). Text counts are shown only for
ready documents, because an in-progress replacement can retain its old indexed
pages until publication.

## Existing archive upgrade

The updated real instance on port 8090 reports 494 ready documents, zero errors,
zero pending/running jobs, and 494 unchanged files on its first scan. Page 25 of
the ready-document list contains the remaining 14 entries. The known
`Vertragsrecht` search still returns 12 documents. No re-import was performed.
The final UI also returns focus to the document heading when moving between
pages, and displays small files in KiB instead of rounding them to zero MiB.

## Stored text viewer (2026-10-05)

Ready documents now offer "Gespeicherten Text ansehen". A modal dialog displays
the exact saved page text from the existing page API with preserved line breaks,
OCR origin, previous/next navigation and a page-number jump. Only one page is
loaded at a time; the original PDF is not re-extracted. API responses for stored
pages now use `Cache-Control: no-store`.

Docker build tests passed. Browser checks used synthetic PDFs: a 20-page document
was opened, advanced to page two, and jumped to page 20, where Next was disabled.
An empty saved page displayed an explicit message. An OCR page displayed its saved
"Hausrat Versicherung Nachweis 2025" text and OCR label. Escape closed the dialog
and returned focus to the opener. Mobile dark mode and desktop light mode were
visually checked. Document text is assigned as text content, never rendered HTML.
