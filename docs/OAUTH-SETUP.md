# Configuring OAuth 2.0 authentication for MailFerry

A step-by-step provider setup guide for system administrators, MSPs and
migration professionals.

> **Verified against the official Microsoft and Google documentation as of
> 01/08/2026.**
>
> Provider portals change their wording and layout frequently. This guide
> uses stable, official product and setting names rather than screenshots,
> and every scope, permission name, cmdlet and endpoint below was taken from
> the vendor's own current documentation on that date — not from memory.

This document describes **only what MailFerry v2.1.0 actually
implements**. Nothing here is planned or hypothetical.

For MailFerry's authentication model itself — profiles, CSV references,
secret storage, the token lifecycle — see [OAUTH.md](OAUTH.md). This guide
is about configuring the *provider*.

All examples use domains and addresses reserved for documentation by
[RFC 2606](https://www.rfc-editor.org/rfc/rfc2606) and
[RFC 6761](https://www.rfc-editor.org/rfc/rfc6761): `example.com`,
`example.org`, `example.net`, and the `.example` / `.test` / `.invalid`
top-level domains. Substitute your own.

---

## Contents

- [Before you start](#before-you-start)
- [Choosing an authentication model](#choosing-an-authentication-model)
- [Microsoft 365 / Exchange Online](#microsoft-365--exchange-online)
  - [Delegated (one user's own mailbox)](#m365-delegated)
  - [Application / app-only (many mailboxes)](#m365-application)
- [Google Workspace / Gmail](#google-workspace--gmail)
  - [Delegated (one user's own mailbox)](#google-delegated)
  - [Service account with domain-wide delegation (many mailboxes)](#google-service-account)
- [Testing with `mailferry auth test`](#testing-with-mailferry-auth-test)
- [Headless and server authentication](#headless-and-server-authentication)
- [Common configuration mistakes](#common-configuration-mistakes)
- [Troubleshooting](#troubleshooting)
- [Security and least privilege](#security-and-least-privilege)

---

## Before you start

You need:

- MailFerry v2.1.0 or later.
- Administrative access to the provider tenant (or a cooperative
  administrator — some steps *require* an administrator and cannot be
  delegated to the mailbox owner).
- IMAP enabled on the mailboxes you intend to migrate. This is a frequent
  blocker and is **not** an OAuth problem; see
  [Troubleshooting](#troubleshooting).

MailFerry authenticates IMAP with SASL **XOAUTH2**, the mechanism both
Microsoft and Google document for IMAP, and supports **OAUTHBEARER**
(RFC 7628) for other standards-compliant servers.

Source and destination authenticate **independently**. A migration may have
an OAuth source and a password destination, two different OAuth providers,
or any other combination.

---

## Choosing an authentication model

This is the most consequential decision, and getting it wrong wastes the
most time. MailFerry keeps the three models deliberately distinct.

| Model (`--kind`) | Authenticates | Interactive sign-in | Use when |
|---|---|---|---|
| `delegated` (default) | **one** mailbox — whoever signs in | Once per mailbox | You are migrating a handful of mailboxes, or the mailbox owner is present |
| `application` | **many** Microsoft 365 mailboxes | None | Tenant or MSP migration on Microsoft 365 |
| `service_account` | **many** Google Workspace mailboxes | None | Tenant or MSP migration on Google Workspace |

A delegated token *is* the signed-in user. It cannot reach anybody else's
mailbox, no matter what consent an administrator grants. If you find
yourself planning to sign in interactively as fifty different people, you
want an administrative model instead.

---

## Microsoft 365 / Exchange Online

Both models start from one Microsoft Entra app registration.

**IMAP endpoint:** `outlook.office365.com`, port `993`, implicit TLS
(`ssl` in MailFerry's CSV).

<a name="m365-delegated"></a>

### Delegated — one user's own mailbox

#### 1. Register the application

Microsoft Entra admin center (or Azure portal) → **Microsoft Entra ID** →
**App registrations** → **New registration**.

| Setting | Value |
|---|---|
| Name | anything, e.g. `MailFerry Migration` |
| Supported account types | *Accounts in this organizational directory only* (single tenant) unless you are an MSP registering centrally |
| Redirect URI | see below |

Record the **Application (client) ID** and the **Directory (tenant) ID**.

#### 2. Add the redirect URI

MailFerry performs the loopback authorization-code flow with PKCE
(RFC 8252). It binds an ephemeral port on the loopback interface and sends
exactly:

```
http://127.0.0.1:<random-port>/callback
```

Add this under the **Mobile and desktop applications** platform.

Two rules from Microsoft's redirect URI documentation matter here:

- **The port is ignored** when matching a loopback redirect URI, so you
  register the URI *without* a port. The **path is not ignored** — it must
  be `/callback`.
- Microsoft's portal **may refuse an `http://127.0.0.1` URI typed into the
  Redirect URIs box**. Microsoft's documented workaround is to add it by
  editing the app registration's **manifest**, in the `replyUrlsWithType`
  attribute, with type `InstalledClient`:

  ```json
  "replyUrlsWithType": [
    { "url": "http://127.0.0.1/callback", "type": "InstalledClient" }
  ]
  ```

Microsoft explicitly recommends the IP literal `127.0.0.1` over `localhost`
so the flow does not break on a renamed network interface or a firewall
rule — which is why MailFerry uses it.

If you get `AADSTS50011`, the error page states the exact redirect URI that
was sent. Register that.

#### 3. Add the API permission

**API permissions** → **Add a permission** → **APIs my organization uses** →
search for **Office 365 Exchange Online** → **Delegated permissions** →

```
IMAP.AccessAsUser.All
```

> This permission is under **Office 365 Exchange Online**, *not* Microsoft
> Graph. Searching only the Microsoft APIs tab will not find it.

MailFerry requests the full scope URI Microsoft documents:

```
https://outlook.office.com/IMAP.AccessAsUser.All
```

plus `offline_access` (so a long migration can refresh its token) and the
identity-only `openid` and `email` scopes, which grant no mailbox access and
exist purely so MailFerry can tell you *which* account signed in.

#### 4. Allow public client flows

**Authentication** → **Advanced settings** → **Allow public client flows** →
**Yes**. MailFerry is a public client: it holds no client secret for
delegated sign-in, which is correct for a native application under RFC 8252.

#### 5. Grant admin consent — once, separately

If your tenant has user consent disabled (common), users will see
*"Need admin approval"*. **Consent is a one-off, tenant-wide action. It is
not part of authorising a mailbox.**

Either **API permissions** → **Grant admin consent for &lt;tenant&gt;**, or
have an administrator open:

```
https://login.microsoftonline.com/example.com/adminconsent?client_id=<application-client-id>
```

Then have each *user* sign in normally.

> Granting admin consent to the **delegated** `IMAP.AccessAsUser.All`
> permission does **not** give the application access to other people's
> mailboxes. It only removes the approval prompt. If you want one credential
> that reaches many mailboxes, use the
> [application model](#m365-application).

#### 6. Create the MailFerry profile

```bash
mailferry auth add microsoft365 \
    --name m365-user \
    --client-id 00000000-0000-0000-0000-000000000000 \
    --tenant example.com \
    --user jeslyn@example.com
```

`--tenant` accepts the tenant GUID, a verified domain, or `common`.

`--user` is strongly recommended. It pre-selects the account at the sign-in
page and **refuses to save the profile if a different account signs in** —
the standard way an operator ends up with a profile bound to the
administrator who granted consent. See
[Common configuration mistakes](#common-configuration-mistakes).

<a name="m365-application"></a>

### Application / app-only — many mailboxes

One tenant application authenticates every mailbox an administrator has
authorised. No interactive sign-in, ever. This is the MSP model.

Steps 1 and 4 above still apply (register the app; no redirect URI is needed
for this model). Then:

#### 1. Add the application permission

**API permissions** → **Add a permission** → **APIs my organization uses** →
**Office 365 Exchange Online** → **Application permissions** →

```
IMAP.AccessAsApp
```

#### 2. Grant tenant admin consent

Required for every tenant. In your own tenant, use **Grant admin consent for
&lt;tenant&gt;** on the API permissions page.

For a **multi-tenant** (ISV/MSP) registration, Microsoft documents the
consent URL for POP and IMAP as using the `https://ps.outlook.com/.default`
scope:

```
https://login.microsoftonline.com/<tenant>/v2.0/adminconsent?client_id=<client-id>&redirect_uri=<redirect-uri>&scope=https://ps.outlook.com/.default
```

> Note the asymmetry, which trips people up: the **consent** request uses
> `https://ps.outlook.com/.default`, but the **access token** request uses
> `https://outlook.office365.com/.default`. MailFerry requests the latter
> automatically.

#### 3. Create a client secret

**Certificates & secrets** → **New client secret**. Copy the *value*
immediately — the portal shows it once.

#### 4. Register the service principal in Exchange Online

Consent alone is not enough; Exchange needs its own service principal.

```powershell
Install-Module -Name ExchangeOnlineManagement
Import-Module ExchangeOnlineManagement
Connect-ExchangeOnline -Organization <tenant-id>

New-ServicePrincipal -AppId <application-client-id> -ObjectId <enterprise-app-object-id>
```

> **The single most common failure in this whole process:** `ObjectId` is the
> Object ID from the **Enterprise applications** blade, **not** the Object ID
> shown on the App registrations overview page. Microsoft's documentation
> calls this out explicitly. Using the wrong one produces an authentication
> failure that looks like a credential problem.

#### 5. Grant mailbox access

Microsoft documents scoping app-only IMAP access per mailbox with
`Add-MailboxPermission`:

```powershell
$sp = Get-ServicePrincipal -Identity "<service-principal-display-name>"
Add-MailboxPermission -Identity "jeslyn@example.com" -User $sp.Identity -AccessRights FullAccess
```

Repeat for each mailbox the migration must reach. **Grant only the mailboxes
you are actually migrating** — see
[Security and least privilege](#security-and-least-privilege).

#### 6. Create the MailFerry profile

```bash
mailferry auth add microsoft365 \
    --name m365-tenant \
    --kind application \
    --client-id 00000000-0000-0000-0000-000000000000 \
    --tenant example.com
```

MailFerry prompts for the client secret without echoing it and stores it in
your OS secret store, writing only a reference into `mailferry.toml`. You may
instead pass an existing reference:

```bash
    --client-secret env:MAILFERRY_M365_SECRET
```

One profile now serves every authorised mailbox. The CSV's `srcuser` selects
which mailbox each row touches — this is exactly the mechanism Microsoft
documents for shared and delegated mailbox access, where the XOAUTH2
username field carries the target mailbox rather than the token's own
identity.

```csv
srchost,srcport,srcsecurity,srcuser,srcpassword,dsthost,dstport,dstsecurity,dstuser,dstpassword
outlook.office365.com,993,ssl,jeslyn@example.com,oauth:m365-tenant,mail.example.net,993,ssl,jeslyn@example.net,DestPassword1
outlook.office365.com,993,ssl,ann@example.com,oauth:m365-tenant,mail.example.net,993,ssl,ann@example.net,DestPassword2
```

MailFerry mints the tenant-level token **once** and shares it across every
mailbox and worker in the run.

---

## Google Workspace / Gmail

**IMAP endpoint:** `imap.gmail.com`, port `993`, implicit TLS.

<a name="google-delegated"></a>

### Delegated — one user's own mailbox

#### 1. Create an OAuth client

Google Cloud Console → select or create a project → **APIs & Services**.

1. **Enable the Gmail API** for the project.
2. Configure the **OAuth consent screen**. For a Workspace domain, choose
   **Internal** so only your own users can consent.
3. **Credentials** → **Create credentials** → **OAuth client ID** →
   application type **Desktop app**.

Record the **Client ID** and **Client secret**. Google issues a secret even
for desktop clients; it is not treated as confidential under RFC 8252, and
MailFerry sends it alongside PKCE when one is configured.

#### 2. Scope

MailFerry requests the scope Google documents for IMAP:

```
https://mail.google.com/
```

plus the identity-only `openid` and `email` scopes so it can report which
account signed in. Google's consent screen will describe the mail scope as
full mail access — that is inherent to IMAP, not to MailFerry.

#### 3. Create the MailFerry profile

```bash
mailferry auth add google \
    --name gmail-user \
    --client-id 000000000000-xxxxxxxxxxxxxxxxxxxx.apps.googleusercontent.com \
    --client-secret env:MAILFERRY_GOOGLE_SECRET \
    --user jeslyn@example.com
```

MailFerry requests `access_type=offline` and `prompt=consent`, which is what
guarantees Google returns a refresh token — without it a long migration
would stop when the first access token expired.

<a name="google-service-account"></a>

### Service account with domain-wide delegation — many mailboxes

#### 1. Create the service account and key

Google Cloud Console → **IAM & Admin** → **Service accounts** → **Create
service account**. Then open the service account → **Keys** → **Add key** →
**Create new key** → **JSON**. The key file downloads once.

Record the service account's **Client ID** — the numeric one, not the email
address. You need it in the next step.

#### 2. Authorise domain-wide delegation

This must be done by a Google Workspace **super administrator**:

**Admin console** → **Security** → **Access and data control** →
**API controls** → **Domain wide delegation** → **Manage Domain Wide
Delegation** → **Add new**.

| Field | Value |
|---|---|
| Client ID | the service account's **numeric** client ID |
| OAuth scopes | `https://mail.google.com/` |

Google notes that authorisation **may take up to 24 hours to propagate**. If
a freshly authorised service account fails, wait before assuming it is
misconfigured.

> **Scope choice.** Google also documents
> `https://www.googleapis.com/auth/gmail.imap_admin`, which is usable *only*
> with domain-wide delegation and changes IMAP behaviour: all labels are
> exposed regardless of the user's own IMAP settings, and folder size limits
> are ignored. That can be desirable for migration completeness. MailFerry
> defaults to `https://mail.google.com/`; to use the admin scope instead,
> authorise it in the Admin console and pass
> `--scopes https://www.googleapis.com/auth/gmail.imap_admin`.

#### 3. Create the MailFerry profile

```bash
mailferry auth add google \
    --name ws-tenant \
    --kind service_account \
    --key-json /secure/path/service-account.json
```

Give `--key-json` either a **file path** (MailFerry imports the key into your
OS secret store and writes only a reference) or an existing reference such
as `file:/etc/mailferry/sa.json`.

MailFerry signs an RS256 JWT assertion per mailbox with the claims Google
requires — `iss` (service account), `sub` (the mailbox being impersonated),
`scope`, `aud`, `iat`, `exp` — and exchanges it for that user's access
token. The CSV's `srcuser` is the impersonated mailbox.

---

## Testing with `mailferry auth test`

Always test before pointing a migration at anything.

```bash
mailferry auth test m365-user
mailferry auth test m365-tenant --user jeslyn@example.com
```

It runs the whole chain as separate, individually reported stages:

```
  Profile      m365-user
  Provider     Microsoft 365
  Model        Delegated — one user's own mailbox (interactive sign-in)
  Mailbox      jeslyn@example.com
  Authorised   2026-08-01 09:14 NZST (0 day 00:12:31 ago)

  Obtaining access token             ✓  XOAUTH2 · 1682 bytes (not shown) · expires in 0 day 00:59:12
  Connecting imaps://outlook.office365.com:993   ✓  TLS established
  Authenticating SASL XOAUTH2        ✓  mailbox authenticated
  Listing folders (LIST "" "*")      ✓  14 folder(s) visible

Server capabilities   (18 advertised)
  IMAP4REV1    UIDPLUS      MOVE         IDLE
  ...

  MULTIAPPEND    not advertised — one append per message (no action needed)
  UIDPLUS        supported — APPENDUID confirms every copy
  ...
```

Because each stage is reported separately, a failure tells you *where* the
chain broke — OAuth, TLS, SASL, or IMAP itself. `--no-imap` checks only the
token. `--host` / `--port` test a non-default endpoint.

Nothing is written to the mailbox: the probe client runs read-only, and
`auth test` never modifies your configuration.

---

## Headless and server authentication

A remote migration server with no graphical browser has two supported paths.

**Microsoft — device code (RFC 8628):**

```bash
mailferry auth add microsoft365 --name m365-user --no-browser \
    --client-id <client-id> --tenant example.com
```

MailFerry prints a URL and a short code; complete the sign-in on any other
device while MailFerry polls.

**Google — manual redirect capture:**

```bash
mailferry auth add google --name gmail-user --no-browser \
    --client-id <client-id> --client-secret env:GOOGLE_SECRET
```

Google's device flow does not cover the IMAP mail scope, so MailFerry does
not pretend otherwise. Instead it uses the *same* officially supported
loopback + PKCE flow, with the authorisation code carried back by
copy-and-paste:

1. MailFerry prints the authorisation URL.
2. Open it in a browser on your own machine and sign in.
3. The browser is redirected to a `127.0.0.1` address it cannot reach. This
   is expected and is not an error.
4. Copy that entire address from the browser's address bar and paste it back
   into MailFerry.

The loopback listener runs concurrently, so if you *can* reach the port
(`ssh -L`, for example) the flow simply completes by itself.

**For unattended migration, prefer an administrative model.** Neither
delegated path is suitable for automation: both need a human once per
mailbox.

---

## Common configuration mistakes

**Signing in as the administrator to clear the consent prompt.** The single
most common error. A delegated token belongs to whoever signed in, so
granting consent *during* authorisation leaves you with a profile bound to
the administrator's mailbox. Grant consent separately, then use `--user` so
MailFerry verifies the account and refuses to save a mismatch.

**Reusing a browser session.** Single sign-on will silently reuse an
existing session. Use a private window, or sign out first.

**Looking for `IMAP.AccessAsUser.All` under Microsoft Graph.** It is under
*Office 365 Exchange Online*.

**Using the App registrations Object ID in `New-ServicePrincipal`.** It must
be the **Enterprise applications** Object ID.

**Registering the wrong redirect URI.** The path must be `/callback`; the
port is ignored. `http://localhost/callback` does not match
`http://127.0.0.1:PORT/callback` — the host strings differ.

**Forgetting `offline_access` in a hand-written profile.** Without a refresh
token a migration stops when the first access token expires. MailFerry
requests it automatically during `auth add`.

**Expecting delegated admin consent to grant tenant-wide mailbox access.**
It does not. That is what the application model is for.

**Not waiting for Google domain-wide delegation to propagate.** Up to 24
hours.

**IMAP disabled on the mailbox.** Not an OAuth problem at all.

---

## Troubleshooting

| Symptom | Cause and fix |
|---|---|
| `AADSTS50011` reply URL mismatch | The registered redirect URI does not match. The error page shows the exact URI sent — register that, remembering the manifest workaround for `http://127.0.0.1`. |
| `AADSTS65001` / *Need admin approval* | Admin consent not granted. Grant it once, tenant-wide, then sign in as the user. |
| `AADSTS7000218` | *Allow public client flows* is not enabled on the app registration. |
| `invalid_grant` | The refresh token is expired, revoked, or was issued to a different client. Run `mailferry auth refresh <name>`. |
| `invalid_client` | Wrong client ID or secret, or the registration does not permit this flow. |
| `invalid_scope` / `consent_required` | The IMAP scope was not consented. Re-check the permission and consent. |
| Token succeeds, IMAP `AUTHENTICATIONFAILED` | Almost always IMAP disabled on the mailbox: `Get-CASMailbox -Identity jeslyn@example.com \| fl ImapEnabled` then `Set-CASMailbox -Identity jeslyn@example.com -ImapEnabled $true`. On Google, check IMAP is enabled in Gmail settings. |
| App-only token works, mailbox refuses it | The Exchange service principal is missing or `Add-MailboxPermission` was not applied to that mailbox. |
| Google service account returns `unauthorized_client` | Domain-wide delegation not authorised for that numeric client ID, the scope does not match exactly, or it has not propagated yet. |
| Wrong account authorised | Remove the profile, grant consent separately, retry with `--user` in a private browser window. |

`mailferry auth test <name>` narrows almost all of these to a single stage.
Use `--trace` on a migration run for the wire protocol — credentials are
redacted and message bodies are never written to traces.

---

## Security and least privilege

- **Grant only the mailboxes you are migrating.** For Microsoft app-only,
  `Add-MailboxPermission` per mailbox rather than tenant-wide access. For
  Google, authorise only the scope you need.
- **Remove access when the migration finishes.** Delete the app registration
  or service account, or revoke the grant. `mailferry auth remove` deletes
  only MailFerry's *local* profile and stored secret — it cannot revoke
  anything at the provider, and says so.
- **Never put a secret in configuration.** MailFerry rejects a literal
  secret in `mailferry.toml`. Client secrets, refresh tokens and service
  account keys are held as references (`env:`, `file:`, `keychain:`,
  `dpapi:`, `cmd:`) resolved at run time and never written to the State
  Database, logs, reports or traces.
- **Prefer the OS secret store.** `auth add` uses the macOS login keychain,
  the Linux Secret Service, or Windows DPAPI. Where no secret service exists
  it falls back to a `0600` file and tells you so; the documented
  alternative is an external secret manager via `cmd:`.
- **Protect the CSV.** It may contain destination passwords. `chmod 600`.
- **Use a dedicated app registration per engagement** if you are an MSP, so
  revocation is surgical.
- **Delegated is least privilege for a small migration**; application and
  service-account models are more powerful and correspondingly more
  dangerous. Choose deliberately.
