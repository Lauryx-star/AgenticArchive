# Access protection validation

Implemented and checked on 2026-10-07.

## Scope and extension points

The archive now requires authentication by default, including before initial
setup. A one-time, 256-bit code in the private data directory authorizes creation
of the first administrator. The browser chooses a username and a password of at
least 15 Unicode characters. No default account/password exists. Setup is atomic
and cannot be reused; account creation is independent of indexing.

`internal/access` owns the separate `access.db`, stable user IDs, roles, password
hashes, sessions and permissions. Initial setup creates an administrator; the
settings UI now supports listing and creating accounts, assigning admin/reader
roles and assigning a new password to another user. Every user has a personal
password-change form requiring the current password. Readers can retrieve archive
content and use personal settings, but cannot access archive settings or trigger
scans/retries/OCR. Unknown roles/permissions deny access. Directory scopes and
account deletion are not implemented.

Password hashing uses the Go standard library's PBKDF2-HMAC-SHA256 with a random
16-byte salt and 600,000 iterations. Parameters follow the
[OWASP password-storage guidance](https://cheatsheetseries.owasp.org/cheatsheets/Password_Storage_Cheat_Sheet.html).
The account database and setup-code file use mode 0600. Session and CSRF tokens
have 256 bits of randomness; only their hashes are stored in the account database.
Sessions resolve the current role on every request, last 12 hours and survive
server restarts. Logout and password changes/recovery revoke the affected sessions.
Personal changes revoke all of the caller's sessions; administrative resets revoke
all target sessions while preserving the actor's sessions. Administrative reset
cannot be used on the actor's own account to bypass current-password verification.
At most 20 live sessions are retained per user.

User-management writes recheck the actor's current administrator role inside an
immediate SQLite transaction. Role demotion also checks the remaining administrator
count in that transaction. Two concurrent self-demotions cannot remove every
administrator. Session creation compares the password verification's hash with the
current database hash under its write transaction, preventing stale verification
from issuing a session after a reset commits.

All archive routes share an outer authentication layer. Administrative handlers
also declare the specific required permission. Mutation requests require a
session-specific CSRF header and reject foreign browser origins. Account endpoints
require bounded, strict JSON; login/setup/create/change/reset password work shares
a serialized, globally limited budget
to ten attempts per minute. This modest global limit resets on restart and can
temporarily affect legitimate clients. HTTPS deployment requires a reverse proxy
preserving Host and explicit Secure-cookie configuration; authentication does
not encrypt HTTP.

## Automated verification

- All Go tests passed with the race detector; vet and whitespace checks passed.
- Actual application routes reject anonymous search, status, settings, document
  metadata, page text, PDFs, HEAD requests and mutation requests.
- Login page and its necessary assets remain public; archive UI assets redirect
  anonymous clients to login, including path-normalization variants.
- Setup rejects a wrong code, rejects weak passwords, removes the setup file,
  cannot create a second account and does not unlock anonymous access.
- Administrator scan/settings actions work with valid CSRF; missing/wrong CSRF,
  foreign origins, malformed/oversized JSON and excess login attempts fail.
- Changing an existing session's database role to reader immediately removes
  administrative rights. Normalized route redirects preserve permission checks.
- Tests cover persisted sessions, expiry, session storage limits, logout,
  password recovery, old-password rejection and Secure-cookie configuration.
- User tests cover creation, duplicate/invalid usernames, invalid roles, reader
  restrictions, role changes, stale actor permissions and concurrent demotions.
- Password tests cover wrong-current/weak-new password failures without session
  loss, self-service changes by both roles, administrative resets, rejection of
  self-reset and unknown fields/target IDs, targeted session revocation and stale
  verification being unable to issue new sessions. User-list JSON excludes secrets.

## Browser and Docker verification

An isolated Docker instance used generated text, scanned and broken PDF fixtures.
Setup succeeded through the HTTP API with synthetic credentials. In the browser,
the existing test account logged in, triggered a scan and displayed stored page
text. The session survived a container restart, and browser logout returned to
the login page. The login layout at 390 × 844 had no horizontal overflow.

The initial access rollout on port 8090 used `agenticarchive:access`, preserving
its original read-only source, index volume, one CPU and 512 MiB limit. Before
the update it had 494 ready documents and no failed documents. Afterwards, a
CLI reconciliation reported 494 unchanged, zero queued/deleted/failed PDFs; the
reference search still returned 12 matches. Anonymous status, settings, page-text
and PDF calls return 401. No real administrator credentials were created or read.
The user subsequently completed real account setup.

The user-management Docker build also passed its full test suite. An isolated
instance reused only the synthetic archive and accounts. HTTP checks exercised
account creation, reader restrictions, personal password change, administrative
password reset, target-session revocation and login with each assigned password.
The browser verified administrator account lists, disabled last-administrator
role controls, role-selection save enablement and the reset dialog. Reader settings
show only the personal account section; archive/user management are hidden. The
expanded personal password form at 390 × 844 has no horizontal overflow.

The real instance now runs `agenticarchive:users`. Its existing administrator
session remained valid after recreation. The browser displayed the personal
account section, the single original administrator, last-admin protection and
the saved 15-minute scan setting. No synthetic accounts were added to the real
instance and no real passwords were inspected or replaced. All 494 documents
remain ready. CLI reconciliation reported 494 unchanged and zero queued/deleted/
failed; the reference search still returned 12 results. Anonymous calls to the
new user-list and password endpoints return 401. The temporary fixture instance
was stopped after validation.
