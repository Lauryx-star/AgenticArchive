# Sicherheitsprüfungen und Merge-Schutz

**Dieser PR aktiviert keine Repository-Regeln.** Ein roter Workflow allein
verhindert keinen Merge. Lauryx-star muss diesen PR mergen und anschließend die
unten beschriebene Regel aktivieren, damit normale künftige Merges blockiert werden.
Es werden weder Einstellungen automatisch geändert noch PATs benötigt.

## Prüfungen

`.github/workflows/security.yml` läuft für **alle Pull Requests**, auch aus Forks,
bei Erstellung, Wiederöffnung und jedem neuen Commit, bei Push auf `main`,
manuell über **Actions → Security → Run workflow** und montags um 06:23 UTC.
Keine Pfadfilter; neuere PR-Läufe brechen überholte Läufe ab.

| Job | Abdeckung und Fehlerverhalten |
| --- | --- |
| Go vulnerabilities | govulncheck v1.8.0; Go aus `go.mod`, CGO aktiviert. `-scan=module` blockiert bekannte Schwachstellen in Abhängigkeiten auch ohne nachgewiesene Erreichbarkeit; `-scan=package -tags=sqlite_fts5` prüft importierte Pakete der FTS5-Konfiguration. Anders als der normale Symbolmodus werden nicht nur aufrufbare verwundbare Funktionen bewertet. |
| Static security analysis | Semgrep 1.179.0 mit den Sicherheitsverzeichnissen `go/lang/security`, `javascript/lang/security`, `python/lang/security` aus dem gepflegten Upstream `semgrep/semgrep-rules`, fest auf Commit `a84ff9cc2453ca91d581380de4b8b3f272f6f4be` gepinnt. `--error --strict` lässt Befunde und Scan-/Konfigurationsfehler scheitern; `nosemgrep`-Kommentare unterdrücken keine Befunde. |
| Repository vulnerabilities, secrets and configuration | Trivy 0.75.0 prüft Abhängigkeiten (u. a. `go.sum`), Secrets und unterstützte Fehlkonfigurationen (u. a. Dockerfile). Secret-Werte werden in Trivys Tabellenbericht maskiert. |
| Container vulnerabilities | Lokaler Docker-Build einschließlich bestehender Go-Tests; Trivy prüft das fertige Image einschließlich Debian, Poppler, Tesseract und Go-Binary. Kein Image-Push, kein Registry-Login. |
| **Security gate** | Läuft mit `always()` und ist nur erfolgreich, wenn **alle vier** Scan-Jobs erfolgreich waren. Fehler, Abbruch, unerwartetes Überspringen und Scanner-/Download-/Buildfehler führen nicht zu einem grünen Gate. Ein vollständig abgebrochener Lauf liefert ebenfalls keinen erfolgreichen Pflichtcheck. |

Alle Semgrep-Befundstufen und alle von Trivy unterstützten Schweregrade
**UNKNOWN, LOW, MEDIUM, HIGH, CRITICAL** blockieren. Auch Schwachstellen ohne
verfügbaren Fix bleiben Befunde; keine Baseline, kein `continue-on-error`,
kein `--ignore-unfixed`. Trivy verwendet keine Repository-Ignoredatei,
Repository-Konfiguration oder eigene Secret-Konfiguration. Ein vorgeschalteter
Check lehnt Inline-Unterdrückungen für Trivy/tfsec ab, da diese sonst Befunde vor
der Auswertung entfernen können. Auch Trivys protokollierte ERROR/FATAL-Meldungen
(z. B. Rückfall auf eingebettete Checks nach Downloadfehlern) lassen den
Repository-Job scheitern. govulncheck hat keinen Schweregradfilter.

Die Jobs verwenden `pull_request`, **nicht** `pull_request_target`, nur lesende
Repository-Berechtigungen und Checkouts ohne gespeicherte Credentials. Es gibt
keine Repository-Secrets oder privilegierten Owner-Tokens. Unvertrauenswürdiger
PR-Code wird nur auf kurzlebigen GitHub-Runnern ausgeführt, nie im Owner-Kontext.

## Rote Checks untersuchen

Im PR **Checks → Security → fehlgeschlagener Job → Scan-Schritt** öffnen.
Die Logs zeigen Regel-/Vulnerability-ID, Datei/Paket, Schweregrad und gegebenenfalls
eine reparierte Version. Das Gate protokolliert nur die Job-Ergebnisse.
Es werden keine Reports/Artefakte mit Secret-Werten hochgeladen. Gefundene Secrets
umgehend widerrufen/rotieren, dann entfernen; nicht in Kommentare kopieren.
Semgrep kann Quellcodeausschnitte ausgeben, und Docker protokolliert den Build:
keine Credentials in Quellcode oder Build-Kommandos ablegen.
Scannerfehler (z. B. Netzwerk, Datenbank, Parser oder Timeout) beheben und erneut
ausführen; ein fehlender Scan ist kein Sicherheitsnachweis.

## Main schützen: manuelle Einrichtung durch den Owner

1. Diesen PR mergen; falls nötig einen ersten Workflow-Lauf abwarten oder
   auf `main` manuell starten. Erst danach ist der Check gegebenenfalls auswählbar.
2. [Settings → Rules → Rulesets](https://github.com/Lauryx-star/AgenticArchive/settings/rules)
   öffnen. Bestehende Regeln prüfen; bei bereits passender Regel diese bearbeiten,
   statt ein widersprüchliches Duplikat anzulegen. Sonst **New ruleset →
   New branch ruleset**, Name beispielsweise `Main security`.
3. **Enforcement status: Active** auswählen (nicht Disabled/Evaluate).
4. Unter **Target branches → Add a target → Include default branch** wählen;
   aktuell ist das `main`. Keine Ausnahme für `main` hinzufügen.
5. **Require a pull request before merging** aktivieren.
6. **Require status checks to pass → Add checks**: exakt **`Security gate`**
   hinzufügen, als Quelle **GitHub Actions** auswählen. **Require branches to be
   up to date before merging** aktivieren. Keine weiteren Scan-Einzeljobs als
   Pflichtchecks nötig.
7. **Bypass list → Add bypass**: Rolle **Repository admin** hinzufügen.
   Rechts neben **Always allow** den Modus **For pull requests only** auswählen.
   Nicht „Always allow“ belassen: der Bypass soll keinen direkten Push erlauben.
8. Einstellungen prüfen und **Create / Save changes** bestätigen. Danach an
   einem Test-PR kontrollieren, dass ein roter `Security gate` den normalen Merge
   blockiert und ein aktueller grüner Check den normalen Merge erlaubt.

Lauryx-star besitzt aktuell die Admin-Rolle und kann dadurch bei einem roten
Check im PR-Merge-Dialog bewusst die angebotene Option zum Umgehen der Regeln
wählen und den Merge bestätigen. Den Grund und akzeptierte Risiken im PR
dokumentieren; der Scan bleibt rot. **Dieser Bypass gilt auch für künftige
Repository-Administratoren, nicht exklusiv für den Benutzernamen Lauryx-star.**
Andere überlappende Rulesets/Branch-Protection-Regeln können einen Merge weiterhin
verhindern und müssen separat berücksichtigt werden.

Offizielle Anleitung:
[Ruleset erstellen und PR-only-Bypass konfigurieren](https://docs.github.com/en/repositories/configuring-branches-and-merges-in-your-repository/managing-rulesets/creating-rulesets-for-a-repository),
[verfügbare Regeln und Pflichtchecks](https://docs.github.com/en/repositories/configuring-branches-and-merges-in-your-repository/managing-rulesets/available-rules-for-rulesets).

## Wartung und Grenzen

Action-Referenzen sind vollständige verifizierte Commit-SHAs mit Release-Kommentar.
Scanner und Semgrep-Regeln sind fest versioniert; Trivy-Downloads werden vor
Ausführung gegen die offizielle SHA-256-Prüfsumme geprüft. Bei Updates Release,
SHA/Prüfsumme und CLI-Optionen upstream prüfen, Pins in allen betroffenen Jobs
anpassen und Workflow sowie positive/negative Scan-Beispiele erneut testen.
Die Gate-/Unterdrückungstests laufen im statischen Job; lokal nach der
Semgrep-Installation mit dessen Python-Umgebung:
`python scripts/test-security-workflow.py` (benötigt das von Semgrep mitinstallierte
`ruamel.yaml` und `jq`).
Sicherheitsdatenbanken aktualisieren sich beim Scan. Pip-Abhängigkeiten von
Semgrep, Runner-Images, Go-Patchversionen und Docker-Basisimages sind nicht
vollständig eingefroren; aktuelle Daten/OS-Pakete können ohne Codeänderung neue
Befunde erzeugen.

Kein Scanner findet alle Sicherheitsprobleme. Semgrep OSS bietet begrenzte
statische Analyse und hier Sprachregeln, nicht sämtliche Framework-Regeln.
Trivy erkennt nur unterstützte Pakete/Dateiformate und Secret-Muster, keinen
vollständigen Git-Verlauf und keine beliebige Compose- oder Laufzeitkonfiguration.
Die Scanner haben eingebaute Dateifilter/Allowlisten, etwa für bestimmte
Test-/Beispieldateien; ein grüner Secret-Scan garantiert deshalb keine
Secret-Freiheit sämtlicher Dateien.
govulncheck deckt bekannte Go-Advisories ab, nicht automatisch alle Schwachstellen
im C-Code des SQLite-Treibers. Das Image ergänzt OS-Pakete, ersetzt aber weder
Laufzeittests noch manuelle Reviews, Zugriffsschutz oder eine Threat-Analyse.
Fehlalarme und bestehende Befunde werden nicht automatisch ausgeblendet.

Änderungen am Workflow selbst können Prüfungen schwächen; solche PRs besonders
sorgfältig reviewen. Ein Pflichtcheck mit GitHub-Actions-Quelle ist keine Garantie
für unveränderte Workflow-Inhalte. Fork-PRs benötigen je nach GitHub-Einstellung
zunächst eine manuelle Freigabe zum Ausführen; bis zum erfolgreichen Lauf fehlt
der erforderliche Check. Merge Queue ist nicht konfiguriert (dafür wäre zusätzlich
ein `merge_group`-Trigger nötig).
