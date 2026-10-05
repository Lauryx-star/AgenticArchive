# Real archive validation — 2026-10-05

## Setup

User-provided test PDFs were mounted read-only into a separate local test
container. Its persistent index is separate from the synthetic demo index.
No original files were changed and no document content was sent to an external
OCR or LLM service. This report contains aggregate measurements only.

- Host: Apple Silicon Mac with Docker Desktop; container architecture ARM64.
- Runtime: the existing `agenticarchive:prototype` image.
- Limits: 512 MiB memory, no additional swap, one CPU, serial OCR.
- UI: <http://127.0.0.1:8090>.
- Container: `agenticarchive-real-test`.
- Persistent volume: `agenticarchive-real-test-data`.

## Results

| Measurement | Observed value |
| --- | --- |
| PDF files | 494 |
| Input size | 559.7 MiB |
| Source pages / indexed pages | 1,246 / 1,246 |
| Largest source document | 100 pages |
| PDFs without an existing text layer | 158 |
| Pages processed with OCR | 295 |
| Ready / failed documents | 494 / 0 |
| Approximate initial import duration | 13.3 minutes |
| Peak cgroup memory | 288,706,560 bytes (275.3 MiB) |
| Memory limit / OOM events / OOM kills | 512 MiB / 0 / 0 |
| Repeat incremental scan | 494 unchanged, 0 updated, 0 deleted |
| Repeat scan duration | 0.117 seconds in this single run |
| SQLite quick check | `ok` |
| Pages returning no text | 20 |

Import duration was calculated from container startup to the final monitoring
sample; polling adds up to approximately ten seconds of uncertainty. Cgroup
memory includes charged filesystem cache as well as process memory. These are
observations from one run, not performance guarantees.

## Search and source checks

Two user-supplied keywords and their AND combination returned 12, 49 and 7
matching documents, respectively. Queries took about 2–22 milliseconds in the
post-import sample. Searches also returned results while OCR was running.

An independent extraction of existing source PDF text layers identified 3 and
35 documents with the respective complete words. All of those documents appeared
in the search results; no reference document was missing. Additional results
include documents made searchable through OCR. Sample page references were valid.

This verifies retrieval of known text-layer matches and technical source mapping.
It does not establish OCR accuracy for every page. Twenty pages returned empty
text. The user subsequently reviewed those pages and confirmed that they contain
no readable content. This manual review supports the empty-page result for this
sample, without establishing universal OCR accuracy.

## Next validation

- Review more representative OCR pages; the twenty empty-text pages were checked
  by the user and contain no readable content.
- Validate rotation, shadows and perspective on phone-photo PDFs.
- Build and execute the AMD64 image on the Synology DS218+.
- Measure the complete approximately 5 GiB archive on the target NAS.

The separate test instance remains available locally with the completed index.
Stop it with `docker stop agenticarchive-real-test` when no longer needed. The
test index is private derived data and should not be added to the repository.
