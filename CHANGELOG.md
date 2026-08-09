# Changelog

All notable changes to **MailFerry – IMAP Migration & Sync** are documented
in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

- macOS Developer ID signing + notarisation pipeline
  (docs/RELEASING-MACOS.md) — binaries are not yet signed or notarised.
- QRESYNC/CONDSTORE delta sync, permanent quarantine purge,
  Prometheus-style metrics (v2.1.x and beyond).

## [2.1.1] - 2026-08-10

Bug-fix release. No changes to the migration engine's correctness,
resumability, statistics identity or on-disk schema.

### Fixed

- **`--no-skip-failed` did not do what it said.** It was documented as
  "do not skip Failed Message Registry entries on resume", but its only
  effect was suppressing one log line: failed messages are excluded
  *structurally* (their rows are `failed`, and the transfer selects
  `planned`), which the flag never touched. An operator could reasonably
  believe they had re-queued their failures when nothing had changed.
  No migration-correctness impact — nothing was lost or duplicated, and
  `mailferry retry-failed` always worked correctly.

### Added

- **`--retry-failed` runtime override.** `mailferry run CSV --retry-failed`
  admits Failed Message Registry entries to the work queue **for that run
  only**. It is purely a scheduling decision: the registry and the State
  Database are not rewritten up front, the rows keep their `failed` state,
  and normal transitions apply only as each message is actually retried —
  success flows through the ordinary path to RECOVERED, a repeat failure
  stays FAILED with an incremented count.

  This restores the intent of the broken flag while keeping a clean
  separation an operator can reason about:

  | | Meaning |
  |---|---|
  | `mailferry retry-failed` (command) | *Permanently* re-queue: a deliberate State Database write, resetting rows to `planned` and the registry to RETRY_PENDING so the messages rejoin every future run. **Unchanged.** |
  | `run CSV --retry-failed` (flag) | *This execution only.* Runtime flags should influence runtime behaviour, not rewrite persistent migration state. |

- `recovery.skip_known_failed_messages = false` in `mailferry.toml` is the
  persistent equivalent of the runtime flag, and now genuinely works.

### Changed

- `--no-skip-failed` is retained as a **deprecated alias** for
  `--retry-failed` through the v2.1.x series, and prints a notice. It will
  be removed thereafter.
- CLI help, `mailferry failed` footer, `mailferry retry-failed` output,
  the configuration template and the README now state the distinction
  between the permanent command and the one-run flag explicitly.

## [2.1.0] - 2026-08-03

Feature release: OAuth 2.0 / modern authentication with a first-class
`mailferry auth` lifecycle, IMAP MULTIAPPEND batching, evidence-based
server identification, and a substantially polished operator experience.
Migration correctness, the statistics identity, terminal sanitisation and
SIGHUP handling are all unchanged and still test-enforced.

### Added

- **OAuth 2.0 / modern IMAP authentication** (SASL XOAUTH2, and OAUTHBEARER
  for standards-compliant servers). First-class providers: **Microsoft 365
  / Exchange Online** and **Google Workspace / Gmail**; a **generic**
  provider covers other standards-compliant OAuth IMAP servers. The
  authentication layer separates SASL *mechanisms* from OAuth *providers*,
  and the migration engine consumes an authenticated connection without any
  provider-specific logic. Source and destination authenticate independently
  (password↔OAuth, or two different providers, in one run). Access tokens are
  refreshed automatically from a refresh token across a long migration.
  Configuration is backward-compatible: the canonical 10-column CSV is
  unchanged — an endpoint uses OAuth by putting `oauth:<profile>` in its
  password column, with `[oauth.<profile>]` profiles defined in
  mailferry.toml. **Secrets are never stored**: client secrets and refresh
  tokens are given as external references (`env:` / `file:` / `keychain:` /
  `cmd:`) resolved at run time and never written to the State Database,
  logs, reports or traces; the XOAUTH2/OAUTHBEARER SASL payload is redacted
  from `--trace`; a literal secret in configuration is rejected. MailFerry
  never silently falls back from OAuth to password LOGIN — failures are
  actionable (mechanism not advertised, invalid_grant, consent/scope missing,
  tenant policy, invalid client). See `docs/OAUTH.md`.
- **`mailferry auth` command family** — a first-class authentication
  lifecycle with the polish of a modern CLI sign-in tool. `auth add
  <provider> --name` opens the browser itself (127.0.0.1 loopback + PKCE,
  RFC 8252), **always** shows a copyable URL as well, reports
  waiting/success/cancellation/failure plainly, and finishes by naming the
  **account** that was authorised — read from the provider's OpenID Connect
  `id_token` (interactive requests additionally ask for the identity-only
  `openid`/`email` scopes, which grant no mailbox access; the claim is a
  label and never an authorisation decision). An operator never copies an
  access token by hand.
  Headless (`--no-browser`): Microsoft uses the device-code flow (RFC 8628);
  Google/generic use **manual redirect capture** — the same officially
  supported loopback + PKCE flow, with the authorisation code carried back by
  copy-and-paste from the failed `127.0.0.1` redirect, while the loopback
  listener runs concurrently so a forwarded port simply completes by itself.
  MailFerry never emulates a flow a provider does not offer.
  `auth add --user MAILBOX` names the account to authorise: it is sent as the
  standard OIDC `login_hint` so the provider pre-selects it, and the account
  that actually signed in is **verified afterwards** — on a mismatch nothing
  is saved. This closes a trap with real consequences: a delegated profile
  authenticates whoever signs in, so an operator who signs in as a tenant
  administrator to grant consent would otherwise walk away with a profile
  bound to the administrator's mailbox. Without `--user`, Microsoft is asked
  for the account picker instead, so a stale single-sign-on session cannot
  silently choose; `auth refresh` uses the profile's recorded account as the
  hint automatically, so re-authorising can never rebind a profile to a
  different mailbox.
  `auth list` / `show` / `test` / `refresh` / `remove` cover the rest of the
  lifecycle; none ever display a token or secret, and `remove` clearly
  distinguishes deleting the local profile from revoking the grant at the
  provider. `auth test` now mints a token **and** performs a real, read-only
  IMAP login against the provider's documented host (`--no-imap` to skip).
- **Bulk / MSP authentication models** — beyond delegated (per-user) OAuth,
  `kind = "application"` uses Microsoft 365 app-only client credentials
  (`IMAP.AccessAsApp`) so one tenant app authenticates **many** mailboxes,
  and `kind = "service_account"` uses a Google Workspace service account with
  **domain-wide delegation** (signed-JWT impersonation) so one account
  authenticates **many** mailboxes — the target mailbox is the SASL login in
  both. Delegated, application and service-account models are kept distinct in
  configuration, validation, the CLI and the docs. An administrative profile's
  token source is **shared across the whole run**: a Microsoft 365
  `application` profile mints its tenant-level token once for hundreds of
  mailboxes and every worker, instead of once per mailbox.
- **Coherent secure credential storage on every OS.** A storage backend is
  now chosen only when MailFerry has verified it can also *read the value
  back on that platform*: macOS login keychain and Linux Secret Service
  (`keychain:` references), **Windows DPAPI** (`CryptProtectData`,
  current-user scope, `dpapi:` references — real OS cryptography with an
  OS-managed key, since Windows has no plaintext-returning credential CLI),
  and a documented 0600-file last resort with guidance to use `env:` / `file:`
  on encrypted storage / `cmd:` with an external secret manager instead.
  `auth add` can prompt for an application client secret with **no echo** and
  store it, and can import a service-account key file into the store, so
  neither has to sit in a hand-managed location.
- **IMAP MULTIAPPEND (RFC 3502)** — several messages uploaded in **one**
  APPEND command when the destination advertises the capability. On by
  default (`migration.multiappend = "auto"`, `--multiappend auto|off`), with
  automatic batch sizing (`migration.multiappend_batch_size = "auto"`,
  `--multiappend-batch-size auto|N`) and an F9 Settings entry. `Auto` sizes
  each batch from the smallest of four documented limits: message count
  (scaled by configured concurrency), an aggregate byte budget, the source
  FETCH pipeline depth, and a solo threshold above which a large message is
  never batched. Measured on the built-in benchmark, 400 × 8 KB messages went
  from 400 APPEND commands to 50 — an 8× reduction in append commands for
  that corpus; no universal speed-up is claimed, since the benefit depends on
  latency, server and message sizes. **It is an optimisation and nothing
  more**: bodies still stream chunk-by-chunk (memory stays O(chunk), never
  O(batch)); a destination without the capability, `--baseline` and
  `--dry-run` transparently use the unchanged single-append path; the append
  window still bounds in-flight *messages*, so cancellation is as responsive
  as before; progress and statistics remain per message with
  `MsgsDone == Appended + Adopted + PriorDone + Planned` intact; and every
  message keeps its own durable state row, destination UID (from the RFC 4315
  APPENDUID uid-set), flags and INTERNALDATE. A batch is never a unit of
  durable accounting. Because RFC 3502 makes MULTIAPPEND atomic, a rejected
  batch stored nothing, so the messages are safely retried individually and
  batching is disabled for the mailbox after a second rejection; a `BAD`
  (the server could not parse a command it advertised) additionally drops the
  connection so reconciliation runs before anything is appended again.
  MULTIAPPEND deliberately does **not** appear in the primary Mode line —
  it belongs in Settings, logs and telemetry. See `docs/MULTIAPPEND.md`.
- **One authoritative version definition.** `internal/identity` now defines
  the release as `Core` (the semantic version) plus `Stage` (`dev`,
  `alpha1`, `beta2`, `RC1`, … or empty for a final release), and every
  version string in MailFerry derives from those two constants — the
  Dashboard banner, splash, `version`, Help/About, logs, diagnostics, the
  RFC 2971 ID MailFerry sends, generated configuration comments, benchmark
  output, release artefact names and the git tag. Promoting a release
  candidate to the final release is a one-line change (`Stage = ""`).
  `build.sh` now asks the binary via `mailferry version --bare` instead of
  parsing source, so the build system consumes the same definition. Two
  renderings exist for two audiences: `Version` is the SemVer form
  (`2.1.0-RC1`) for tags, filenames and anything a machine compares, and
  `Display` is the human form (`v2.1.0 RC1`) for banners. A test walks the
  repository and fails if any Go source outside `internal/identity`
  hard-codes the version, and another fails if `build.sh` stops deriving it.
- **Version is now v2.1.0 RC1** throughout.
- **Runtime freezes when the run ends, and the Dashboard says when.** The
  Dashboard's Runtime used to keep counting after a migration finished,
  reporting something that was no longer true. It now freezes at the real
  duration, and a `Completed` / `Interrupted` / `Finished` row appears with
  the completion date and time to the second and with the zone. That instant
  is the engine's `RunResult.FinishedAt` — the same value the reports, the
  results CSV and the Results screen already use, so there is exactly one
  completion time anywhere in MailFerry. A finished mailbox's detail freezes
  with the same discipline.
- **Durations and timestamps read like a tool, not a debugger.**
  `util.FmtDHMS` — still the single duration renderer — omits a zero day
  component (`01:32:05` rather than `0 day 01:32:05`) and says `2 days` when
  there are two. `util.FmtStamp` — still the single timestamp renderer — now
  always includes seconds and the zone (`03/08/2026 21:14:37 NZST`), because
  a completion time exists to be correlated with server logs and tickets.
  Territory still governs date order; the time is 24-hour everywhere.
- **Rebalanced Dashboard.** The left column no longer sits half empty: the
  run information is laid out in two evenly split, colon-free aligned
  columns, with the connection panel below it. The layout adapts —
  side-by-side panel on a wide terminal, a condensed line per side on a
  narrow one, and on a short terminal the panel yields its rows to the
  mailbox list (the primary content; the same detail is one keypress away in
  the F3 inspector). `tuishot` takes optional `cols`/`rows` arguments so a
  layout can be checked at real terminal sizes, and the layout tests drive
  the same renderer at six widths, asserting no line ever overflows.
- **Live mailbox inspector.** The F3 mailbox detail (Enter) used to freeze a
  snapshot taken at the moment it opened. It is now a live inspector: every
  figure re-renders from the current snapshot on the same ticker as the
  Dashboard — status, folders, messages, bytes, throughput, per-mailbox ETA,
  adopted, failed, reconnects, elapsed, current operation, MULTIAPPEND state
  and batch figures, both servers' identification, transport security and the
  destination's notable capabilities. It follows the mailbox by name, so
  re-sorting or filtering the list underneath it cannot silently swap which
  mailbox is being inspected, and a mailbox that leaves the run says so
  instead of showing somebody else's numbers. Esc or Enter returns to the
  list.
- **One way out.** Every interactive quit path — `q`, Ctrl+C, and the
  Results screen — now funnels through a single confirmation dialog, and
  **Esc can no longer end MailFerry from any screen**. Esc means "back":
  it clears a search, closes a dialog, returns from a detail or edit screen,
  and does nothing at all on the home screen. When a migration is still
  running the dialog says plainly that stopping is safe — completed work is
  already recorded in the State Database and re-running resumes from exactly
  where the run stops, with nothing lost and nothing copied twice. Ctrl+C
  still escalates for an operator in a hurry (confirm → graceful stop →
  force), so the emergency path is unchanged in length. SIGHUP deliberately
  does **not** route through the dialog: a terminal that has gone away cannot
  answer one, and stopping gracefully on hangup remains the v2.0.3 behaviour.
- **Source / Destination connection panel** on the Dashboard, filling space
  the left column was wasting. It describes the two **live connections** —
  `Host`, `Server`, `Auth`, `TLS` and `Speed` per side — and deliberately
  does not repeat the run's cumulative traffic, which the `Sent/Rcvd` row
  above already answers. `Speed` is that connection's current throughput,
  one figure per side: every IMAP connection both sends and receives by
  nature, so splitting it into TX/RX would add a column without adding an
  answer.
  Identification uses only evidence learned from the live connection, in
  descending confidence: the **OAuth provider** whose token this server
  accepted (nothing else can accept it), the server's RFC 2971 ID response,
  vendor-private capabilities that name their own vendor, the greeting
  banner, and the TLS certificate. **There is no hostname table and no
  bundled provider database to maintain**; the identifier is not even given
  the hostname, so guessing from a domain is structurally impossible rather
  than merely discouraged. A server MailFerry has never heard of still
  identifies itself, because an unrecognised self-report is used verbatim.
  When nothing identifies the server the panel says `Unknown` rather than
  guessing, and when sources disagree the strongest wins while the
  conflicting evidence goes to the per-mailbox log. No extra network
  operations: every signal is a by-product of the normal handshake.
- **Expanded server identification.** The evidence-based engine now
  recognises, from their own self-reports (IMAP greeting, RFC 2971 ID,
  vendor-private capabilities): Dovecot, Courier IMAP, Cyrus IMAP,
  Microsoft Exchange / Exchange Online, Zimbra, Axigen, SmarterMail,
  MDaemon, Kerio Connect, MailEnable, WildDuck and Stalwart. It can also
  identify SMTP servers from a 220 greeting banner — Postfix, Exim,
  Sendmail, Microsoft Exchange, Zimbra, MDaemon, Kerio Connect, MailEnable,
  SmarterMail, Stalwart and gromox (grommunio) — ahead of any SMTP support:
  MailFerry does not speak SMTP yet, the engine is simply ready for the
  greeting when it does. Bundled-component suites (mailcow, Mailu, iRedMail,
  docker-mailserver) are deliberately NOT identified: they speak through the
  Postfix and Dovecot they bundle, and the evidence names the software that
  actually spoke — identifying the suite would require the fingerprinting
  this design refuses to do. Still no hostname, provider or fingerprint
  database; the only maintained mapping translates known protocol evidence
  into clean display names, and no positive evidence still means `Unknown`.
  Every supported product is covered by a named test.
- **Adaptive Source/Destination panel for heterogeneous runs.** The panel
  previously described a single "lead" mailbox, which is untrue of an MSP
  batch migrating from several platforms at once. It now summarises each
  side across every CONNECTED mailbox: when one identified platform serves a
  side — even across several hosts — the detailed Host/Server/Auth/TLS/Speed
  form is kept (multiple hosts show as `N hosts`, mixed mechanisms as
  `OAuth (Microsoft) · Password`, and Speed becomes that side's summed
  current throughput); when different platforms participate in one run, the
  panel switches to `↓ Sources` / `↑ Destinations` with per-platform mailbox
  counts, aggregated by identified platform — never by hostname — largest
  first, with unidentified servers counted as `Unknown` and mailboxes that
  have not yet connected counted as nothing at all. The F3 inspector remains
  authoritative for any individual mailbox's exact live connection.
- **`Sent/Rcvd` replaces `Wire RX/TX`** on the Dashboard — cumulative bytes
  transferred by the whole migration, with the overall `Rate` immediately
  below it. The two now answer clearly different questions (total traffic
  versus throughput), and a test asserts neither is duplicated in the
  connection panel.
- **MULTIAPPEND visibility.** The Dashboard's run-information block now
  carries a MULTIAPPEND line (`Active (Auto) avg 7.8`, `Not supported`,
  `Disabled`, `Ready (Batch Size: 16)`, `Fallback (rejected)`), the F3
  mailbox detail adds the per-mailbox state plus the destination's notable
  advertised capabilities, F7 Performance reports messages/commands/average
  batch/round trips saved, headless `--no-tui` output carries the same line
  at detail level, and a destination that will be batched to gains an `MA`
  connection badge. Still deliberately absent from the primary Mode line —
  batching is an optimisation, not a run mode.
- **`mailferry auth test` is now a staged operator diagnostic** rather than a
  pass/fail. It reports the profile (provider, model, mailbox, tenant, when
  it was authorised), then each stage separately — obtaining the access
  token, connecting over TLS, authenticating SASL, and `LIST "" "*"` — so a
  failure says *where* the chain broke. It finishes with the server's full
  advertised capability list and a plain-language verdict on the ones that
  affect a migration (MULTIAPPEND, UIDPLUS, LITERAL+, MOVE, CONDSTORE,
  QRESYNC). A failed IMAP authentication prints the provider-specific check
  to make first — typically `Set-CASMailbox -ImapEnabled $true`. The token's
  length and remaining lifetime are shown; the token never is.
- **Non-secret OAuth profile provenance.** Profiles record `authorised_at`,
  shown by `auth list`, `auth show` and `auth test`, so an operator can see
  how old a credential is. `auth test` additionally reports the live token's
  expiry and flags a pre-provisioned token that cannot refresh. It never
  writes to configuration — informational commands stay zero-side-effect.
- **`docs/OAUTH-SETUP.md`** — a complete provider configuration guide for
  system administrators and MSPs, verified against the official Microsoft and
  Google documentation on 01/08/2026: prerequisites, Entra app registration,
  the exact scopes and permission names, redirect URI rules (including the
  `http://127.0.0.1` manifest workaround), admin consent, Exchange Online
  service principal registration and per-mailbox scoping, Google Cloud OAuth
  clients, service accounts and domain-wide delegation, headless flows,
  testing, common mistakes, troubleshooting and least-privilege guidance.
  Examples use only RFC 2606 / RFC 6761 reserved namespaces.
- **Accurate run-mode presentation** — one authoritative representation of a
  run's active characteristics (`config.RunMode`) drives every surface: the
  startup banner, Dashboard, Results, headless summary, session log
  (machine-readable `modes=`) and — from the **persisted** run record — the
  attach monitor. A dry run is never shown as a plain migration; Date Range,
  Portable, Ephemeral, Backup (flag sync) and Baseline are surfaced when
  active, with ordinary tuning flags deliberately excluded. Rendered as
  `Migration & Sync · Dry Run · Date Range` in the TUI and comma-separated in
  plain text, from the same underlying data.
- **Interactive F9 Settings**: the Settings view is now a real editor —
  browse categorised options (Migration, Network & Retry, Recovery, Logging,
  Interface, State Database & Cluster), edit them (toggle booleans, cycle
  enums, step or type numerics), see unsaved-change and CLI-override
  indicators, then Save to write `mailferry.toml` atomically with comments
  preserved, or Esc to discard. Validation happens before any write, so an
  invalid value can never corrupt the file; a CLI-overridden value is not
  silently persisted; display-cadence changes apply live, others on the next
  run. Read-only runtime/cluster facts stay clearly separated. Opening,
  viewing or cancelling writes nothing (the zero-side-effect contract holds).
- **Expanded mailferry.toml** via a single typed option catalogue shared by
  file loading, `mailferry config` and F9 (no drift): new `[network]`
  section (`timeout_seconds`, `compress`) plus `migration.order` and
  `migration.skip_completed`.
- **Completion date/time on the Results screen** and the headless summary,
  captured once when the run reaches its terminal state (never re-stamped),
  shown in the operator's regional format where reliably detectable with an
  unambiguous ISO 8601 fallback, and contextually labelled
  (Completed / Interrupted / Finished).

### Changed

- **Redesigned F6 Errors / Failed Messages** view: a master/detail screen in
  the Results visual language that clearly separates run-level Errors &
  Warnings from the Failed Message Registry, decodes RFC 2047 subjects,
  aligns columns responsively, shows full detail (subject, reason, retry
  status) for the selection, and always states the overall status
  truthfully — it never claims "all clear" while failures are outstanding.
- **Unified interactive exit contract**: every interactive TUI exit path
  (complete, warnings, nothing-new, dry-run, interrupted, failed; q/Esc/
  Ctrl+C/double-Ctrl+C/SIGHUP) now lands on the Results screen and leaves the
  same concise shell acknowledgement — no second report dump, consistent
  across outcomes. An interrupted run gets the Results screen too. Headless
  output is unchanged (full structured summary).

### Fixed

- IMAP connect address now uses `net.JoinHostPort`, so IPv6 literal hosts
  dial correctly (`[::1]:993`); hostnames and IPv4 are unaffected.
- **Cancelling a run no longer abandons appends already on the wire.** A
  stopped transfer now settles the append commands it has already sent before
  unwinding, so each message's outcome is recorded instead of left ambiguous.
  Previously those messages stayed `inflight` while the destination was still
  committing them, and a resume could scan, fail to see them yet, and copy
  them a second time. The window is bounded by the append window, so
  cancellation remains prompt. Found by a new cancellation stress test
  (`TestMultiAppendCancelResumeNeverDuplicates`), which cancels at six
  different points, batched and unbatched, and asserts an exact-once result
  after resume.
- **A starting migration could fail if something was watching it.** A
  brand-new State Database is created in rollback-journal mode, and the
  switch to WAL needs a moment with no other connection attached. SQLite does
  **not** apply `busy_timeout` to `PRAGMA journal_mode`, so a read-only
  observer opening the file at that instant — `mailferry attach` polling for
  a run that is just starting is exactly this — could abort the migration
  with `database is locked` before it began. Schema initialisation now
  retries briefly on lock contention. The window was milliseconds and the
  observer never holds the lock, so the race is closed completely.
- `keychain:` secret references are now resolvable on Linux (Secret Service
  via `secret-tool`). Previously `auth add` could store a credential there and
  write a reference that only macOS could read back.

## [2.0.3] - 2026-07-19

Patch release: the polished MailFerry Results experience — an interactive
migration now finishes on a native Results screen instead of dumping a
raw report — plus statistics-correctness refinements. Migration
semantics are untouched; the v2.0.2 terminal-state fix (verified on the
affected macOS arm64 system) is fully preserved.

### Added

- **Results TUI**: interactive runs land on a polished final screen in
  the dashboard's visual language, with distinct verdicts — MIGRATION
  COMPLETE, MIGRATION COMPLETED WITH WARNINGS, MIGRATION COMPLETE —
  NOTHING NEW TO COPY, and DRY RUN COMPLETE — plus Run and Messages
  panels, a problems-first per-mailbox results table, and a concise
  "Needs attention" panel (outstanding count, affected mailboxes and
  folders, F6 / `mailferry retry-failed` / report path). Individual
  failed-message entries deliberately stay in the detail surfaces: F6
  (which now doubles as the Failed Messages browser after completion),
  `mailferry failed [--json/--csv]`, failed_messages.csv and the logs.
  Navigation: Enter → Mailboxes, F1–F10 views, S → Reports & files
  popup, Q/Esc → exit. After quitting, the shell gets a short
  confirmation — never a duplicate report. Headless/non-TTY runs keep
  the structured text report (never a forced TUI).
- **Statistics completeness**: two new authoritative counters close the
  accounting identity `synced = copied + adopted + prior-confirmed +
  dry-run-planned`, now enforced by invariant tests against the real
  engine: `PriorDone` (messages confirmed on the destination by earlier
  runs) and `Planned` (dry-run would-be copies). Both appear
  contextually in the Results view, the headless summary
  ("confirmed (prior)", "would copy (plan)") and results.csv
  (msgs_prior, msgs_planned columns).
- **RFC 2047 subject decoding** wherever failed-message subjects are
  shown interactively (`mailferry failed`, the headless Failed Messages
  section, the F6 registry browser), with clean ellipsis truncation —
  an encoded word is never cut in half at display time; undecodable
  values fall back verbatim.
- Restored v1.x informational parity in the headless summary: Run ID,
  stale and on-other-workers counts, aggregate failed-message line,
  average/slowest/fastest mailbox timings, retries, stall statistics,
  per-warned-mailbox breakdown, stale/cancelled listings and the
  ephemeral State-Database note.

### Fixed

- **SIGHUP no longer kills MailFerry in raw mode.** A terminal hangup
  mid-TUI (SSH drop, closed terminal window) terminated every previous
  release with the terminal still in raw/alternate-screen state — the
  classic origin of a "stair-step" session (self-healed since v2.0.2 on
  the NEXT run, but the source remained). SIGHUP is now a graceful
  stop: the engine aborts cleanly (per-message state committed), the
  TUI unwinds, the terminal is restored, and the process exits with
  the interrupted status. Headless runs and `mailferry attach` treat
  SIGHUP the same way. Verified by a PTY regression test that delivers
  SIGHUP mid-transfer and asserts a clean terminal afterwards.
- **Percentages can no longer overstate completion.** The one
  authoritative renderer (used by the dashboard, Results view, headless
  summary and attach) shows 100% only when done truly equals total:
  26,081 of 26,089 now renders 99.9%, never 100%; symmetrically, real
  progress never rounds down to 0%. The same rule now applies to the
  completed-with-warnings history detail.

### Changed

- docs/img screenshots regenerated from the current v2.0.3 interface
  (synthetic RFC 2606 fixture data only), including the new Results
  screen; docs/img/tui-dashboard.png no longer shows RC-era branding.

## [2.0.2] - 2026-07-19

Patch release: terminal output now renders correctly even when the
terminal arrives broken — the actual root cause of the reported macOS
"stair-step" output. The defect was reproduced on real macOS arm64
with v2.0.0/v2.0.1 and this fix was verified on that same environment
before release.

### Fixed

- **Stair-step output (root cause)**: MailFerry emits ordinary "\n"
  line output and relies on the terminal’s output post-processing
  (OPOST/ONLCR) for carriage returns. A terminal left in a raw-ish
  state by a previously crashed full-screen program — OPOST or ONLCR
  disabled — renders every subsequent run as a stair-step from the very
  first banner line, and the v2.0.1 capture/restore then PRESERVED that
  inherited damage instead of healing it (it restored the poisoned
  entry state verbatim, so the shell never recovered). MailFerry now
  sanitises the cooked-mode flags BEFORE its first byte of output: the
  `stty sane` newline subset (+OPOST +ONLCR +ICRNL +ICANON +ECHO +ISIG;
  −OCRNL −ONOCR −ONLRET −INLCR −IGNCR) is repaired only when actually
  broken and a one-line notice reports the repair. Every terminal
  capture thereafter snapshots the SANE state, so no exit path can hand
  a broken terminal back to the shell. Conservative by design:
  already-sane terminals get no write at all, pipes/non-TTY output are
  never touched, and TUI, headless, SSH/tmux/screen behaviour is
  unchanged. Real-world verification: entry state `-opost` was repaired
  (`+OPOST +ICRNL +ISIG`), every section rendered from column 1, and
  the shell returned completely normal (`opost onlcr`).

### Added

- **`mailferry term-diag`** — supported terminal self-test: prints test
  lines around a minimal alternate-screen stage; zero IMAP, network,
  database or credential activity.
- **`MAILFERRY_TERM_DIAG=FILE`** — records termios flags (OPOST ONLCR
  OCRNL ONOCR ONLRET ICRNL INLCR IGNCR ICANON ECHO ISIG) at every
  application lifecycle stage (entry, post-sanitise, banner, pre/post
  TUI, report, exit) for field diagnosis. Flags only — never mailbox
  data, addresses or credentials.
- **PTY-level regression tests** that assert cursor/newline semantics
  at the moment of every write: every rendered line must begin at
  column 1 on clean AND pre-poisoned terminals, and a poisoned terminal
  must end HEALED. Termios before/after equality is deliberately
  asserted nowhere — v2.0.1 proved equality can pass while rendering
  is broken.

## [2.0.1] - 2026-07-19

Patch release: terminal-state restoration guarantee.

### Fixed

- **Terminal restoration is now a guaranteed invariant, not an
  expectation.** The exact pre-TUI terminal state is captured before
  Bubble Tea takes over (both the migration TUI and `mailferry attach`)
  and restored verbatim on every return path — including TUI startup
  failures and the bounded hard-stop timeout — followed by idempotent
  defensive resets (leave alternate screen, show cursor, disable mouse
  reporting, reset attributes). Verified by a PTY termios matrix across
  nine exit paths (normal/instant completion, graceful Ctrl+C, double
  Ctrl+C, SIGTERM, SIGHUP, --dry-run, attach quit/Ctrl+C): no path
  leaves the terminal without newline translation (the "stair-step"
  artefact), raw input mode, or hidden echo. The v2.0.0-rc.3 hard-stop
  correction is preserved unchanged; released rc.2/rc.3/v2.0.0 binaries
  were bracketed with the same matrix and showed no reproducible leak
  on these paths — this release closes the class permanently, including
  platform edge cases beyond the test environment.

## [2.0.0] - 2026-07-19

**Stable release** of the complete native Go rewrite — the culmination
of rc.2/rc.3 plus the final feature set, published only after the full
internal release gate passed.

### Added

- **Whole-file CSV validation**: every detectable error reported in a
  single pass (headers, ports 1–65535, security none/ssl/starttls,
  empty values, column counts, quoting), passwords never echoed;
  migration never starts on invalid input.
- **`--dry-run`**: strict read-only runs — mutating IMAP verbs (APPEND,
  STORE, CREATE, …) are blocked at one choke point inside the client
  before any socket write (proven by fake-server mutation counters:
  zero), state kept in memory only, DRY RUN plan summary printed.
- **ISO 8601 date-range migration**: `--from`/`--to`, inclusive bounds,
  explicit offsets and `Z` honoured, local timezone otherwise; IMAP
  INTERNALDATE is the authoritative timestamp; the resolved window is
  persisted in the State Database and a stored window wins on resume
  (deterministic); no `present`/`now` keywords — an omitted `--to`
  simply means no upper bound.
- **`mailferry dedup`**: explicit, destination-only deduplication.
  Analysis (default and `--dry-run`) mutates nothing and writes a
  dedup_report.csv; `--execute` relocates duplicates reversibly (UID
  MOVE to MailFerry-Quarantine when the server offers MOVE, otherwise
  COPY + `\Deleted` flag — **EXPUNGE is never issued**; permanent
  deletion is deliberately not implemented). Duplicates require
  matching normalised Message-ID AND size AND header fingerprint;
  uncertain matches are always retained; keeper is the lowest UID;
  interruption-safe and resumable via the dedup_state table; actively
  leased mailboxes are skipped with a clear notice.
- **`mailferry attach`**: read-only live monitor for running headless
  migrations — 1 Hz read-only State-Database snapshots plus a session
  log tail; takes no leases, writes nothing; attach/detach/re-attach
  freely without ever disturbing workers; non-TTY invocations print a
  one-shot status snapshot.
- **`--portable`**: self-contained mode rooted at the executable's
  directory (mailferry.toml, mailferry.db, logs/, cache/); precedence
  CLI flags → portable → TOML → native; informational commands remain
  zero-side-effect; read-only roots produce clear actionable errors.

### Changed

- Version identity to stable v2.0.0 across the application,
  documentation and release artefacts.

## [2.0.0-rc.3] - 2026-07-19

Final planned Release Candidate before stable v2.0.0. Multi-process
coordination hardening, honest coordination reporting, terminal-safety
fixes and the canonical src/dst CSV format. Published as a GitHub
**pre-release**.

### Fixed

- **Multi-instance visibility**: a MailFerry process that finds its
  mailboxes actively owned by other healthy workers now says so plainly
  on stdout — `Mailbox already active: <mailbox> — owned by worker
  <id> (heartbeat …, run <run-id>)` — and stands by as a hot failover,
  taking the mailbox over automatically when it is released or its
  owner dies. Headless runs stream all notable coordination events
  (takeovers, stale-lock auto-resets, completed-by-another-worker).
  Nothing exits silently: runs that copy nothing explain why
  ("Nothing new to copy — every message was already on the Destination
  Server"), and the startup banner prints the unique
  `Run <run-id> · worker <worker-id>` identity.
- **Terminal restoration**: a second Ctrl+C in the TUI no longer
  hard-exits underneath Bubble Tea — the engine is aborted, the TUI
  quits and restores the terminal, then the process ends (bounded 2 s
  wait). An engine panic is recovered and reported instead of killing
  the process mid-alt-screen. No exit path leaves the terminal in raw
  or alternate-screen state.
- **SQLite multi-process hardening**: WAL / busy-timeout / synchronous
  pragmas now travel in the connection string so every future
  connection gets them, and Run IDs carry a random suffix so processes
  launched in the same second can never collide.

### Changed

- **Canonical CSV format is `src`/`dst`**:
  `srchost,srcport,srcsecurity,srcuser,srcpassword,dsthost,dstport,dstsecurity,dstuser,dstpassword`.
  `mailferry init`, the docs and all fixtures use it; the obsolete v1
  `old*`/`new*` header is rejected with an actionable rename hint —
  columns are never silently misinterpreted.

### Added

- **Real multi-process concurrency tests**: the suite builds the actual
  binary and launches genuine OS processes against one shared
  `mailferry.db`, verifying concurrent independent runs, mixed-CSV
  behaviour (available mailboxes proceed; held ones are reported and
  topped up after release), exactly-once delivery under contention,
  kill‑9 stale-worker reclaim across processes, unique run/worker
  identities, and cross-process resume-to-zero idempotency.

## [2.0.0-rc.2] - 2026-07-18

Release-candidate refinement pass: native operating-system paths, lazy
state initialisation and canonical identity. Published as a GitHub
**pre-release**; not the production release.

### Changed

- **Canonical slogan corrected** everywhere to
  **"High-Performance Native IMAP Migration Engine"** (no leading "A").
  Identity has a single authoritative source (`internal/identity`) and
  is now enforced by an automated test.
- **Native per-OS application paths** (new `internal/paths`, one place
  for every location; resolving a path never creates it):
  - macOS: `~/Library/Application Support/MailFerry/` (configuration +
    State Database), `~/Library/Logs/MailFerry/`,
    `~/Library/Caches/MailFerry/`
  - Linux: XDG Base Directories (`$XDG_CONFIG_HOME/mailferry/`,
    `$XDG_STATE_HOME/mailferry/` for the database and logs,
    `$XDG_CACHE_HOME/mailferry/`), with standard fallbacks
  - Windows: `%APPDATA%\MailFerry\` (configuration),
    `%LOCALAPPDATA%\MailFerry\` (database, `Logs\`, `Cache\`)
  - Precedence: CLI flags → `mailferry.toml` (new `database.path` /
    `logging.directory` keys) → native default. A `./mailferry.toml`
    in the working directory is still honoured; a full `--portable`
    mode is planned but not yet implemented.
- **`mailferry.db` is the canonical State Database** (replacing the
  `./migration.db` working-directory default): one authoritative
  per-user database regardless of the directory MailFerry is run from.
  An existing development `./migration.db` is detected and reported
  with explicit choices — it is never adopted, moved, overwritten or
  silently duplicated. `--db PATH` continues to be honoured verbatim.
- **Zero-side-effect informational commands**: `--help`, `-h`,
  `version`, `--version`, `about`, `changelog`, `roadmap` and
  `config paths` no longer create configuration, directories, logs,
  caches or databases — resolution is separated from creation, and
  regression tests enforce it. Configuration is generated on the
  first *operational* run (`run`/`resume`) or explicitly via
  `mailferry config`; read-only State Database commands (`status`,
  `failed`, `retry-failed`, `verify`, `compact`) now refuse to create
  an empty database as a side effect.
- New **`mailferry config paths`** shows every canonical location
  (with creation status) without creating anything.
- **Restrictive permissions** on MailFerry-generated files: 0700
  application directories; 0600 configuration, State Database
  (including WAL/SHM) and logs, where the platform supports POSIX
  permissions.
- Documentation: README rewritten around the native-path/lazy-creation
  model; **CONTRIBUTING.md rewritten for the Go project** (developer
  workflow, dependency policy, engineering principles, bug-report
  guidance); new end-user **docs/INSTALLATION-MACOS.md** (architecture
  choice, Gatekeeper "Open Anyway" walk-through specific to this RC,
  checksum verification vs notarisation).

### Fixed

- Read-only inspection commands could previously create an empty
  State Database when pointed at a missing path.

## [2.0.0-rc.1] - 2026-07-18 (superseded by rc.2 before public release)

First public **Release Candidate** of MailFerry v2.0.0 — the complete
native Go rewrite. Published as a GitHub **pre-release** for real-world
testing; it is not the production release. Test carefully and keep your
source mailboxes until you have verified results. MailFerry never
expunges or deletes mail on either server.

### Added

MailFerry v2.0.0 is a ground-up rewrite in Go: a single static,
cross-platform binary (macOS arm64/amd64, Linux amd64/arm64, Windows
amd64/arm64) with no runtime dependencies — no Python, no Perl, no
imapsync. The unreleased Python development line (1.2.0-dev, preserved on the
`legacy/python-final` branch) is the behavioural reference; the Go engine
reached functional parity with it in this release candidate (audit:
docs/PARITY-v2.0.0-RC.md). Milestones:

- **M1 (complete)** — native Go IMAP core (pipelined, streamed literals,
  LITERAL+, STARTTLS/TLS, watchdogs), byte-compatible State Database and
  message fingerprints (verified: Go and Python resume each other's
  migration.db with zero duplicate appends), planner with special-use
  mapping and mUTF-7, streaming FETCH→APPEND with bounded backpressure,
  worker pools with per-host budgets, context-based graceful shutdown,
  crash reconciliation incl. the ack-lost-APPEND window, adoption,
  idempotent re-runs, classic live dashboard, wrapper-compatible CSV,
  mailferry.toml, cluster worker registration/heartbeats/dead-worker
  reclaim, results.csv and summary. Go test suite covers fresh migration,
  idempotent re-run, lost-DB adoption, incremental top-up, ack-lost APPEND
  duplicate prevention, rejected-message WARNINGS, cancel/resume and
  Python fingerprint compatibility.
- **RC parity audit (complete)** — a full regression and feature-parity
  audit of the Go release candidate against the final Python reference
  (source-level, both trees; matrix in `docs/PARITY-v2.0.0-RC.md`). Every
  gap found was restored:
  - **mailferry.toml first-run generation fixed**: the default
    configuration is now generated by *any* command on first launch (it
    previously required `run`/`config`), the note states the full path,
    and the event is recorded in `session.log`. New in Go: when a future
    version adds options, they are appended to an existing file as fully
    commented, documented defaults — append-only, idempotent and safe for
    strict TOML parsers; user customisations are never rewritten.
  - **Engine parity restored**: COMPRESS=DEFLATE (RFC 4978, auto when
    offered, wire counters stay honest beneath the deflate layer),
    `--baseline` RFC-3501-only mode, protocol `--trace` with credentials
    always redacted, engine-wide pause gating (paused time never counts
    as stalled), `--order size` admission, `--sync-flags` backup-mode
    flag re-application (order-canonical comparison), NDJSON event +
    progress logs, bounded shutdown escalation (6 s grace, then all
    connections are force-closed so Ctrl+C can never hang on a dead
    socket), "Stale lock auto-reset" history event.
  - **CLI parity restored**: real `check`/`validate` preflight, `doctor`,
    `capabilities`, `verify`, `compact`, `import-state`, `changelog`
    (embedded), `roadmap`, in-binary loopback `benchmark`,
    `failed --ignore`, `config --path`, `--no-retry`,
    `--max-conns-per-mailbox`, `--lock-timeout`, `--reset-stale-locks`
    (compatibility no-op), `--include/--exclude/--map`, `--json-logs`,
    `--json-progress`, `--check/--dry-run`, obsolete wrapper flags
    rejected with guidance, `--init` alias, >20-workers advisory.
  - **TUI parity restored**: Enter detail popups (History, Errors,
    Mailboxes), `/` search in five views, global `p` pause, `r`/`R`
    retry, `u` CSV reload, `s` sort and `f` filter, Space freeze with
    ❄FROZEN badge, `^L` repaint, `?` help, live shutdown-phase
    progression, view-render crash guard, and the system monitor
    (CPU/load/memory/RSS with 60-second sparklines) in Performance.
  - Privacy: all test fixtures, examples, comments and screenshots now
    use RFC-2606 example domains only.
  - Release naming corrected to `mailferry-v2.0.0-<os>-<arch>`
    (the RC's `-m2` suffix read as "Apple M2 only"); `build.sh` is the
    reproducible six-target build; macOS Gatekeeper analysis and the
    Developer ID signing/notarisation pipeline are documented in
    `docs/RELEASING-MACOS.md` (the RC prompt is expected behaviour for an
    unsigned, un-notarised download — production releases will be signed
    and notarised, never worked around).
  - Verification: 16 Go test suites green (including new TOML lifecycle,
    COMPRESS end-to-end, trace redaction and sync-flags idempotency
    regressions); `go vet`/`gofmt` clean; Python reference suites still
    green (89 e2e + 138 TUI).
- **M2 (complete)** — interactive **Bubble Tea + Lip Gloss TUI** compiled
  into the single binary: the familiar dashboard (banner, slogan, live
  mailbox table with progress bars, wire-based Speed/ETA, aggregate
  footer), F1–F10 navigation with 1–0 digit aliases (for terminals/SSH
  that intercept function keys), Workers/cluster roster, Mailboxes with
  detail, Queue, **History/Activity** (honest recovery vocabulary),
  Errors, Performance, **F8 Logs with Follow ON/OFF** (scroll ↑↓/PgUp/PgDn/
  Home/End + mouse wheel; F re-follows), Settings, Help/About, centred
  graceful-shutdown dialog, terminal-resize handling and a too-small
  guard. Automatic terminal detection: an interactive TTY launches the
  TUI, non-interactive/`--no-tui` runs headless — both drive the exact
  same engine (the TUI is a pure consumer of the engine event bus, so a
  resize, lost terminal or SSH disconnect can never corrupt migration
  state). Resilience parity ported and tested: stale supervisor +
  **Recovery Mode**, progressive failed-message isolation, the persistent
  **Failed Message Registry** (`failed` / `retry-failed` commands, JSON/CSV
  export, RECOVERED transitions), **COMPLETED WITH WARNINGS**, and cluster
  worker heartbeats / REMOTE progress mirroring / offline-worker takeover.
  New **`status`** command reads run/worker/registry state without
  competing with active workers. Six static binaries build CGO-free.

### Changed

- **Repository transition: the Go implementation is canonical for v2.**
  The Go module moved from `go/` to the repository root (`go build
  ./cmd/mailferry` works straight after cloning); the Python
  implementation was removed from `main` and preserved permanently on
  the `legacy/python-final` branch (plus the untouched `v1.0.0` tag).
  The embedded changelog now comes from the single root `CHANGELOG.md`
  via `go:embed` — no duplicated copies. Every source file carries the
  standard authorship/SPDX header, enforced by an automated test.

### Known limitations of this release candidate

- Deduplication mode, date-range migration and `mailferry attach` are
  **not implemented yet** (planned for final v2.0.0 — see Unreleased).
- macOS binaries are **not Developer-ID signed and not notarised**:
  Gatekeeper will ask for explicit approval on downloaded binaries
  (System Settings → Privacy & Security → Open Anyway). This is expected
  for the RC; the production signing pipeline is documented in
  docs/RELEASING-MACOS.md and will never rely on weakening Gatekeeper.
- Multi-instance clustering and COMPRESS=DEFLATE are implemented and
  covered by the automated suite, but still undergoing real-world
  validation at RC stage (use `--compress off` to rule compression out
  when diagnosing).
- Windows console support is built and cross-compiled but has had only
  limited interactive testing.

## [1.2.0-dev] - 2026-07-18 (unreleased Python development line)

The final Python-engine feature set — never published to GitHub; retained
in-repo as the behavioural and functional reference for the Go rewrite.
Folds in the never-released v1.1.0 work.

### Added
- **Intelligent failed-message isolation & recovery.** A small batch of
  poison messages can no longer trap a migration (the classic signature:
  the same 8 messages retried through every reconnect until PARTIAL, on
  every resume). MailFerry now distinguishes transport trouble from
  message trouble: pure connection losses retry the same batch (up to
  `batch_attempts`, default 3); repeated deaths on the same window — or a
  drop on messages the server already rejected — switch the folder into
  **Recovery Mode**: the batch is progressively split (8 → 4+4 → 2+2 → 1,
  3 attempts per level, with the message that was mid-APPEND at the moment
  of death probed first), the exact failing message(s) are identified,
  recorded, and the migration continues with everything else. Isolation
  state survives reconnects and restarts; source-side fetch stalls are
  never blamed on a message. Recovery Mode is fast regardless of mailbox
  size — re-entries skip completed work.
- **Persistent Failed Message Registry.** Permanently failed messages are
  recorded in the State Database (`failed_messages`) with folder, UID,
  Message-ID, subject, sender, date, size, failure type (`APPEND_NO`,
  `CONNECTION_RESET`, `TIMEOUT`, `MALFORMED_MIME`, `OVERSIZE`, `UNKNOWN`),
  reason, first/last failure timestamps, failure count and status
  (`FAILED` / `RETRY_PENDING` / `RETRYING` / `RECOVERED` / `IGNORED`).
  Future runs skip known-failed messages by default (logged); nothing is
  ever blacklisted forever: `mailferry retry-failed` re-queues everything,
  one mailbox, one folder or one UID — a successful retry becomes
  **RECOVERED**. `mailferry failed` lists the registry and exports it
  (`--csv FILE` / `--json`); every run also writes
  `logs/failed_messages.csv` when failures are outstanding.
- **COMPLETED WITH WARNINGS.** A mailbox whose healthy messages all
  migrated no longer reports PARTIAL: it completes as **WARNINGS** with a
  clear account (total / migrated / failed / success rate) on the
  dashboard, in the summary, in results.csv (exit code 0) and in History.
  The Mailboxes view gains a Fail column; the dashboard shows a red
  `MsgFail` counter and a per-mailbox warnings line.
- **`mailferry.toml` configuration.** Fully optional: on first launch a
  documented configuration file is generated at
  `~/.config/mailferry/mailferry.toml` (search order: `--config PATH` >
  `./mailferry.toml` > default) with sections `[migration]`, `[retry]`,
  `[recovery]`, `[logging]`, `[dashboard]`, `[database]`. Missing options
  fall back to built-in defaults, invalid values warn and revert, unknown
  keys warn and are ignored — a config problem can never stop MailFerry.
  CLI flags always override the file. `mailferry config` shows the active
  path and effective overrides; `[logging] keep_days` prunes old logs.
- **Clear recovery vocabulary.** History now tells the real story:
  *Stalled transfer detected → Connection recovery 1/3 → Entering
  Recovery Mode → Batch isolation → Failed message isolated → Message
  skipped → Migration resumed → Completed with warnings*. "Recovery
  exhausted" appears only when nothing else can be done (mailbox STALE).
- **Multi-instance clustering on a shared State Database.** MailFerry now
  behaves like a distributed migration platform: several instances (same
  machine or different machines) can point at the same `migration.db` and
  cooperate on one project. Every instance registers as a **Worker**
  (`hostname:pid:uuid`) and heartbeats every 15 s; mailboxes are claimed
  atomically through per-mailbox leases, so two workers can never process
  the same mailbox. A mailbox owned by a live peer shows as **REMOTE** with
  its progress mirrored live from the State Database. If a worker goes
  silent for `--worker-timeout` (default 60 s — crash, kill -9, power
  loss), its mailboxes are **reclaimed automatically** with an atomic
  compare-and-swap and resume from the last confirmed checkpoint — never
  duplicating (recovery re-entry adopts, never re-copies). A displaced
  worker that comes back detects the lost lease on its next heartbeat and
  stops that mailbox immediately. Graceful exits release ownership at once
  (peers resume unfinished jobs). Startup never refuses because "another
  instance is running" — new instances simply join the cluster. The
  Workers view (F2) gains a cluster roster: Worker ID, host, status
  (WORKING / IDLE / OFFLINE), active mailboxes, last heartbeat, connected
  since; the run keeps its dashboard live while peers still hold project
  mailboxes, standing by as a hot failover.
- **Real-time transfer speed.** The Speed column and global Rate ride the
  wire counters (which tick on every socket read/write) with a ~10 s
  rolling average, so throughput is live even while one huge message
  streams or commits are sparse. `0 B/s` now means "connected but idle";
  `-` appears only before a mailbox starts or after it finishes. ETA uses
  the accurate payload rate and falls back to the wire rate during long
  streams, so it keeps counting down instead of freezing.
- **`tail -f` log viewer (F8).** Scroll freely with ↑↓/k j, PgUp/PgDn,
  Home/End — any manual scroll switches to browsing with a clear
  `FOLLOW: OFF (press F to resume)` banner and a position indicator;
  `F` (or End) snaps back to the live tail and auto-scrolls as new entries
  arrive.
- **Stale-sync detection with automatic self-healing.** A supervisor watches
  every running mailbox for *meaningful* progress (messages committed, bytes
  delivered, folders advancing, real wire traffic). A mailbox that delivers
  nothing for `--stale-timeout` (default 5 minutes) — through reconnect
  cycles, keepalive noise or a hung transfer — is verified stale and
  recovered automatically: connections are force-closed and the runner
  reconnects and resumes from the last confirmed checkpoint (never
  duplicates — recovery re-entry adopts, never re-copies). Hard progress
  closes the episode as recovered; otherwise recovery retries up to
  `--recovery-retries` times (default 3), spaced `--recovery-interval`
  seconds (default 30). If all attempts fail the mailbox is marked
  **STALE**, the operator is notified (Errors panel, History, session log,
  summary) and manual actions remain available (`r` retry / rerun).
  Recovery kicks never consume the normal retry budget. The Dashboard shows
  a RECOVER badge during recovery, a red STALE status on failure, and a
  `Stalls n (rec m)` counter; the summary reports stalls detected /
  auto-recovered.
- **Stale locks can never block a run.** Leases left by dead workers —
  cluster or legacy — are verified against heartbeats and reclaimed
  automatically; `--lock-timeout` (default 5 minutes) caps how long any
  unexplained lease survives. A live worker can never be dispossessed:
  every takeover is an atomic compare-and-swap that fails if the owner
  heartbeats. (`--reset-stale-locks` is retained as a compatibility no-op
  — the cluster reclaims on its own.)
- **History / Activity view (F5)** — a chronological, fully navigable
  milestone feed: migrations started/completed, folders migrated,
  reconnects, stale detection & recovery, lock events, pauses and reloads.
  ↑↓ or `k`/`j` select, Enter opens a detail popup, `/` searches, End
  re-enables follow, Esc/`q` returns to the Dashboard. (`k`/`j` and `q` now
  work everywhere.)
- **Terminal User Interface (TUI)** — the primary runtime interface: the
  familiar classic dashboard (banner, info panel, mailbox table with inline
  Source Server / Destination Server detail, summary footer — same layout
  and colour scheme as v1.0.0) remains the main screen, now with an
  F1–F10 navigation bar and nine additional keyboard-driven views.
- Ten views, each an independent module: **Dashboard** (F1/1, the classic
  dashboard), **Workers** (F2/2), **Mailboxes** (F3/3), **Queue** (F4/4),
  **History/Activity** (F5/5), **Errors** (F6/6), **Performance** (F7/7,
  sparklines), **Logs** (F8/8, independent of terminal scrollback),
  **Settings** (F9/9), **Help/About** (F10/0). Digit keys `1`–`0` mirror
  `F1`–`F10` so navigation works on macOS and under tmux/screen where the
  function keys are intercepted.
- Keyboard model: arrows, PgUp/PgDn, Home/End, Enter (details), Esc (back),
  Tab/Shift+Tab, Space (freeze display), `/` (search), `s`/`f` (sort/filter),
  `p` (pause/resume), `r`/`R` (retry), `u` (reload CSV), `Ctrl+L` (redraw).
- Detail popups (Workers, Mailboxes, Errors) with a soft-shadow overlay;
  sort/filter/search on Mailboxes; severity/mailbox filters and follow mode
  on Logs.
- **Graceful shutdown dialog**: Ctrl+C shows a centred, branded Unicode
  dialog with a soft shadow reporting each phase live — stopping the
  scheduler, waiting for active Workers, saving migration state, flushing
  logs, closing IMAP connections, releasing resources, shutdown complete.
  A second Ctrl+C forces an immediate exit.
- New CLI commands: `resume` (alias of `run`), `validate` (alias of `check`),
  `doctor` (environment self-test), `benchmark` (loopback throughput),
  `changelog` and `roadmap` (release history and roadmap in the terminal).
  `--about` and F10 show a full About panel with version, author, repository,
  documentation, issue tracker, community and licence.
- Friendly small-terminal guard: below 80×20 the UI shows a centred notice
  (with live progress) and restores automatically when enlarged.
- Published documentation set under `docs/` (installation, quick start, CSV
  format, commands, TUI, sync modes, reliability, servers, troubleshooting).

### Improved
- macOS memory readout now shows used memory (via `host_statistics64`),
  not just total.
- Rendering avoids full-screen clears except on resize (per-row differential
  repaint); the final frame and summary print into normal scrollback on exit.
- Input, rendering, scheduling, logging and the State Database run on
  independent threads/loop; the interface can never block or slow migration.

### Fixed
- Ctrl+C exits cleanly, always: stop accepting new work → active Workers
  finish their current batch → connections closed with a polite LOGOUT →
  State Database and logs flush → summary prints (exit 130). A second Ctrl+C
  stops immediately; state stays consistent either way.
- **Graceful shutdown is now bounded even when a server has stalled.** A
  single Ctrl+C previously waited on any in-flight network operation (up to
  the inactivity timeout — minutes on a hung server), which looked like a
  hang and forced a second Ctrl+C. MailFerry now escalates automatically: a
  few seconds after the first Ctrl+C it closes all open IMAP connections, so
  stalled reads/writes unwind at once and shutdown completes promptly. On
  stop, connections are aborted rather than waiting for a polite LOGOUT, and
  the LOGOUT wait on clean exit is shorter. State stays consistent — the next
  run resumes exactly where it stopped.
- View table rows are ANSI-aware: colour-wrapped cells (e.g. the Mailboxes
  status icon) can no longer emit a broken escape that ate the first letter
  of the next column, and could desync the terminal into an apparent hang.
- A faulty view renders an inline error panel instead of freezing navigation.
- Stray asyncio errors during reconnects no longer spray tracebacks; they are
  routed to the session log.
- Folder counter no longer over-counts after reconnects, and mailbox totals
  no longer inflate on reconnect re-entries (resynced from the State Database).
- Abandoned transfers during shutdown/reconnect are drained so connection
  readers can never wedge on a full body queue.

### Changed
- The interactive runtime is now the TUI; `--no-tui` (alias `--no-console`)
  falls back to timestamped status lines, and output redirection auto-disables
  the TUI.
- Connection inactivity watchdog allows 3× headroom while an APPEND awaits
  acknowledgement — large messages on slow destinations no longer trigger
  premature reconnects.
- Minimum interactive terminal size is 80×20.

### Performance baseline
Loopback benchmark (`mailferry benchmark`, in-process servers — bounded by the
test harness, not a WAN figure): 20,000 × 8 KB messages across 4 mailboxes /
4 workers migrated in ~17 s ≈ **1,150 msgs/s, 9.1 MB/s**, integrity verified
(no loss, no duplicates). A relative baseline for tracking future releases.

## [1.0.0] - 2026-07-17

First stable release of MailFerry — High-Performance Native IMAP Migration
Engine.

### Added
- Native asyncio IMAP protocol core: pipelined commands, streamed message bodies
  (constant memory), non-synchronising literals (`LITERAL+`), wire
  compression (`COMPRESS=DEFLATE`), STARTTLS and implicit TLS with
  certificate verification on by default, inactivity and byte-progress
  watchdogs, capability-driven optimisation with `--baseline` fallback.
- Migration, sync and backup in one command: fresh migration, fingerprint
  **adoption** of destinations pre-synced by other tools (duplicate-free,
  even after State Database loss), and incremental top-up runs; deletions
  are never propagated; optional `--sync-flags` re-applies flag changes.
- Per-message State Database (SQLite WAL): planned/inflight/done/skipped
  states, source→destination UID mapping via `APPENDUID`, UIDVALIDITY-aware
  re-verification, crash-window reconciliation, mailbox leases,
  `import-state` migration from the legacy wrapper, `compact` maintenance
  command.
- Scheduler: worker pools, per-host connection budgets, parallel folder
  pipelines inside large mailboxes, throttle-aware backoff; authentication
  failures are never auto-retried.
- Folder planner: NAMESPACE/delimiter translation, special-use role mapping
  (localisation-proof), Gmail virtual-folder policy, mUTF-7 Unicode names,
  include/exclude globs, explicit `--map` file.
- Live Dashboard: alternate-screen differential renderer, two-line MailFerry
  banner, global progress (messages/data %, throughput, ETA, duplicates
  prevented, reconnects, retries), per-mailbox operation verbs and detailed
  Source Server / Destination Server panels; non-TTY status lines;
  `--json-progress` snapshots.
- Reporting: session log, per-mailbox logs, `results.csv`, end-of-run
  summary, optional NDJSON event logs, `--trace` protocol logging with
  credential redaction.
- CLI: `run`, `check` (write-nothing preflight), `init`, `import-state`,
  `capabilities`, `verify`, `compact`; `--version`, `--about`, `--help`;
  exit codes 0/1/130/141.
- Tooling: single-source version and identity (`mailferry/__init__.py`),
  release builder with branding/header/changelog/license validation and
  SHA-256 checksums, AGPL source-header applier, end-to-end fake-IMAP test
  suite (34 checks).
- Packaging: standalone `mailferry.pyz` (zipapp), source archive, wheel.

[Unreleased]: https://github.com/ajsap/mailferry/compare/v2.1.1...HEAD
[2.1.1]: https://github.com/ajsap/mailferry/compare/v2.1.0...v2.1.1
[2.1.0]: https://github.com/ajsap/mailferry/compare/v2.0.3...v2.1.0
[2.0.3]: https://github.com/ajsap/mailferry/compare/v2.0.2...v2.0.3
[2.0.2]: https://github.com/ajsap/mailferry/compare/v2.0.1...v2.0.2
[2.0.1]: https://github.com/ajsap/mailferry/compare/v2.0.0...v2.0.1
[2.0.0]: https://github.com/ajsap/mailferry/compare/v2.0.0-rc.3...v2.0.0
[2.0.0-rc.3]: https://github.com/ajsap/mailferry/compare/v2.0.0-rc.2...v2.0.0-rc.3
[2.0.0-rc.2]: https://github.com/ajsap/mailferry/compare/v1.0.0...v2.0.0-rc.2
[2.0.0-rc.1]: https://github.com/ajsap/mailferry/blob/main/CHANGELOG.md
[1.0.0]: https://github.com/ajsap/mailferry/releases/tag/v1.0.0
