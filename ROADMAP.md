# MailFerry Roadmap

The canonical roadmap also ships in the binary: `mailferry roadmap`.

- [x] **v1.0.0** — Initial public release (Python): native asyncio IMAP
  protocol core, per-message State Database, duplicate-free adoption,
  live two-sided dashboard, release tooling.
- [x] **v1.2-dev** — Unreleased Python reference line (preserved on the
  `legacy/python-final` branch): full TUI, self-healing stall recovery,
  failed-message isolation with a persistent registry and COMPLETED WITH
  WARNINGS, multi-instance clustering with failover, live wire-speed
  metering, mailferry.toml. Never published as a release.
- [x] **v2.0.0** — Complete architectural rewrite in Go (this
  repository): single static cross-platform binary, goroutine-based
  concurrent engine, plus destination deduplication and date-range
  migration modes. Released as the stable v2.0.0 after three public/internal
  release candidates and a full verification gate.
- [x] **v2.1.0** (released 2026-08-03) — OAuth 2.0 / modern authentication
  (SASL XOAUTH2 / OAUTHBEARER) with first-class Microsoft 365 and Google
  Workspace / Gmail providers and a generic provider; a `mailferry auth`
  command family (browser loopback + headless device-code authorisation,
  OS-keychain credential storage); bulk/MSP authentication models
  (Microsoft 365 app-only, Google domain-wide delegation); **MULTIAPPEND
  (RFC 3502) batched appends** with automatic, capability-negotiated batch
  sizing and unchanged per-message correctness; accurate run-mode
  presentation from one authoritative source; interactive, persistent F9
  Settings writing mailferry.toml atomically; an expanded typed
  configuration surface; Results completion date/time; a redesigned F6
  Errors / Failed Messages view; and a unified interactive exit contract.
- [ ] **v2.1.x** (planned) — QRESYNC/CONDSTORE delta sync, permanent
  quarantine purge, Prometheus-style metrics, and the macOS Developer ID
  signing + notarisation pipeline (docs/RELEASING-MACOS.md).
