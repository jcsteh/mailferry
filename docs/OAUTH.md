# OAuth 2.0 / modern authentication

> **Setting up a provider?** See
> **[OAUTH-SETUP.md](OAUTH-SETUP.md)** — the step-by-step Microsoft Entra /
> Google Cloud configuration guide (scopes, permissions, redirect URIs,
> admin consent, domain-wide delegation, troubleshooting). This document
> covers MailFerry's own authentication model.

MailFerry authenticates IMAP connections with either a password (`LOGIN`) or
an OAuth 2.0 bearer token (SASL **XOAUTH2**, the mechanism Microsoft 365 and
Google document and require; **OAUTHBEARER**, RFC 7628, for standards-
compliant servers). Source and destination authenticate **independently** —
migrate from a password server into a modern-auth tenant, or between two
OAuth providers, in one run.

The migration engine never learns which provider or mechanism is in use: it
consumes an already-authenticated connection. All provider knowledge lives in
`internal/oauth`; the IMAP protocol code only encodes a token it is handed.

## Named authentication profiles

An **authentication profile** is the operator abstraction: authorise once,
then reference the profile from any CSV row. The canonical 10-column CSV is
unchanged — an endpoint uses OAuth by putting **`oauth:<profile>`** in its
password column instead of a password:

```csv
srchost,srcport,srcsecurity,srcuser,srcpassword,dsthost,dstport,dstsecurity,dstuser,dstpassword
imap.example.com,993,ssl,alice@example.com,SRC-PASSWORD,outlook.office365.com,993,ssl,alice@example.net,oauth:client-a
outlook.office365.com,993,ssl,bob@example.com,oauth:client-a,imap.gmail.com,993,ssl,bob@example.net,oauth:ws-bulk
```

Row 1 migrates a password server → Microsoft 365 (OAuth). Row 2 migrates
Microsoft 365 → Gmail — both OAuth. Existing password-only CSVs keep working
unchanged, and source/destination auth is fully independent.

## The `mailferry auth` command family

```
mailferry auth add <provider> --name NAME [flags]   authorise and save a profile
mailferry auth list                                 list saved profiles (no secrets)
mailferry auth show NAME                             show one profile (no secrets)
mailferry auth test NAME [--user MAILBOX]            get a token AND try a real IMAP login
mailferry auth refresh NAME                          re-authorise a delegated profile
mailferry auth remove NAME                           delete the profile + stored secret
```

`auth show` and every other command display **references only** — access
tokens, refresh tokens, client secrets and SASL payloads are never printed.
`auth list` and `auth show` do name the **account** a delegated profile was
authorised as, so you can tell at a glance which mailbox a profile speaks for.
An email address is a label, not a credential.

`auth test` is the honest check: it mints a token *and* opens a real IMAP
connection to the provider's documented host, authenticates with the bearer
token, and logs out. Nothing is written to the mailbox (the client is put in
read-only mode for the probe). Use `--no-imap` to check the token alone, or
`--host`/`--port` for a non-default endpoint.

`auth remove` deletes the **local** profile and its stored secret. To revoke
access at the provider you must also remove the app's consent/grant in the
Microsoft Entra or Google admin console — the command says so.

## Authentication models (`--kind`)

MailFerry distinguishes three deliberately different models. **Do not blur
them** — they have very different consent and security implications.

| Kind | Who it authenticates | Provider mechanism |
|---|---|---|
| `delegated` (default) | one consenting user's own mailbox | interactive user OAuth (refresh token) |
| `application` | **many** authorised Microsoft 365 mailboxes | app-only client credentials (`IMAP.AccessAsApp`) |
| `service_account` | **many** impersonated Google Workspace mailboxes | domain-wide delegation (signed JWT) |

### Delegated (interactive)

For an individual administrator authorising their own mailbox. You register an
OAuth app with your provider and supply its `--client-id`.

**Interactive (browser):**
```
mailferry auth add microsoft365 --name client-a --client-id <app-guid> --tenant <tenant>
mailferry auth add google        --name gmail-a  --client-id <id> --client-secret env:GID_SECRET
```
MailFerry starts a `127.0.0.1` loopback listener, opens the provider's
official authorize endpoint in your browser (PKCE, RFC 8252), receives the
code on the loopback redirect, and exchanges it for a refresh token. The
copyable URL is **always** shown as well — a browser that "opened" may have
landed in the wrong profile or a background window — and if the browser
cannot be opened at all the flow still completes from that URL.

You never copy an access token by hand. That is not a supported workflow.

```
  Provider     Microsoft 365
  Profile      client-a
  Model        Delegated — one user's own mailbox (interactive sign-in)

Opening your browser to sign in...

Not the right browser or nothing happened?
Open this URL to continue:

  https://login.microsoftonline.com/...

Waiting for authorisation...

✓ Authorised
  Profile      client-a
  Provider     Microsoft 365
  Account      jeslyn@example.com

  Credential stored in the OS keychain.
  mailferry.toml holds only a reference — never the token itself.
```

### Name the mailbox you mean: `--user`

```
mailferry auth add microsoft365 --name client-a --client-id <app-guid> \
    --tenant <tenant> --user jeslyn@example.com
```

A delegated profile authenticates **whoever signs in**, and a browser that is
already signed in as somebody else will be reused by single sign-on. The
classic way to get this wrong: your tenant requires admin consent, you sign
in as the administrator to grant it, and the refresh token you walk away with
belongs to the *administrator's* mailbox.

`--user` closes that hole from both ends. It is sent as the standard OIDC
`login_hint`, so the provider pre-selects that account, and the account that
actually signed in is verified afterwards. On a mismatch **nothing is
saved**:

```
✗ Wrong account — nothing was saved.
  You asked for : jeslyn@example.com
  You signed in : itadmin@example.com
```

Retry in a private/incognito window, or sign out of the provider first.

Without `--user`, Microsoft is asked for the account picker instead, so a
stale session cannot silently choose for you. `auth refresh` uses the
profile's recorded account as the hint automatically, so re-authorising can
never quietly rebind a profile to a different mailbox.

Admin consent itself is a **one-off, tenant-wide** action and is not part of
per-user authorisation — grant it in the portal (App registrations → API
permissions → *Grant admin consent*) or at
`https://login.microsoftonline.com/{tenant}/adminconsent?client_id={client-id}`,
then have each user sign in normally. Note that consenting to the *delegated*
`IMAP.AccessAsUser.All` does not give the app access to other people's
mailboxes; for one credential covering many mailboxes use `kind =
"application"` instead.

The account line comes from the provider's OpenID Connect `id_token`:
interactive authorisations additionally request the identity-only `openid`
and `email` scopes, which grant no mailbox access. The claim is used purely
as a label — MailFerry makes no authorisation decision from it.

Cancellation (Ctrl-C) and the `--timeout` expiry are reported as such, and
nothing is saved.

**Headless / SSH (`--no-browser`):**
```
mailferry auth add microsoft365 --name client-a --client-id <app-guid> --tenant <tenant> --no-browser
```
- **Microsoft**: device-code flow (RFC 8628) — MailFerry shows a URL and a
  short code to enter on any other device, then polls until you finish.
- **Google / generic**: Google's device flow does **not** cover the IMAP mail
  scope, so MailFerry uses **manual redirect capture** — still the same
  officially-supported loopback authorization-code + PKCE flow, with the
  authorisation code carried back by copy and paste instead of by a local
  socket:

  1. MailFerry prints the authorize URL.
  2. You open it in a browser on **your own** machine and sign in.
  3. The browser is redirected to a `127.0.0.1` address it cannot reach —
     this is expected and is not an error.
  4. You copy that whole address out of the browser's address bar and paste
     it back into MailFerry, which extracts the code (verifying the PKCE
     state) and completes the exchange.

  The loopback listener runs at the same time, so if you *can* reach the
  host's port (e.g. `ssh -L`), it simply completes by itself and you never
  need to paste anything. MailFerry never emulates a device flow the
  provider does not offer. For truly unattended Google bulk migration, use a
  `service_account` instead.

### Application — Microsoft 365 bulk (one app → many mailboxes)

App-only access with the Exchange Online **`IMAP.AccessAsApp`** application
permission and tenant-admin consent. One tenant application authenticates
every authorised mailbox; the target mailbox is the SASL login (the CSV user).

```
mailferry auth add microsoft365 --name tenant-bulk --kind application \
    --client-id <app-guid> --tenant <tenant> \
    --client-secret keychain:mailferry/tenant-bulk
```

Prerequisites (Microsoft docs are authoritative): register the app, grant the
`IMAP.AccessAsApp` **application** permission with admin consent, register the
service principal in Exchange Online, and scope mailbox access with an
application access policy. MailFerry requests the `.default` scope of
`https://outlook.office365.com`.

### Service account — Google Workspace bulk (one account → many mailboxes)

A Google **service account with domain-wide delegation**, authorised by a
Workspace admin for the `https://mail.google.com/` scope. MailFerry signs a
JWT impersonating each target user (the CSV user) and exchanges it for that
user's access token.

```
mailferry auth add google --name ws-bulk --kind service_account \
    --key-json file:/etc/mailferry/sa.json
```

Prerequisites (Google docs are authoritative): create a service account,
enable domain-wide delegation, and in the Workspace Admin console authorise
its client ID for the `https://mail.google.com/` scope. That scope is usable
**only** via domain-wide delegation.

## Secure credential storage

MailFerry's posture is that credentials are the operator's and are never
persisted by MailFerry in plaintext config. `auth add` stores the obtained
refresh token in the **OS-native secure store** where reachable:

| Platform | Store | Reference written to `mailferry.toml` |
|---|---|---|
| macOS | login keychain (`security`) | `keychain:mailferry/<name>` |
| Linux | Secret Service via `secret-tool` (libsecret) | `keychain:mailferry/<name>` |
| Windows | **DPAPI**, `CryptProtectData`, current-user scope | `dpapi:<path>` |
| No OS secret service (e.g. headless Linux without a Secret Service) | **0600 file** | `file:<path>` |

A backend is chosen **only** when MailFerry has verified it can also *read
the value back on that platform*. Writing a reference a later migration run
could not resolve would be a defect, not a fallback, and there is a test for
exactly that.

Windows has no first-class command-line credential store that returns a
plaintext secret, so MailFerry does not pretend `keychain:` works there.
Instead it protects the secret with the OS's own Data Protection API under
the current-user scope and stores the ciphertext: only the same Windows
account on the same machine can decrypt it. That is real OS-provided
cryptography with an OS-managed key — not MailFerry-invented crypto.

The profile carries only a **reference** — never the secret. The 0600
fallback is filesystem-permission protection, **not** encryption: MailFerry
deliberately does not invent crypto and does not store a key beside the
ciphertext (that would be security theatre). When the fallback is used,
`auth add` says so explicitly, and the documented secure alternative is to
keep the secret in an external manager and reference it with `env:`, `file:`
on encrypted storage, or `cmd:` (a Vault / 1Password / gcloud CLI, say).

`auth add` for an `application` profile will prompt for the client secret
with **no echo** and store it in the secret store, rather than requiring you
to place it somewhere by hand first; `--key-json` accepts a plain file path
and imports the service-account key into the store the same way. Neither
weakens the posture — both move a secret *out* of a hand-managed file and
into the OS store.

You may also supply secrets directly as references for `application` /
`service_account` profiles and for automation:

| Reference | Source |
|---|---|
| `env:NAME` | environment variable |
| `file:/path` | a file (0600 recommended), whitespace-trimmed |
| `keychain:SERVICE[/ACCOUNT]` | the OS secret service (macOS keychain / Linux Secret Service) |
| `dpapi:/path` | a Windows DPAPI-protected file (current-user scope) |
| `cmd:program args` | stdout of a command (any external secret store) |

A **literal** secret written into a profile is rejected before the run starts.

## Token lifecycle

- Delegated profiles mint short-lived access tokens from the refresh token and
  refresh automatically across a multi-hour migration and every reconnect.
- Application/service-account profiles mint tokens on demand — no interactive
  re-authorisation is ever needed (`auth refresh` says so).
- Each token source serialises its own refreshes, so concurrent workers never
  race or corrupt credential state. Rotated refresh tokens are kept in memory
  for the run only and never persisted.
- **Administrative profiles are shared across mailboxes.** A Microsoft 365
  `application` profile mints one tenant-level token that authorises every
  consented mailbox, so a 500-mailbox migration asks Entra ID for it **once**
  — not 500 times, and not once per worker. Delegated and service-account
  profiles are keyed per user, because there the token *is* the user.
- MailFerry never silently falls back from OAuth to password `LOGIN`. Failures
  are actionable and distinguish: mechanism not advertised, `invalid_grant`
  (expired/revoked — re-authorise), `invalid_scope`/consent, tenant/domain
  policy, and provider/network errors.

## What is never written

Tokens and secrets never appear in `migration.csv`, `mailferry.toml`,
`mailferry.db`, `session.log`, `--trace` logs, `results.csv`,
`failed_messages.csv`, TUI screens, or error messages. The XOAUTH2/OAUTHBEARER
SASL payload is redacted from `--trace`.

## Manual verification against real providers

Automated tests use a fake provider and never contact Microsoft or Google. To
verify against a real tenant:

1. Register the app / service account and grant the scopes above.
2. `mailferry auth add …` (or supply references for bulk kinds).
3. `mailferry auth test <name> --user <test-mailbox>` — proves a token is
   obtained **and** that it authenticates a real IMAP session (neither the
   token nor the SASL payload is ever shown).
4. Point a one-row CSV at a **test** mailbox with `oauth:<name>`.
5. `mailferry check mailboxes.csv` — connect + SASL auth + LIST.
6. `mailferry run mailboxes.csv --dry-run` — plans without writing anything.
7. Confirm no token appears in the session log or a `--trace` log.
