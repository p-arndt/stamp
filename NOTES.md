# stamp — Absprache

Ergebnis eines `/interview deep` am 2026-07-29. Bis diese Datei geändert wird, ist
das hier der verbindliche Scope.

## Agreement

**Goal:** `stamp release minor` erledigt einen Release lokal und vollständig —
Version berechnen, Versionsstellen schreiben, git-Preflight, Commit, annotated Tag,
Push. Der Tag-Push löst die GitHub Action aus; die Pipeline bestimmt die Version
nie selbst. stamp ist der Release-Controller, die Pipeline nur der Worker.

**For:** p-arndt, über alle eigenen Repos hinweg — ersetzt langfristig das je Repo
duplizierte `scripts/release.mjs` + `scripts/set-version.mjs`.

### In scope

- `stamp release <patch|minor|major|x.y.z|x.y.z-beta.1>` mit `--dry-run`,
  `--no-push`, `--yes`
- `stamp set <patch|minor|major|x.y.z>` — schreibt nur die Versionsstellen, kein git
- `stamp current` — aktuelle Version auf stdout, für justfiles und Skripte
- `stamp verify --tag v0.5.0` — Tag gegen Repo-Version, exit 1 bei Abweichung;
  gedacht als erster Step der GitHub Action
- Version-Sources: `file` (VERSION) und `json` (package.json, konfigurierbares Feld)
- `mirrors`: mehrere Versionsstellen werden gemeinsam gesetzt
- Optionale `.stamp.yml`; ohne Config läuft alles über Auto-Detect
- Eigener Release-Workflow: Cross-Compile, Archive, Checksums, Changelog,
  GitHub Release — Binaries zum Download
- Nachträglich ergänzt (2026-07-30): `stamp self-update` / `stamp check-update`
  plus passiver Update-Hinweis auf stderr, portiert aus hop. Verteilung läuft
  über GitHub Releases, also ist der Updater die Verteilung — ohne ihn hängt
  jede Installation auf ihrer Version fest. `STAMP_NO_UPDATE_CHECK=1` schaltet
  den Hinweis ab; Dev-Builds prüfen nie.
- Ersetzt (2026-08-15): `internal/update` ist raus, der Updater kommt jetzt aus
  `github.com/p-arndt/selfupdate` v0.1.0 — dieselbe Sache, aber einmal gepflegt
  statt je Repo kopiert. Die Bibliothek trifft stamps Release-Layout
  vollständig mit ihren Defaults (`stamp_<version>_<goos>_<goarch>.tar.gz|.zip`,
  `stamp_<version>_checksums.txt`, `STAMP_NO_UPDATE_CHECK`,
  `<UserConfigDir>/stamp/update-check.json`); gesetzt wird nur `UpdateCmd`, weil
  das Kommando hier `stamp self-update` heißt und nicht `stamp update`.
  Ausgegeben wird weiter über `internal/ui` — deshalb `SelfUpdate` statt des
  fertigen `Run()`. Signaturprüfung (`verify.Ed25519`) bleibt vorerst aus: der
  Release-Workflow signiert nichts, und ein Key will verwaltet werden.
- Nachträglich ergänzt (2026-08-15): `stamp prerelease <patch|minor|major>` als
  eigenes Kommando, mit `--type <id>` und Default aus `release.prerelease`
  (Fallback `beta`). Der Bump-Keyword nennt die kleinste Eskalation der
  kommenden stabilen Version; ist die Ziel-Basis noch unveröffentlicht, läuft
  nur der Zähler weiter (`1.3.0-beta.1` → `1.3.0-beta.2`). Innerhalb einer
  laufenden Serie trägt das Keyword deshalb keine Information — dort ist es
  optional, `stamp prerelease` allein schneidet den nächsten Kandidaten. Auf
  einer stabilen Version bleibt es Pflicht, weil sonst nichts sagt, ob die
  Serie auf `1.2.4` oder `2.0.0` zielt. Dazu `stamp release
  final`, das eine Prerelease zu ihrem Release promotet. Es geht auch ohne —
  `release patch` auf `1.3.0-rc.1` ergibt `1.3.0`, weil Masterminds' `IncPatch`
  bei vorhandener Prerelease nur diese verwirft — aber das muss man wissen, und
  der naheliegende Griff zu `release minor` ergibt `1.4.0` und überspringt
  `1.3.0` still. `final` benennt die Absicht und bricht ab, wenn es nichts zu
  promoten gibt. Der Identifier wird ohne `--type` aus der laufenden Serie
  übernommen, sonst aus der Config — der Config-Default mitten in einer
  rc-Serie hieße Rückschritt auf `beta` und damit Preflight-Abbruch. Preflight bleibt unverändert: Prereleases laufen durch
  dieselben Checks, der Plan zeigt nur eine zusätzliche Info-Zeile.

### Out of scope

- **Checks / Tests ausführen.** Kein `checks:`-Block, kein `--check`-Flag.
  stamp prüft ausschließlich git-Zustand und Version. Tests sind Sache der CI.
- `--no-commit` — macht Tag/Version/git-Zustand unnötig kompliziert
- `stamp release retry` — später, nicht in v1
- Prerelease-spezifische Lockerungen (z. B. Release von einem Feature-Branch
  ohne `--branch`) — bewusst nicht, ein Prerelease ist ein normaler Release
- Workspace-Fan-Out (pnpm-workspaces automatisch scannen) und Glob-Mirrors;
  Monorepos listen ihre Pfade explizit als mirrors auf
- Cargo.toml / Rust (myterm bleibt außen vor)
- Workflow-Generator (`stamp init --ci`)
- `go install`-Verteilung, Homebrew-Tap
- **Migration bestehender Repos.** hop, shenv und compose-check-updates behalten
  ihr `release.mjs`, bis stamp sich bewährt hat.

### Constraints

- Go, wie die anderen Repos: bare module `stamp`, Go 1.26, Layout analog hop
  (`main.go`, `internal/…`, `justfile`, `.github/workflows/{ci,release}.yml`,
  `cliff.toml`, `VERSION`)
- Tag-Template muss echt konfigurierbar sein: hop/shenv taggen `v0.5.0`,
  **uprox taggt ohne Prefix** (`0.9.0`). Kein hardcodiertes `v`.
- `package.json` darf beim Schreiben nicht umformatiert werden — uprox ist
  tab-indentiert. Also chirurgischer Ersatz des Version-Feldes, kein
  Marshal-Roundtrip.
- Branch und Tag gehen in **einem** Push-Aufruf raus: `git push origin <branch> <tag>`,
  nicht zwei unabhängige Pushes und nicht `--follow-tags`.
- Existierende Node-Repos haben heterogene, teils teure `test`-Skripte (uprox
  `test` installiert Playwright-Browser) — mit „keine Checks" ist das erledigt.

### Approach

Reihenfolge: **erst prüfen, dann schreiben.** git-Preflight läuft auf dem
unveränderten Baum — working tree clean, Branch stimmt, Branch ist up-to-date mit
Remote, Tag existiert noch nicht, Zielversion ist gültiges semver und höher als die
aktuelle. Erst danach wird geschrieben, committed, getaggt, gepusht. Damit ist der
häufigste Abbruchgrund erledigt, bevor überhaupt eine Datei angefasst wird.

Fehler nach dem Schreiben werden **stufenweise** bis zum letzten sicheren Punkt
zurückgerollt:

| Fehlgeschlagen | Reaktion |
|---|---|
| Schreiben | Dateien aus dem Speicher restaurieren |
| Commit | Dateien restaurieren, unstage |
| Tag | Commit stehen lassen (er ist gültig), Tag-Fehler melden |
| Push | nichts zurückrollen, exakten Befehl zum Fortsetzen ausgeben |

Kein `git reset --hard` — zu gefährlich, wenn ein Push teilweise durchging.

Verworfen: „Version schreiben, dann Tests laufen lassen, bei Fehler zurückrollen"
(aus dem ursprünglichen Konzept) — fällt mit der Entscheidung gegen Checks weg.
Verworfen: voller Rollback inklusive Push-Fehler, siehe oben.

### Assumptions

Selbst entschieden statt gefragt — hier ist die Gelegenheit zu widersprechen:

- CLI mit stdlib `flag`, kein cobra. git über `os/exec`, kein go-git. YAML über
  `gopkg.in/yaml.v3`.
- Auto-Detect-Reihenfolge im Repo-Root: `VERSION` → `package.json` → Fehler mit
  Hinweis auf `.stamp.yml`.
- Defaults ohne Config: Branch = `main`, Tag = `v{{version}}`,
  Commit = `release: {{tag}}`. Steht HEAD auf einem anderen Branch, bricht der
  Preflight ab (nicht nur Warnung) und nennt beide Auswege: `release.branch` in
  `.stamp.yml` oder `--branch <name>`. Nachträglich vom Nutzer entschieden.
- Bestätigungsprompt läuft immer; `--yes` überspringt ihn. Ohne TTY und ohne
  `--yes` bricht stamp ab, statt blind durchzulaufen.
- Ausgabe ist gestylter Text wie im Mockup — kein TUI.
- Sonderfall aus `release.mjs` bleibt: steht die Version schon auf dem Ziel, gibt es
  keinen Commit, stamp taggt HEAD.
- Trailing-Newline-Verhalten der VERSION-Datei wird erhalten, wie es vorgefunden wird.
- Erster Tag `v0.1.0` von Hand; danach released stamp sich mit sich selbst — das ist
  der Integrationstest.
- Repo landet unter `github.com/p-arndt/stamp`.

### Done when

- In einem Klon von hop setzt `stamp release minor --dry-run` korrekt `0.4.0 → 0.5.0`
  und verändert nichts.
- Ein echter `stamp release patch` in einem Wegwerf-Repo mit VERSION-Datei erzeugt
  Commit, annotated Tag und einen einzigen Push, der Branch und Tag überträgt.
- Dasselbe in einem Repo mit `package.json` als Source — Formatierung und Einrückung
  der Datei bleiben unverändert (`git diff` zeigt nur die Versionszeile).
- Ein Repo mit VERSION als Source und `package.json` als Mirror hat nach dem Release
  in beiden Dateien dieselbe Version, in einem Commit.
- `stamp verify --tag v0.5.0` gibt bei Übereinstimmung 0 zurück, bei Abweichung 1
  mit lesbarer Meldung.
- Preflight bricht nachweisbar ab bei: dirty tree, falschem Branch, Branch hinter
  Remote, existierendem Tag, Version ≤ aktuell.
- `--no-push` hinterlässt Commit und Tag lokal, pusht nichts.
- Ein simulierter Tag-Fehler hinterlässt den Commit intakt und sagt klar, was zu tun ist.
- stamp hat einen grünen CI- und Release-Workflow und mindestens einen Release, der
  von stamp selbst geschnitten wurde.

### Open risks

- `stamp verify` läuft im CI-Checkout am Tag. Es muss das Tag-Template rückwärts
  auflösen (`0.9.0` bei uprox, `v0.5.0` bei hop) — bei exotischen Templates ist die
  Umkehrung nicht eindeutig. Fällt erst auf, wenn ein Repo so eins benutzt.
- „Branch up-to-date mit Remote" braucht ein `git fetch`; das macht stamp
  netzabhängig und langsamer. Wenn das störend ist, kommt ein `--no-fetch` dazu.
- Ohne Checks kann ein roter Baum getaggt werden. Bewusste Entscheidung: die Action
  testet vor dem Publish, ein fehlgeschlagener Release-Run lässt den Tag aber stehen.
