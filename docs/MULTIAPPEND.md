# MULTIAPPEND — batched IMAP uploads

MailFerry can upload several messages in **one** IMAP `APPEND` command when
the destination server supports the MULTIAPPEND extension
([RFC 3502](https://www.rfc-editor.org/rfc/rfc3502)). It is on by default,
it is transparent, and it is **only an optimisation** — it never changes
what is migrated or how migration correctness is established.

If you read nothing else:

* **Workers** = how many mailboxes migrate **at the same time**.
* **MULTIAPPEND batch size** = how many messages are **grouped into one
  append command**.

Those are different things. Raising the batch size does not add concurrency,
and raising Workers does not make batches bigger.

---

## What it does

Without MULTIAPPEND, every message costs a full command/response exchange on
the destination connection:

```
C: MF0012 APPEND "INBOX" (\Seen) "17-Jul-2026 10:00:00 +0000" {4211+}
C: <4211 octets>
S: MF0012 OK [APPENDUID 1111 87] append done
```

With MULTIAPPEND, one command carries several messages, each keeping its own
flags, its own INTERNALDATE and its own literal:

```
C: MF0012 APPEND "INBOX" (\Seen) "17-Jul-2026 10:00:00 +0000" {4211+}
C: <4211 octets>
C:  (\Flagged) "18-Jul-2026 09:14:00 +0000" {8930+}
C: <8930 octets>
C:  "18-Jul-2026 09:20:11 +0000" {2205+}
C: <2205 octets>
C:
S: MF0012 OK [APPENDUID 1111 87:89] append done
```

Measured on the built-in loopback benchmark (`mailferry benchmark`,
400 synthetic messages of ~8 KB, in-process servers):

| Setting | APPEND commands | Messages per command |
|---|---|---|
| `--multiappend off` | 400 | 1.00 |
| `--multiappend auto` (default) | 50 | 8.00 |

That is an **8× reduction in append commands** for that corpus and that
configuration. It is a protocol-round-trip measurement, not a throughput
claim: how much wall-clock time it saves depends entirely on your network
latency, your destination server and your message sizes. On a fast LAN it
may be barely measurable; across a high-latency link to a remote destination
it can matter a great deal. **MailFerry does not claim a universal speed-up.**

---

## Configuration

```toml
[migration]
# "auto" (default) uses MULTIAPPEND whenever the destination advertises it.
# "off" never uses it.
multiappend = "auto"

# "auto" (default) sizes each batch automatically.
# A fixed count (2-100) overrides it.
multiappend_batch_size = "auto"
```

Command line (highest precedence — **CLI > TOML > defaults**):

```
mailferry run mailboxes.csv --multiappend auto|off
mailferry run mailboxes.csv --multiappend-batch-size auto|N
```

F9 Settings, under **Migration**:

```
  MULTIAPPEND              auto
  MULTIAPPEND batch size   auto
```

All three surfaces validate through the same typed option catalogue
(`internal/config/options.go`), so an out-of-range value is rejected
identically wherever you type it.

### Why there is no separate `on`

`on` and `auto` would do exactly the same thing. MailFerry cannot make an
unsupported server accept MULTIAPPEND, and failing a migration because an
*optimisation* is unavailable would be wrong. So there are two honest states —
use it when it is available, or never use it — and `on`, `true` and `yes` are
accepted as spellings of `auto` rather than as a third behaviour.

---

## The `Auto` heuristic

RFC 3502 makes a MULTIAPPEND **all-or-nothing**:

> If the append is unsuccessful for any reason (including being cancelled),
> the mailbox MUST be restored to its state before the APPEND attempt; no
> partial appending is permitted.

A batch is therefore an atomic unit of *work*. Its size trades round trips
saved against work redone when something goes wrong, and against how large a
transaction the destination is asked to hold open. Four limits apply and the
**smallest wins**:

| Limit | Auto value | Why |
|---|---|---|
| Message count | `clamp(256 / concurrency, 8, 32)` | No server documents a hard MULTIAPPEND limit, so stay conservative. Each concurrent destination pipeline holds its own open transaction, so the budget is shared out. |
| Aggregate bytes | `clamp(256 MiB / concurrency, 4 MiB, 32 MiB)` | 100 × 4 KB and 100 × 50 MB are not the same request. Bounds transaction size and the cost of retrying a rejected batch. |
| Source pipeline | never wider than the FETCH window (default 8) | The destination command stays open while each next body arrives; waiting on bodies that were never requested would hold it open for nothing. |
| Solo threshold | a message ≥ `byte budget / 4` is appended **alone** | One large message must not drag smaller ones into its atomic failure domain. |

`concurrency` = `parallel_mailboxes × parallel_folders` — the number of
destination append pipelines the configuration may run at once.

With MailFerry's defaults (10 mailboxes × 3 folder pipelines, fetch window 8)
that resolves to **8 messages or 8 MiB per command, with anything over 2 MiB
sent alone**.

### These limits do not bound client memory

They do not need to. Message bodies stream from the source socket to the
destination socket in fixed 64 KB chunks whether batching is on or off.
Memory stays **O(chunk)**, never **O(batch)** — a 32-message batch of 10 MB
messages never puts 320 MB in RAM. The byte budget exists for the *server's*
transaction and for retry cost, not for MailFerry's heap.

### The append window is unchanged

MailFerry has always bounded **in-flight messages** with the append window,
and batching does not redefine it. Eight messages in one command and eight
separate commands leave exactly the same amount of work unsettled, so
cancellation stays as responsive as it ever was.

---

## Capability negotiation and fallback

MailFerry **never** issues MULTIAPPEND speculatively.

| Situation | Behaviour |
|---|---|
| Destination advertises `MULTIAPPEND` | Batching is used according to configuration. |
| Destination does not advertise it | Transparent single-message `APPEND`. Not a warning, not an error. |
| `--baseline` (RFC 3501 only) | Extensions are off, including this one. |
| `--dry-run` | Neither `APPEND` nor MULTIAPPEND is ever issued. |
| Advertised, then answers `NO` | The batch is atomic, so **nothing was stored**. Those messages are retried individually so the real offender is identified. A second rejection disables batching for the rest of the mailbox. |
| Advertised, then answers `BAD` | The server could not even *parse* the command, so the streamed literals may have been misinterpreted. The connection is dropped, per-message reconciliation runs, batching is disabled and the migration continues with single appends. |

Falling back is always safe **because** of RFC 3502 atomicity: a rejected
batch appended nothing, so nothing can be uploaded twice by the fallback
itself.

---

## Correctness

Batching changes the **protocol**, never the bookkeeping. Every guarantee
MailFerry already made still holds, and the same tests enforce them:

* **A batch is never a unit of durable accounting.** Each message keeps its
  own intent row, its own `done` row, its own destination UID and its own
  place in the statistics.
* **The statistics identity is unchanged**:
  `MsgsDone == Appended + Adopted + PriorDone + Planned`. Progress is counted
  in messages; a command carrying 20 messages advances progress by 20.
* **Idempotent reruns, adoption and Message-ID recovery** work exactly as
  before — they operate on messages, and messages are what the State Database
  records.
* **Flags and INTERNALDATE are preserved per message**, byte-for-byte
  identically to the single-append path.
* **The Failed Message Registry, retry behaviour and Recovery Mode** are
  untouched.
* **MailFerry still never expunges or deletes mail.**

### Acknowledgement loss — the dangerous case

MailFerry sends a batch of A, B, C, D. The destination stores all four. The
connection dies before the tagged `OK` arrives. Did they land?

MailFerry does not guess, and it does **not** resend the batch:

1. Every message in the batch already has a durable `inflight` intent row,
   written *before* the command opened.
2. On reconnect, reconciliation scans the destination tail and
   fingerprint-matches **each message individually** (Message-ID based, the
   same mechanism that survives a lost State Database).
3. Messages found on the destination are **adopted** — recorded as done
   without copying. Only messages genuinely absent are appended again.

This is the existing acknowledgement-loss reconciliation model, unchanged;
batching simply widens the window it already covers. The regression test
`TestMultiAppendAckLossReconcilesWithoutDuplicates` kills the connection
after a batch is committed but before the acknowledgement, and asserts that
every message arrives exactly once.

If the source body stream fails **mid-literal**, the declared literal can no
longer be completed, so the destination connection is torn down deliberately
rather than left desynchronised — and the same per-message reconciliation
resolves what landed.

### Cancellation settles what it has already sent

Stopping a run (Ctrl-C, a stop request) closes the batch at a **message
boundary** — a MULTIAPPEND with fewer messages than planned is perfectly
valid — and then **waits for the acknowledgements of the commands already on
the wire** before unwinding.

This matters. Walking away from an append the destination has already been
asked to perform leaves that message's outcome unknown, and the destination
may still be committing it while the *next* run's reconciliation scans — so
the resume can fail to see it and copy it a second time. Settling first
records the true outcome instead.

The wait is bounded by the append window (the same cap that already governs
how much work may be unsettled), so cancellation stays prompt. If the
connection dies while draining, the remaining messages stay `inflight` and
the ordinary reconciliation path resolves them.

`TestMultiAppendCancelResumeNeverDuplicates` cancels at six different points,
batched and unbatched, and asserts the resumed migration ends with every
message present exactly once.

---

## Where it shows up

MULTIAPPEND is an optimisation, so it deliberately does **not** appear in the
primary Mode line. `Migration & Sync` stays `Migration & Sync`; the Mode line
is reserved for things that change what a run *does* (Dry Run, Date Range,
Portable, Ephemeral, Backup, Baseline).

You will find it in:

* **F9 Settings** — the current setting, editable.
* **Per-mailbox logs** — a per-folder summary such as
  `MULTIAPPEND: 240 message(s) uploaded in 30 append command(s) (average 8.0 per command)`,
  plus an explicit line if the destination ever forces a fallback.
* **`--trace`** — the commands themselves (credentials redacted, bodies never
  written).
* **`mailferry benchmark`** — measured command counts and batch sizes.

---

## Troubleshooting

**A server behaves oddly under batching.** Turn it off and re-run:

```
mailferry run mailboxes.csv --multiappend off
```

Nothing else changes; the migration is byte-for-byte the same, just one
command per message. Resume is unaffected — MailFerry picks up from the same
per-message state either way.

**You want a specific batch size** (for a server you know well, or to compare
protocol behaviour):

```
mailferry run mailboxes.csv --multiappend-batch-size 20
```

The aggregate byte guard and the solo threshold still apply, and the source
fetch window is widened to match so the command is not left waiting.

**Which servers support it?** Dovecot, Cyrus and several other open-source
IMAP servers advertise MULTIAPPEND. Microsoft 365 / Exchange Online and
Gmail do **not** — migrations *to* those destinations transparently use
single appends, which is exactly the behaviour MailFerry has always had.
Check any server with:

```
mailferry capabilities imap.example.com 993
```
