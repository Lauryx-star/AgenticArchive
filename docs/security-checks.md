# Security Checks and Merge Protection

**This PR does not enable repository rules.** A failing workflow alone does not
prevent a merge. Lauryx-star must merge this PR and then enable the rule described
below to block normal future merges. No settings are changed automatically, and no
PATs are required.

## Checks

`.github/workflows/security.yml` runs for **all pull requests**, including those
from forks, when opened, reopened, and whenever a new commit is pushed; on pushes
to `main`; manually via **Actions → Security → Run workflow**; and every Monday
at 06:23 UTC. There are no path filters; newer PR runs cancel outdated runs.

| Job | Coverage and failure behavior |
| --- | --- |
| Go vulnerabilities | govulncheck v1.8.0; Go version from `go.mod`, with CGO enabled. `-scan=module` blocks known vulnerabilities in dependencies even without proven reachability; `-scan=package -tags=sqlite_fts5` checks imported packages in the FTS5 configuration. Unlike the default symbol mode, it does not evaluate only vulnerable functions that can be called. |
| Static security analysis | Semgrep 1.179.0 with the `go/lang/security`, `javascript/lang/security`, and `python/lang/security` security directories from the maintained upstream `semgrep/semgrep-rules`, pinned to commit `a84ff9cc2453ca91d581380de4b8b3f272f6f4be`. `--error --strict` fails on findings and scan/configuration errors; `nosemgrep` comments do not suppress findings. |
| Repository vulnerabilities, secrets and configuration | Trivy 0.75.0 checks dependencies (including `go.sum`), secrets, and supported misconfigurations (including Dockerfile). Secret values are masked in Trivy's table report. |
| Container vulnerabilities | Local Docker build, including existing Go tests; Trivy scans the finished image, including Debian, Poppler, Tesseract, and the Go binary. No image push or registry login. |
| **Security gate** | Runs with `always()` and succeeds only if **all four** scan jobs succeeded. Failures, cancellations, unexpected skips, and scanner/download/build errors do not result in a green gate. A completely cancelled run also does not produce a successful required check. |

All Semgrep finding levels and all Trivy-supported severities
**UNKNOWN, LOW, MEDIUM, HIGH, CRITICAL** block. Vulnerabilities without an
available fix remain findings; there is no baseline, no `continue-on-error`, and
no `--ignore-unfixed`. Trivy does not use a repository ignore file, repository
configuration, or custom secret configuration. A preceding check rejects inline
Trivy/tfsec suppressions because they could otherwise remove findings before
evaluation. Trivy's logged ERROR/FATAL messages (for example, falling back to
embedded checks after download failures) also fail the repository job. govulncheck
has no severity filter.

The jobs use `pull_request`, **not** `pull_request_target`, read-only repository
permissions, and checkouts without persisted credentials. There are no repository
secrets or privileged owner tokens. Untrusted PR code runs only on ephemeral GitHub
runners, never in the owner's context.

## Investigating failing checks

In the PR, open **Checks → Security → failed job → scan step**. The logs show the
rule/vulnerability ID, file/package, severity, and, where available, a fixed
version. The gate logs only job results. No reports/artifacts containing secret
values are uploaded. Revoke/rotate any discovered secrets immediately, then remove
them; do not copy them into comments. Semgrep may output source-code excerpts, and
Docker logs the build: do not put credentials in source code or build commands.
Resolve scanner errors (e.g. network, database, parser, or timeout errors) and
rerun; a missing scan is not evidence of security.

## Protecting `main`: manual setup by the owner

1. Merge this PR; if needed, wait for an initial workflow run or start one manually
   on `main`. The check may only become selectable after that.
2. Open [Settings → Rules → Rulesets](https://github.com/Lauryx-star/AgenticArchive/settings/rules).
   Review existing rules; if a matching rule already exists, edit it instead of
   creating a conflicting duplicate. Otherwise, select **New ruleset →
   New branch ruleset**, with a name such as `Main security`.
3. Select **Enforcement status: Active** (not Disabled/Evaluate).
4. Under **Target branches → Add a target → Include default branch**, select the
   default branch; it is currently `main`. Do not add an exception for `main`.
5. Enable **Require a pull request before merging**.
6. Under **Require status checks to pass → Add checks**, add exactly **`Security gate`**
   and select **GitHub Actions** as the source. Enable **Require branches to be
   up to date before merging**. No individual scan jobs need to be required.
7. Under **Bypass list → Add bypass**, add the **Repository admin** role. Select
   **For pull requests only** beside **Always allow**. Do not leave it as “Always
   allow”: the bypass must not permit direct pushes.
8. Review the settings and confirm **Create / Save changes**. Then verify on a test
   PR that a failing `Security gate` blocks a normal merge and a current passing
   check permits a normal merge.

Lauryx-star currently has the admin role and can therefore choose the offered option
to bypass the rules in the PR merge dialog when a check is failing, then confirm the
merge. Document the reason and accepted risks in the PR; the scan remains red.
**This bypass also applies to future repository administrators, not exclusively to
the Lauryx-star username.** Other overlapping rulesets/branch-protection rules may
still prevent a merge and must be considered separately.

Official guidance:
[Create a ruleset and configure a PR-only bypass](https://docs.github.com/en/repositories/configuring-branches-and-merges-in-your-repository/managing-rulesets/creating-rulesets-for-a-repository),
[available rules and required checks](https://docs.github.com/en/repositories/configuring-branches-and-merges-in-your-repository/managing-rulesets/available-rules-for-rulesets).

## Maintenance and limitations

Action references are full, verified commit SHAs with release comments. Scanner
and Semgrep rule versions are pinned; Trivy downloads are verified against the
official SHA-256 checksum before execution. For updates, check the upstream release,
SHA/checksum, and CLI options; update pins in all affected jobs and retest the
workflow and positive/negative scan examples. Gate/suppression tests run in the
static job; locally, after installing Semgrep, run in its Python environment:
`python scripts/test-security-workflow.py` (requires `ruamel.yaml`, installed with
Semgrep, and `jq`).
Security databases update during each scan. Semgrep pip dependencies, runner images,
Go patch versions, and Docker base images are not fully pinned; current data/OS
packages can produce new findings without a code change.

No scanner finds every security issue. Semgrep OSS provides limited static analysis
and language rules here, not all framework rules. Trivy detects only supported
packages/file formats and secret patterns; it does not scan the complete Git history
or arbitrary Compose/runtime configuration. The scanners have built-in file
filters/allowlists, for example for certain test/sample files; therefore, a green
secret scan does not guarantee that every file is free of secrets.
govulncheck covers known Go advisories; it does not automatically cover every
vulnerability in the SQLite driver's C code. The image scan adds OS packages, but
does not replace runtime tests, manual reviews, access controls, or threat analysis.
False positives and existing findings are not automatically suppressed.

Changes to the workflow itself can weaken checks; review such PRs especially
carefully. A required check sourced from GitHub Actions does not guarantee that
workflow contents remain unchanged. Depending on GitHub settings, fork PRs first
require manual approval to run; the required check is missing until a successful
run. Merge Queue is not configured (it would also require a `merge_group` trigger).
