# Archive chat and personal agents

The archive UI has a Chat tab between Search and Documents. A shared optional
agent service runs in a second container. Every web chat request receives its
own short-lived read token bound to the signed-in archive user. The token is
removed after the request, expires after five minutes if cleanup fails, and is
never exposed to the browser or OpenAI. The agent service receives no PDF mount,
database mount, archive login cookies, passwords, or archive administrator rights.

## Enable the optional container

Copy [`.env.example`](../.env.example) to `.env` beside `compose.yaml` and
edit the settings there. Do not overwrite an existing `.env`; transfer the desired
values instead. There is one Compose file for archive-only, OpenAI and Ollama.
Archive paths, port, persistent volume, provider and service URLs are configured
in `.env`.

Create `secrets/agent-service-key.txt` containing a random service secret of at
least 32 characters. This file is required by the Compose stack, including when
running only the archive. For OpenAI, also create `secrets/openai-api-key.txt`
containing the raw API key. Ollama does not require this second file.
Keep the directory private. On Linux, UID 10001 must be able to traverse/read
the files; use suitable ownership or ACLs. The archive and Ollama agent receive
only the shared service key; the OpenAI agent additionally receives its API key.
No complete secret directory is mounted. Never paste secrets into chat
or put them into tracked files.

```sh
mkdir -m 700 secrets
# Generate only when first setting up; do not overwrite an existing service key.
openssl rand -hex 32 > secrets/agent-service-key.txt
# For OpenAI, save your API key in secrets/openai-api-key.txt with a local editor.
docker compose up -d --build
```

`secrets/` is excluded from Git and both Docker build contexts. Set
`COMPOSE_PROFILES=${LLM_PROVIDER}` and `AGENT_URL=http://archive-agent:8081` to enable Chat.
To run without the agent, set both values empty and stop any already running
agent with `docker compose --profile "*" stop archive-agent-openai archive-agent-ollama`; disabling a profile does not
stop existing containers. The agent publishes no host port. Choose only one profile, `openai` or `ollama`.
Both use the internal DNS alias `archive-agent`. When changing providers, stop
the old agent first with the command above so only one uses that alias.

For existing deployments, set `ARCHIVE_PATH`, `ARCHIVE_PORT`,
`ARCHIVE_DATA_VOLUME` and `ARCHIVE_DATA_EXTERNAL` before restarting. Reuse the
exact existing volume name to preserve accounts, tokens, settings and index.
External volumes must already exist. Do not use `docker compose down -v`.

The provider defaults are `gpt-5.4-mini` for OpenAI and `qwen3:8b` for Ollama;
leave `OPENAI_MODEL` and `OPENAI_API_URL` empty to use those defaults.
The implementation uses the [OpenAI Responses API with function calling](https://developers.openai.com/api/docs/guides/function-calling)
and preserves returned reasoning items in the stateless tool loop. It sets
`store: false`; this is not a guarantee of zero retention of API traffic.
The API key remains on the agent service. Questions, conversation history,
search results, document metadata and requested page text are sent to OpenAI.
Original PDF files are not uploaded.

## Local Ollama instead of OpenAI

On an Apple Silicon Mac, run [Ollama](https://ollama.com/download/mac) natively
to use Metal GPU acceleration; the archive and agent can remain in Docker.
Qwen3 8B is a starting point for an M3 Pro with 18 GB unified memory; Qwen3 4B
is an alternative if memory pressure or response latency is too high. Actual
memory use also depends on context size and other running applications.
Download the base model with `ollama pull qwen3:8b`, then create the archive
configuration with `ollama create agenticarchive-qwen3:8b -f ollama/Modelfile`.
It shares the base model weights and sets a 16,384-token context and temperature
0.2. A larger context needs more memory; the actual allocation can be checked
with `ollama ps`. The plain Qwen3 default can have only 4,096 tokens on this Mac,
which is too small for longer research histories. Do not select a cloud tag
if you want archive text to remain local.

Set these values in `.env`:

```dotenv
LLM_PROVIDER=ollama
OPENAI_MODEL=agenticarchive-qwen3:8b
OPENAI_API_URL=http://host.docker.internal:11434/v1/responses
```

Start both containers with the same command used for OpenAI:

```sh
docker compose up -d --build
```

Only `secrets/agent-service-key.txt` is required for Ollama. Ollama mode never
reads or sends an OpenAI key, even if one already exists in the agent secret
file. Thinking is disabled for faster initial
tests. The same bounded archive tools, user permissions and citations apply.
Ollama must be reachable from the agent container at the configured URL. If its
loopback listener is inaccessible through Docker Desktop, configure Ollama's
`OLLAMA_HOST` to a reachable interface and restrict access with the host firewall;
do not expose its unauthenticated API publicly.

Use a recent Ollama version with [Responses API and function calling](https://docs.ollama.com/api/openai-compatibility).
Compatibility must include tool-call/result replay; support for Chat Completions
alone is insufficient. A synthetic Qwen3 8B tool-call/result/answer exchange has
been verified locally with Ollama 0.40.2; answer accuracy on real archive research
has not been validated. Local
generation shares the 60-second per-call and 240-second research limits;
slow models may time out. The agent rejects answers that never call an archive
tool, including servers that ignore the requested tool choice.

For other authenticated Responses-compatible servers, use `LLM_PROVIDER=openai`,
their full HTTPS Responses URL in `OPENAI_API_URL`, their model name, and their
key in the existing secret file. Provider compatibility varies. Only configure
trusted endpoints: they receive questions, search results and requested page text.

For a different machine, set archive-server `AGENT_URL` to the agent service's
base URL and agent-server `ARCHIVE_MCP_URL` to the archive's `/mcp` URL. Supply
the same `AGENT_SERVICE_KEY_FILE` on both sides, and `OPENAI_API_KEY_FILE` only
on the agent. Outside a private container network, use HTTPS and restrict
access to the agent service. Arbitrary endpoints cannot be supplied by chat
requests. Redirects are not followed, preventing forwarded bearer secrets.

## Personal read tokens and MCP

Every archive reader, including administrators, can create named personal tokens
under Settings → My account → My agent tokens. Tokens expire after 1–365 days,
with at most 20 active personal tokens per user. The full secret appears once
in the creation response and UI; lists contain metadata only. Only SHA-256 hashes
are persisted. Switching away from settings, closing the details panel or
leaving the page clears the displayed secret.

Tokens authenticate **only** `/mcp`, using `Authorization: Bearer aa_…`. They do
not authenticate the browser/API routes, including settings, scans, password
changes or token management. Even an administrator's token grants only archive
reads. Owner identity, current role, expiration and token ID are resolved for
every MCP request. Password changes and password resets revoke all owned tokens
and sessions. Users can revoke their own tokens individually. Ordinary logout
preserves personal tokens intended for independent agents.

The minimal stateless MCP endpoint uses [Streamable HTTP](https://modelcontextprotocol.io/specification/2025-06-18/basic/transports),
protocol `2025-06-18`, JSON responses to POST, and authenticated GET/DELETE
returning 405 (no SSE or session termination). Send `Content-Type: application/json`,
`Accept: application/json, text/event-stream` and, after initialization,
`MCP-Protocol-Version: 2025-06-18`. Initialize, then send
`notifications/initialized`, then `tools/list` / `tools/call`. Cookies alone do
not authenticate MCP. Foreign Origin and Sec-Fetch-Site headers are rejected.
This version uses manually supplied bearer tokens, without OAuth discovery.

Available tools:

- `search_archive(query, page)`: 20 grouped full-text index matches in `hits`,
  with count `total`, plus up to 20 filename substring matches in
  `filename_matches` with count `filename_total`. Both lists use the same page
  number and can overlap. Indexed text and indexed paths are searched together;
  substring filename matches also include documents not yet indexed. A hit
  represents one page of a document, not every matching page. Missing page
  defaults to 1; explicit invalid page values remain errors.
- `list_documents(path, page)`: 20 document records with page counts and status.
- `read_page(document_id, page, offset)`: up to 12,000 Unicode characters,
  `next_offset` for remaining text, OCR flag and a citation marker. Missing
  offset defaults to 0. Missing `list_documents` page also defaults to 1.

Indexed text remains available even if the original archive mount is missing;
PDF source links still enforce the existing source identity check.

Each request has a typed principal containing both user ID and token ID. This
provides the identity needed for a future audit log. **No audit log or per-directory
permissions have been implemented yet.** All readers currently read the same archive.

## Chat limits and evidence

Chat history lives only in the current browser tab's memory and is cleared by
reload or navigation away from the page. The browser carries a signed, user-bound
working context containing successful answers and their source receipts. Each
continuation still requires a current session and CSRF; a tampered context or
one belonging to another user is rejected. Context expires after 12 hours of
inactivity and is not encrypted or a durable conversation database.

Choose **Im Archiv recherchieren** for new evidence, or **Bisherige Ergebnisse
zusammenführen** for synthesis of previous partial results without archive tool
calls. Sources remain linkable, and incomplete research remains incomplete.
Failures preserve the previous context. No fixed twelve-question cutoff applies:
above 12 KB of model text or 20 working messages, old exchanges are condensed
into research memory while the visible transcript stays intact. Repeated lines
are compacted with their multiplicity recorded. Cited result paragraphs are
retained verbatim independently of model-generated context notes; at most 16 KB
of such excerpts fit the working notebook. If that limit is exceeded, the
request fails explicitly and the previous context and visible results remain
available; further research then needs a new chat. Compression can
lose detail; the UI announces it and original documents remain authoritative.
The bounded working request accepts at most 24 messages / 96 KB of text, user messages at most 16 KB and assistant messages
at most 32 KB, and 512 source references per message. Compression calls are
additional to the research loop (at most thirty-two chunks and thirty-two merges per request),
share the 240-second timeout, and may incur provider costs. A question is at most 4,000 characters
in the UI. Two agent requests can run concurrently. A request lasts at most
240 seconds; its research loop uses at most 24 Responses calls (one reserved for finalization)
and 64 archive tool calls, and
has bounded input/output sizes. HTTP/provider failures are not automatically
retried. In Ollama mode, an initial answer without tool calls is discarded and
receives one explicit tool-use correction within the same model-call budget.
An answer that merely announces more research is not returned as a final answer.
The agent allows up to two research corrections within the same budget. After
one empty full-text query, it requires another distinct query or a filename
lookup before accepting an answer with no read sources. This is a recovery
guard, not a guarantee of exhaustive discovery or factual answer accuracy.
Full-text terms are combined with AND within an indexed page/path entry.
The additional filename lookup matches the query as a path substring.
OR and wildcards are unsupported. A failed tool or discovered candidates with
no read source pages also trigger recovery. For candidates, the agent supplies
explicit document/page arguments for up to three next read calls, rather than
only asking the model generally to continue.

One model call is reserved for a tool-free partial summary when round, time,
input or tool limits are reached. Actually read page text and previous grounded partial answers are used for that
summary (up to 32 KB); search snippets are excluded. The response receives a
server-generated incomplete-result notice. If no page was read, the agent still
returns an explicit failure. Repeating the same canonical tool arguments more
than twice also ends research with this partial-summary path. Input is capped
at 40 KB for Ollama and 200 KB for other providers. Read identities still expire
after five minutes and are revoked when the request finishes.

The agent always addresses the user informally in German. Chronological lists
use document dates, amounts, amount types and page citations; missing facts
remain unknown.

The first model response must call a read tool. The agent only exposes the three
allowlisted archive tools, treats document text as untrusted evidence and receives
instructions to report incomplete searches. Citations link only to pages actually
read during that request; the UI marks other citation markers as unverified.
The archive resolves source paths itself rather than trusting returned paths.
Model text is rendered as plain text, never HTML. The source list represents
pages read, not a guarantee that every answer claim is supported.

This is a first research chat, not yet a validated financial reporting engine.
A ten-year energy overview can exceed the current research limits. Exhaustive
invoice discovery, structured extraction, duplicate/storno handling and checked
decimal arithmetic need a dedicated reporting workflow before promising complete
annual totals or exports.

## Validation

Automated tests cover owner isolation, CSRF, hashed storage, expiry, revocation,
password-reset revocation, stale-session token issuance, personal-token limits,
temporary-token cleanup, the MCP read-only boundary, Origin checks, explicit setup
state and a mocked OpenAI search/read/answer loop with reasoning replay. These
tests do not make OpenAI calls or use a real API key. A live provider test remains
necessary once a key is configured.

The opt-in local Ollama integration uses the real runner and authenticated MCP
endpoint with an isolated, indexed archive containing two synthetic invoices:

```sh
OLLAMA_TEST_URL=http://127.0.0.1:11434/v1/responses OLLAMA_TEST_MODEL=agenticarchive-qwen3:8b go test -tags sqlite_fts5 ./cmd/agenticarchive -run TestLiveOllamaResearch -v
```

It verifies that both pages are actually read and that the newer invoice date
appears in the answer. It is skipped by default and never accesses the deployed
archive or real user credentials. Real archive discovery and answer accuracy
still depend on model behavior and the research limits.

The opt-in `TestLiveOllamaChronologicalAmounts` also verifies dates and amounts
in chronological order with two read synthetic invoice sources. Budget tests
cover research beyond the previous eight rounds, repeated-call termination and
grounded partial results instead of discarding all findings.
