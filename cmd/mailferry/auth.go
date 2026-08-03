// MailFerry — IMAP Migration & Sync
// High-Performance Native IMAP Migration Engine
//
// Copyright (C) 2026 Andy Saputra
// Author: Andy Saputra <andy@saputra.org>
// SPDX-License-Identifier: AGPL-3.0-or-later
//
// This file is part of MailFerry (https://github.com/ajsap/mailferry).
// Licensed under the GNU Affero General Public License v3.0 or later;
// see the LICENSE file for details.

// The `mailferry auth` command family — a first-class OAuth authentication
// lifecycle. Named profiles are the operator abstraction: authorise once,
// then reference the profile from any CSV row (source or destination,
// independently) via an "oauth:<name>" password cell. Secrets never touch
// the CSV, mailferry.toml, the State Database, logs or reports — the profile
// stores only a reference into the OS secret store (or a documented
// fallback).
//
// The interactive experience is deliberately modelled on modern CLI sign-in
// tools: MailFerry opens the browser itself, always shows a copyable URL in
// case it cannot, reports waiting/success/cancellation/failure plainly, and
// finishes by naming the account that was actually authorised. An operator
// never copies an access token by hand.
package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/ajsap/mailferry/v2/internal/config"
	"github.com/ajsap/mailferry/v2/internal/identity"
	"github.com/ajsap/mailferry/v2/internal/imapx"
	"github.com/ajsap/mailferry/v2/internal/oauth"
	"github.com/ajsap/mailferry/v2/internal/progress"
	"github.com/ajsap/mailferry/v2/internal/util"
	"golang.org/x/term"
)

// providerAlias maps the `auth add <provider>` argument to a provider family.
var providerAlias = map[string]string{
	"microsoft365": "microsoft", "microsoft": "microsoft", "m365": "microsoft",
	"office365": "microsoft", "o365": "microsoft", "entra": "microsoft",
	"google": "google", "gmail": "google", "workspace": "google",
	"generic": "generic", "custom": "generic",
}

// providerTitle is the operator-facing product name for a provider family.
var providerTitle = map[string]string{
	"microsoft": "Microsoft 365",
	"google":    "Google Workspace / Gmail",
	"generic":   "Generic OAuth provider",
}

// providerIMAP is the officially documented IMAP endpoint per provider.
var providerIMAP = map[string]struct {
	Host string
	Port int
}{
	"microsoft": {"outlook.office365.com", 993},
	"google":    {"imap.gmail.com", 993},
}

// kindTitle describes each authentication model in operator language. The
// delegated/administrative distinction is deliberately unmissable.
var kindTitle = map[string]string{
	"delegated":       "Delegated — one user's own mailbox (interactive sign-in)",
	"application":     "Application — Microsoft 365 app-only, one app → many mailboxes",
	"service_account": "Service account — Google domain-wide delegation → many mailboxes",
}

func cmdAuth(rest []string) int {
	if len(rest) < 1 {
		authUsage()
		return 2
	}
	sub, args := rest[0], rest[1:]
	switch sub {
	case "add":
		return authAdd(args)
	case "list", "ls":
		return authList()
	case "show":
		return authShow(args)
	case "test":
		return authTest(args)
	case "refresh", "reauth":
		return authRefresh(args)
	case "remove", "rm", "delete":
		return authRemove(args)
	default:
		authUsage()
		return 2
	}
}

func authUsage() {
	fmt.Println(identity.BannerLine())
	fmt.Println(`
mailferry auth — OAuth 2.0 authentication profiles

  auth add <provider> --name NAME [flags]   authorise and save a profile
  auth list                                 list saved profiles (no secrets)
  auth show NAME                            show one profile (no secrets)
  auth test NAME [--user MAILBOX]           get a token and try a real IMAP login
  auth refresh NAME                         re-authorise a delegated profile
  auth remove NAME                          delete the profile + stored secret

Providers: microsoft365 | google | generic

Authentication models (--kind):
  delegated        a user's own mailbox (interactive browser/device sign-in)
  application      Microsoft 365 app-only: one tenant app -> many mailboxes
  service_account  Google Workspace domain-wide delegation -> many mailboxes

Headless / SSH (no graphical browser on this host):
  --no-browser     Microsoft: device code on any other device
                   Google/generic: open the printed URL on your own machine,
                   then paste the address the browser was redirected to

Naming the mailbox (recommended for delegated profiles):
  --user MAILBOX   pre-selects that account at the sign-in page and refuses
                   to save the profile if a different one signs in — a
                   browser still signed in as an administrator is the usual
                   way an operator ends up authorising the wrong mailbox

Examples:
  mailferry auth add microsoft365 --name client-a --client-id <app-guid> --tenant <tenant> \
      --user jeslyn@example.com
  mailferry auth add google --name client-b --client-id <id> --client-secret env:GID_SECRET
  mailferry auth add microsoft365 --name tenant-bulk --kind application \
      --client-id <app-guid> --tenant <tenant> --client-secret keychain:mailferry/tenant-bulk
  mailferry auth add google --name ws-bulk --kind service_account --key-json file:/path/sa.json
  Reference a profile from a CSV password cell:  oauth:client-a`)
}

// credStore returns the credential store rooted beside the config file (so
// portable mode keeps credentials with the portable config).
func credStore() *oauth.CredStore {
	return oauth.NewCredStore(filepath.Join(filepath.Dir(bootPath), "credentials"))
}

func loadProfilesOrEmpty() map[string]config.OAuthProfile {
	profiles, warns, err := config.LoadOAuthProfiles(bootPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, progress.C("note: "+err.Error(), "yellow"))
		return map[string]config.OAuthProfile{}
	}
	for _, w := range warns {
		fmt.Fprintln(os.Stderr, progress.C("note: "+w, "yellow"))
	}
	return profiles
}

// field prints one aligned "Label  value" line of the auth summary blocks.
func field(label, value string) {
	if value != "" {
		fmt.Printf("  %-12s %s\n", label, value)
	}
}

func authAdd(args []string) int {
	fs := flag.NewFlagSet("auth add", flag.ExitOnError)
	var (
		name       = fs.String("name", "", "profile name (referenced as oauth:<name> in the CSV)")
		kind       = fs.String("kind", "delegated", "delegated | application | service_account")
		clientID   = fs.String("client-id", "", "OAuth application (client) id")
		tenant     = fs.String("tenant", "", "Microsoft tenant (GUID, domain, or 'common')")
		clientSec  = fs.String("client-secret", "", "client secret reference (env:/file:/keychain:/dpapi:/cmd:), or omit to be prompted")
		keyJSON    = fs.String("key-json", "", "service account key: a file path to import, or a reference")
		tokenURL   = fs.String("token-url", "", "token endpoint (generic provider)")
		scopes     = fs.String("scopes", "", "space-separated scope override")
		user       = fs.String("user", "", "the mailbox to authorise — pre-selects it at the sign-in page and is verified afterwards")
		noBrowser  = fs.Bool("no-browser", false, "headless: device code (Microsoft) or copy/paste redirect (Google)")
		mechanism  = fs.String("mechanism", "", "SASL mechanism override (XOAUTH2 | OAUTHBEARER)")
		timeoutMin = fs.Int("timeout", 10, "minutes to wait for interactive authorisation")
	)
	fs.Parse(reorderArgs(fs, args)) // allow the provider before the flags
	if fs.NArg() < 1 || *name == "" {
		fmt.Fprintln(os.Stderr, "usage: mailferry auth add <provider> --name NAME [flags]")
		return 2
	}
	prov, ok := providerAlias[strings.ToLower(fs.Arg(0))]
	if !ok {
		fmt.Fprintf(os.Stderr, "unknown provider %q (microsoft365 | google | generic)\n", fs.Arg(0))
		return 2
	}
	if existing := loadProfilesOrEmpty(); existing[*name].Name != "" {
		fmt.Fprintf(os.Stderr, "a profile named %q already exists — use `auth refresh %s` or `auth remove %s` first\n",
			*name, *name, *name)
		return 1
	}
	if !knownKind(*kind) {
		fmt.Fprintf(os.Stderr, "unknown --kind %q (delegated | application | service_account)\n", *kind)
		return 2
	}

	prof := config.OAuthProfile{
		Name: *name, Provider: prov, Kind: *kind, Tenant: *tenant, ClientID: *clientID,
		Mechanism: *mechanism, ClientSecret: *clientSec, KeyJSON: *keyJSON, TokenURL: *tokenURL,
	}
	if *scopes != "" {
		prof.Scopes = strings.Fields(*scopes)
	}

	fmt.Println(identity.BannerLine())
	fmt.Println()
	field("Provider", providerTitle[prov])
	field("Profile", *name)
	field("Model", kindTitle[effKind(*kind)])
	fmt.Println()

	// application / service_account are administrative models: no interactive
	// sign-in exists for them by design. MailFerry takes the credential the
	// tenant/Workspace administrator issued and stores it securely.
	if *kind == "application" || *kind == "service_account" {
		if err := prepareAdminCredential(&prof); err != nil {
			fmt.Fprintln(os.Stderr, progress.C("ERROR: "+err.Error(), "red"))
			return 2
		}
		if err := prof.Validate(); err != nil {
			fmt.Fprintln(os.Stderr, "ERROR:", err)
			return 2
		}
		if err := config.WriteOAuthProfile(bootPath, prof); err != nil {
			fmt.Fprintln(os.Stderr, "ERROR:", err)
			return 1
		}
		fmt.Println(progress.C("✓ Administrative profile saved", "green"))
		field("Profile", prof.Name)
		field("Provider", providerTitle[prov])
		field("Model", kindTitle[effKind(*kind)])
		fmt.Println()
		fmt.Printf("  This ONE profile authenticates every mailbox your %s\n",
			map[string]string{"application": "tenant administrator", "service_account": "Workspace administrator"}[*kind])
		fmt.Println("  has authorised — you do not authorise mailboxes one by one.")
		fmt.Println()
		fmt.Printf("  Verify it:  mailferry auth test %s --user <mailbox>\n", prof.Name)
		printProfileUsage(prof)
		return 0
	}

	// delegated: run the interactive authorisation flow.
	if *clientID == "" {
		fmt.Fprintln(os.Stderr, "ERROR: --client-id is required (register an OAuth app with your provider)")
		return 2
	}
	src, err := oauth.NewAuthSource(oauth.FromConfig(prof))
	if err != nil {
		fmt.Fprintln(os.Stderr, "ERROR:", err)
		return 2
	}
	src.SetLoginHint(*user)
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(*timeoutMin)*time.Minute)
	defer cancel()

	res, err := runInteractiveAuth(ctx, src, prov, *noBrowser)
	if err != nil {
		reportAuthFailure(err)
		return 1
	}
	if !reportAccountMatch(src, res) {
		return 1
	}
	prof.Account = res.Account

	// Store the refresh token securely; the profile keeps only the reference.
	ref, backend, serr := credStore().Store(*name, res.RefreshToken)
	if serr != nil {
		fmt.Fprintln(os.Stderr, progress.C("ERROR: could not store the credential: "+serr.Error(), "red"))
		return 1
	}
	prof.RefreshToken = ref
	prof.AuthorisedAt = time.Now().UTC().Format(time.RFC3339)
	if err := config.WriteOAuthProfile(bootPath, prof); err != nil {
		fmt.Fprintln(os.Stderr, "ERROR:", err)
		return 1
	}
	printAuthorised(prof, backend)
	printProfileUsage(prof)
	return 0
}

// printAuthorised is the success block a modern sign-in tool would print.
func printAuthorised(p config.OAuthProfile, backend oauth.Backend) {
	fmt.Println()
	fmt.Println(progress.C("✓ Authorised", "green"))
	field("Profile", p.Name)
	field("Provider", providerTitle[p.Provider])
	field("Account", p.Account)
	fmt.Println()
	fmt.Printf("  Credential stored in %s.\n", backend.Description())
	fmt.Println("  mailferry.toml holds only a reference — never the token itself.")
}

// reportAccountMatch refuses to save a profile that authorised a DIFFERENT
// account from the one the operator named with --user.
//
// This is the trap the flag exists for: a delegated token belongs to
// whoever signed in, and a browser still holding an administrator's session
// (from granting consent, say) will be reused silently by single sign-on. A
// profile bound to the wrong mailbox would then migrate the wrong mail, so
// nothing is written and the operator is told exactly how to retry.
func reportAccountMatch(src *oauth.TokenSource, res oauth.AuthResult) bool {
	ok, want, got := src.AuthorisedAs(res)
	if ok {
		if want != "" && got == "" {
			fmt.Println(progress.C("note: the provider did not identify the signed-in account, "+
				"so MailFerry could not confirm it is "+want+".", "yellow"))
		}
		return true
	}
	fmt.Fprintln(os.Stderr)
	fmt.Fprintln(os.Stderr, progress.C("✗ Wrong account — nothing was saved.", "red"))
	fmt.Fprintf(os.Stderr, "  You asked for : %s\n", want)
	fmt.Fprintf(os.Stderr, "  You signed in : %s\n", got)
	fmt.Fprintln(os.Stderr)
	fmt.Fprintln(os.Stderr, "  A delegated profile authenticates the mailbox that signed in, so this")
	fmt.Fprintln(os.Stderr, "  one would have migrated the wrong mailbox. Your browser is probably")
	fmt.Fprintln(os.Stderr, "  still signed in as the other account.")
	fmt.Fprintln(os.Stderr, "  Retry in a private/incognito window, or sign out of the provider first.")
	return false
}

// reportAuthFailure distinguishes the outcomes an operator cares about:
// cancellation (theirs or the timeout) from a genuine failure.
func reportAuthFailure(err error) {
	switch {
	case errors.Is(err, context.Canceled):
		fmt.Fprintln(os.Stderr, progress.C("✗ Cancelled — nothing was saved.", "yellow"))
	case errors.Is(err, context.DeadlineExceeded):
		fmt.Fprintln(os.Stderr, progress.C("✗ Timed out waiting for authorisation — nothing was saved.", "yellow"))
		fmt.Fprintln(os.Stderr, "  Re-run the command, or raise the wait with --timeout <minutes>.")
	default:
		fmt.Fprintln(os.Stderr, progress.C("✗ Authorisation failed: "+err.Error(), "red"))
	}
}

// prepareAdminCredential completes an administrative profile's credential:
// a client secret typed at a no-echo prompt is stored in the secret store,
// and a --key-json given as a plain FILE PATH is imported into the store so
// the service-account key never has to sit in a hand-managed location.
func prepareAdminCredential(p *config.OAuthProfile) error {
	if p.Kind == "application" && p.ClientSecret == "" {
		secret, err := promptSecret("Client secret (input hidden): ")
		if err != nil {
			return err
		}
		if secret == "" {
			return errors.New("a client secret is required for an application profile " +
				"(supply it at the prompt or as --client-secret env:NAME)")
		}
		ref, backend, serr := credStore().Store(p.Name, secret)
		if serr != nil {
			return serr
		}
		p.ClientSecret = ref
		fmt.Printf("  Client secret stored in %s.\n\n", backend.Description())
	}
	if p.Kind == "service_account" && p.KeyJSON != "" && !isSecretReference(p.KeyJSON) {
		data, err := os.ReadFile(p.KeyJSON)
		if err != nil {
			return fmt.Errorf("--key-json %q is neither a secret reference nor a readable file: %w",
				p.KeyJSON, err)
		}
		ref, backend, serr := credStore().Store(p.Name+"-key", string(data))
		if serr != nil {
			return serr
		}
		p.KeyJSON = ref
		fmt.Printf("  Service account key imported into %s.\n\n", backend.Description())
	}
	return nil
}

// promptSecret reads a secret from the terminal WITHOUT echoing it. On a
// non-terminal stdin (a script or a pipe) it refuses rather than silently
// reading a secret from a place the operator may not have intended.
func promptSecret(prompt string) (string, error) {
	fd := int(os.Stdin.Fd())
	if !term.IsTerminal(fd) {
		return "", errors.New("stdin is not a terminal — pass the credential as an " +
			"external reference instead (e.g. --client-secret env:MY_SECRET)")
	}
	fmt.Print("  " + prompt)
	b, err := term.ReadPassword(fd)
	fmt.Println()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(b)), nil
}

// isSecretReference mirrors the accepted external-reference schemes.
func isSecretReference(s string) bool {
	for _, scheme := range []string{"env:", "file:", "keychain:", "dpapi:", "cmd:"} {
		if strings.HasPrefix(s, scheme) {
			return true
		}
	}
	return false
}

func knownKind(k string) bool {
	switch k {
	case "", "delegated", "application", "service_account":
		return true
	}
	return false
}

func effKind(k string) string {
	if k == "" {
		return "delegated"
	}
	return k
}

// runInteractiveAuth chooses the flow: browser loopback by default; headless
// gets device code (Microsoft) or the printed-URL + pasted-redirect path
// (Google/generic, whose mail scope has no officially supported device flow).
func runInteractiveAuth(ctx context.Context, src *oauth.TokenSource, prov string,
	noBrowser bool) (oauth.AuthResult, error) {
	showURL := func(u string) {
		fmt.Println()
		fmt.Println("Browser didn't open?")
		fmt.Println("Open this URL to continue:")
		fmt.Println()
		fmt.Println("  " + u)
		fmt.Println()
	}

	if noBrowser {
		if prov == "microsoft" {
			da, err := src.StartDeviceAuth(ctx)
			if err != nil {
				return oauth.AuthResult{}, err
			}
			uri := da.VerificationURI
			if da.VerificationURIComplete != "" {
				uri = da.VerificationURIComplete
			}
			fmt.Println("This host has no browser — use any other device.")
			fmt.Println()
			field("Open", uri)
			field("Code", da.UserCode)
			fmt.Println()
			fmt.Println("Waiting for authorisation...")
			return src.PollDeviceAuth(ctx, da)
		}
		// Google/generic headless. Still the official loopback + PKCE flow —
		// only the transport of the authorisation code changes.
		fmt.Println("This host has no browser. Open the URL below on your own machine.")
		fmt.Println("The browser will then fail to reach a 127.0.0.1 address — that is")
		fmt.Println("expected. Copy the WHOLE address it tried to open and paste it here.")
		src.SetManualCode(readRedirectPaste)
		return src.LoopbackAuthorize(ctx, nil, func(u string) {
			fmt.Println()
			fmt.Println("  " + u)
			fmt.Println()
			fmt.Print("Paste the redirect address: ")
		})
	}

	// Interactive: open the browser, degrade to a printed URL if that fails.
	fmt.Println("Opening your browser to sign in...")
	res, err := src.LoopbackAuthorize(ctx, func(u string) error {
		err := oauth.OpenBrowser(u)
		if err == nil {
			// Always show the URL as well: a browser that "opened" may have
			// landed in the wrong profile or a background window.
			fmt.Println()
			fmt.Println("Not the right browser or nothing happened?")
			fmt.Println("Open this URL to continue:")
			fmt.Println()
			fmt.Println("  " + u)
			fmt.Println()
			fmt.Println("Waiting for authorisation...")
		}
		return err
	}, func(u string) {
		showURL(u)
		fmt.Println("Waiting for authorisation...")
	})
	return res, err
}

// readRedirectPaste reads the redirect address (or a bare code) the operator
// pastes on the headless path. It honours ctx so the wait is cancellable.
func readRedirectPaste(ctx context.Context) (string, error) {
	lines := make(chan string, 1)
	go func() {
		sc := bufio.NewScanner(os.Stdin)
		if sc.Scan() {
			lines <- strings.TrimSpace(sc.Text())
			return
		}
		close(lines)
	}()
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case l, ok := <-lines:
		if !ok {
			return "", errors.New("no redirect address was pasted")
		}
		return l, nil
	}
}

func printProfileUsage(p config.OAuthProfile) {
	fmt.Println()
	fmt.Printf("  Use it in a CSV password cell:  oauth:%s\n", p.Name)
	if ep, ok := providerIMAP[p.Provider]; ok {
		fmt.Printf("  IMAP host:                      %s:%d\n", ep.Host, ep.Port)
	}
}

func authList() int {
	profiles := loadProfilesOrEmpty()
	if len(profiles) == 0 {
		fmt.Println("No OAuth profiles. Add one with:  mailferry auth add <provider> --name NAME")
		return 0
	}
	names := make([]string, 0, len(profiles))
	for n := range profiles {
		names = append(names, n)
	}
	sort.Strings(names)
	fmt.Printf("%-16s %-10s %-16s %-30s %s\n",
		"NAME", "PROVIDER", "MODEL", "ACCOUNT", "AUTHORISED")
	for _, n := range names {
		p := profiles[n]
		account := p.Account
		if account == "" && effKind(p.Kind) != "delegated" {
			account = "(any authorised mailbox)"
		}
		fmt.Printf("%-16s %-10s %-16s %-30s %s\n", n, p.Provider, effKind(p.Kind),
			clipS(account, 30), shortTimestamp(p.AuthorisedAt))
	}
	return 0
}

// shortTimestamp renders a stored RFC 3339 time as a compact date for the
// list view, or a dash when the profile predates the field.
func shortTimestamp(s string) string {
	if s == "" {
		return "-"
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return s
	}
	return t.Local().Format("2006-01-02 15:04")
}

func authShow(args []string) int {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "usage: mailferry auth show NAME")
		return 2
	}
	p, ok := loadProfilesOrEmpty()[args[0]]
	if !ok {
		fmt.Fprintf(os.Stderr, "no profile named %q\n", args[0])
		return 1
	}
	fmt.Printf("Profile      %s\n", p.Name)
	fmt.Printf("Provider     %s\n", providerTitle[p.Provider])
	fmt.Printf("Model        %s\n", kindTitle[effKind(p.Kind)])
	if p.Account != "" {
		fmt.Printf("Account      %s\n", p.Account)
	}
	fmt.Printf("Authorised   %s\n", humanTimestamp(p.AuthorisedAt))
	if p.Tenant != "" {
		fmt.Printf("Tenant       %s\n", p.Tenant)
	}
	if p.ClientID != "" {
		fmt.Printf("Client ID    %s\n", p.ClientID)
	}
	if p.Mechanism != "" {
		fmt.Printf("Mechanism    %s\n", p.Mechanism)
	}
	if len(p.Scopes) > 0 {
		fmt.Printf("Scopes       %s\n", strings.Join(p.Scopes, " "))
	}
	// Credential REFERENCES only — never the secret value. Sorted so the
	// output is stable rather than map-order random.
	type refRow struct{ label, ref string }
	rows := []refRow{
		{"Client secret", p.ClientSecret}, {"Refresh token", p.RefreshToken},
		{"Access token", p.AccessToken}, {"Key JSON", p.KeyJSON},
	}
	for _, r := range rows {
		if r.ref != "" {
			fmt.Printf("%-12s %s  %s\n", r.label, r.ref,
				progress.C("(reference — the secret is not shown)", "cyan"))
		}
	}
	return 0
}

// step prints a diagnostic stage label and returns closures to mark it. The
// staged form matters: when something fails, the operator can see EXACTLY
// how far the chain got — OAuth, TLS, SASL or IMAP itself — which is the
// whole difference between "it doesn't work" and a fixable diagnosis.
func step(label string) (done func(detail string), fail func(err error)) {
	fmt.Printf("  %-34s", label)
	return func(detail string) {
			fmt.Print(progress.C("✓", "green"))
			if detail != "" {
				fmt.Print("  " + progress.C(detail, "dim"))
			}
			fmt.Println()
		}, func(err error) {
			fmt.Println(progress.C("✗", "red"))
			fmt.Fprintln(os.Stderr, "     "+progress.C(err.Error(), "red"))
		}
}

func authTest(args []string) int {
	fs := flag.NewFlagSet("auth test", flag.ExitOnError)
	user := fs.String("user", "", "mailbox to authenticate (required for application/service_account)")
	noIMAP := fs.Bool("no-imap", false, "only mint a token; do not attempt an IMAP login")
	host := fs.String("host", "", "IMAP host to test against (default: the provider's documented host)")
	port := fs.Int("port", 0, "IMAP port (default 993)")
	fs.Parse(reorderArgs(fs, args))
	if fs.NArg() < 1 {
		fmt.Fprintln(os.Stderr, "usage: mailferry auth test NAME [--user MAILBOX]")
		return 2
	}
	p, ok := loadProfilesOrEmpty()[fs.Arg(0)]
	if !ok {
		fmt.Fprintf(os.Stderr, "no profile named %q\n", fs.Arg(0))
		return 1
	}
	mailbox := *user
	if mailbox == "" {
		mailbox = p.Account
	}
	if mailbox == "" && effKind(p.Kind) != "delegated" {
		fmt.Fprintf(os.Stderr, "ERROR: %s profiles authenticate a specific mailbox — "+
			"pass --user <mailbox>\n", effKind(p.Kind))
		return 2
	}

	// ---- profile summary (non-secret metadata only) ----------------------
	fmt.Println()
	field("Profile", p.Name)
	field("Provider", providerTitle[p.Provider])
	field("Model", kindTitle[effKind(p.Kind)])
	field("Mailbox", mailbox)
	if p.Tenant != "" {
		field("Tenant", p.Tenant)
	}
	field("Authorised", humanTimestamp(p.AuthorisedAt))
	fmt.Println()

	ts, err := oauth.NewTokenSource(oauth.FromConfig(p), mailbox)
	if err != nil {
		fmt.Fprintln(os.Stderr, progress.C("ERROR: "+err.Error(), "red"))
		return 1
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// ---- 1. token --------------------------------------------------------
	ok1, fail1 := step("Obtaining access token")
	tok, err := ts.Token(ctx)
	if err != nil {
		fail1(err)
		return 1
	}
	// Prove success WITHOUT revealing the token: its length and lifetime say
	// everything an operator needs and disclose nothing usable.
	detail := fmt.Sprintf("%s · %d bytes (not shown)", ts.Mechanism(), len(tok))
	if exp := ts.Expiry(); !exp.IsZero() {
		detail += fmt.Sprintf(" · expires in %s", util.FmtDHMS(time.Until(exp).Seconds()))
	}
	if ts.Static() {
		detail += " · pre-provisioned, cannot refresh"
	}
	ok1(detail)

	if *noIMAP {
		fmt.Println()
		fmt.Println(progress.C("Token only (--no-imap): the IMAP session was not tested.", "cyan"))
		return 0
	}

	h, pt := *host, *port
	if h == "" {
		ep, known := providerIMAP[p.Provider]
		if !known {
			fmt.Println()
			fmt.Println(progress.C("note: no documented IMAP host for this provider — "+
				"pass --host to test the IMAP session too.", "cyan"))
			return 0
		}
		h, pt = ep.Host, ep.Port
	}
	if pt == 0 {
		pt = 993
	}
	if mailbox == "" {
		fmt.Println()
		fmt.Println(progress.C("note: the profile has no recorded account — "+
			"pass --user <mailbox> to test the IMAP session too.", "cyan"))
		return 0
	}

	// ---- 2..4. connect, authenticate, list -------------------------------
	c := imapx.NewClient(imapx.Endpoint{Host: h, Port: pt, Security: "ssl", User: mailbox},
		30*time.Second, true, nil, "auth test", nil)
	c.ReadOnly = true // belt and braces: this probe may never mutate anything
	c.Auth = ts.SASLAuthFor(mailbox)

	ok2, fail2 := step(fmt.Sprintf("Connecting imaps://%s:%d", h, pt))
	if err := c.Connect(); err != nil {
		fail2(err)
		return 1
	}
	ok2("TLS established")
	defer c.Logout(5 * time.Second)

	ok3, fail3 := step("Authenticating SASL " + ts.Mechanism())
	if err := c.Login(); err != nil {
		fail3(err)
		fmt.Fprintln(os.Stderr)
		fmt.Fprintln(os.Stderr, progress.C(imapDiagnosisHint(p.Provider, err), "yellow"))
		return 1
	}
	ok3("mailbox authenticated")

	ok4, fail4 := step(`Listing folders (LIST "" "*")`)
	folders, err := c.ListAll()
	if err != nil {
		fail4(err)
		return 1
	}
	ok4(fmt.Sprintf("%d folder(s) visible", len(folders)))

	// ---- capabilities ----------------------------------------------------
	caps := c.CapabilityList()
	fmt.Println()
	fmt.Println(progress.C("Server capabilities", "cyan") +
		progress.C(fmt.Sprintf("   (%d advertised)", len(caps)), "dim"))
	printCapabilityColumns(caps)

	fmt.Println()
	printCapabilityVerdict(c)
	return 0
}

// printCapabilityColumns lays the advertised capabilities out in columns so
// a long list stays readable instead of scrolling off the screen.
func printCapabilityColumns(caps []string) {
	const cols = 4
	width := 0
	for _, c := range caps {
		if len(c) > width {
			width = len(c)
		}
	}
	for i, c := range caps {
		fmt.Printf("  %-*s", width+2, c)
		if i%cols == cols-1 {
			fmt.Println()
		}
	}
	if len(caps)%cols != 0 {
		fmt.Println()
	}
}

// printCapabilityVerdict translates the capability list into the handful of
// statements that actually affect a MailFerry migration to this server.
func printCapabilityVerdict(c *imapx.Client) {
	say := func(name, yes, no string) {
		if c.Has(name) {
			fmt.Printf("  %-14s %s\n", name, progress.C(yes, "green"))
			return
		}
		fmt.Printf("  %-14s %s\n", name, progress.C(no, "dim"))
	}
	say("MULTIAPPEND", "supported — appends to this server will be batched",
		"not advertised — one append per message (no action needed)")
	say("UIDPLUS", "supported — APPENDUID confirms every copy",
		"not advertised — copies are confirmed by destination probe instead")
	say("LITERAL+", "supported — uploads skip a round trip per literal",
		"not advertised — literals use synchronising continuations")
	say("MOVE", "supported", "not advertised — dedup quarantine uses COPY + flag")
	say("CONDSTORE", "supported", "not advertised")
	say("QRESYNC", "supported — usable by planned delta sync (not yet implemented)",
		"not advertised")
}

// imapDiagnosisHint turns a failed IMAP authentication into the check an
// operator should actually make first, per provider.
func imapDiagnosisHint(provider string, err error) string {
	switch provider {
	case "microsoft":
		return "The token was valid but the mailbox refused it. The usual cause is IMAP\n" +
			"being disabled for this mailbox in Exchange Online:\n" +
			"    Get-CASMailbox -Identity <mailbox> | fl ImapEnabled\n" +
			"    Set-CASMailbox -Identity <mailbox> -ImapEnabled $true\n" +
			"Also confirm the app has the Office 365 Exchange Online permission\n" +
			"IMAP.AccessAsUser.All (delegated) or IMAP.AccessAsApp (application)."
	case "google":
		return "The token was valid but the mailbox refused it. Check that IMAP is enabled\n" +
			"in Gmail settings, and that the https://mail.google.com/ scope was granted\n" +
			"(for a service account, that it is authorised for domain-wide delegation)."
	default:
		return "The token was valid but the server refused it — check the account name and\n" +
			"that the server accepts this SASL mechanism."
	}
}

// humanTimestamp renders a stored RFC 3339 timestamp for display, or a dash.
func humanTimestamp(s string) string {
	if s == "" {
		return "-"
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return s
	}
	return t.Local().Format("2006-01-02 15:04 MST") +
		fmt.Sprintf(" (%s ago)", util.FmtDHMS(time.Since(t).Seconds()))
}

func authRefresh(args []string) int {
	fs := flag.NewFlagSet("auth refresh", flag.ExitOnError)
	noBrowser := fs.Bool("no-browser", false, "headless: device code (Microsoft) or copy/paste redirect (Google)")
	user := fs.String("user", "", "override the mailbox to re-authorise (default: the profile's recorded account)")
	fs.Parse(reorderArgs(fs, args))
	if fs.NArg() < 1 {
		fmt.Fprintln(os.Stderr, "usage: mailferry auth refresh NAME [--no-browser]")
		return 2
	}
	name := fs.Arg(0)
	p, ok := loadProfilesOrEmpty()[name]
	if !ok {
		fmt.Fprintf(os.Stderr, "no profile named %q\n", name)
		return 1
	}
	if p.Kind == "application" || p.Kind == "service_account" {
		fmt.Println("This is an administrative profile — it mints tokens on demand from the")
		fmt.Println("credential your administrator issued, so there is nothing to re-authorise.")
		fmt.Printf("Verify it any time with:  mailferry auth test %s --user <mailbox>\n", name)
		return 0
	}
	// Re-run the interactive flow and replace the stored refresh token.
	src, err := oauth.NewAuthSource(oauth.FromConfig(p))
	if err != nil {
		fmt.Fprintln(os.Stderr, "ERROR:", err)
		return 1
	}
	// Re-authorising must land on the SAME mailbox the profile already names,
	// so the recorded account becomes the hint (and the check) unless the
	// operator deliberately overrides it.
	hint := *user
	if hint == "" {
		hint = p.Account
	}
	src.SetLoginHint(hint)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	fmt.Println(identity.BannerLine())
	fmt.Println()
	field("Provider", providerTitle[p.Provider])
	field("Profile", name)
	field("Account", hint)
	fmt.Println()
	res, err := runInteractiveAuth(ctx, src, p.Provider, *noBrowser)
	if err != nil {
		reportAuthFailure(err)
		return 1
	}
	if !reportAccountMatch(src, res) {
		return 1
	}
	ref, backend, serr := credStore().Store(name, res.RefreshToken)
	if serr != nil {
		fmt.Fprintln(os.Stderr, "ERROR:", serr)
		return 1
	}
	if res.Account != "" {
		p.Account = res.Account
	}
	p.RefreshToken = ref
	p.AuthorisedAt = time.Now().UTC().Format(time.RFC3339)
	if err := config.WriteOAuthProfile(bootPath, p); err != nil {
		fmt.Fprintln(os.Stderr, "ERROR:", err)
		return 1
	}
	fmt.Println()
	fmt.Println(progress.C("✓ Re-authorised", "green"))
	field("Profile", p.Name)
	field("Account", p.Account)
	fmt.Printf("\n  Refresh token replaced in %s.\n", backend.Description())
	return 0
}

func authRemove(args []string) int {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "usage: mailferry auth remove NAME")
		return 2
	}
	name := args[0]
	if _, ok := loadProfilesOrEmpty()[name]; !ok {
		fmt.Fprintf(os.Stderr, "no profile named %q\n", name)
		return 1
	}
	if err := config.DeleteOAuthProfile(bootPath, name); err != nil {
		fmt.Fprintln(os.Stderr, "ERROR:", err)
		return 1
	}
	if err := credStore().Delete(name); err != nil {
		fmt.Fprintln(os.Stderr, progress.C("note: profile removed, but the stored secret could not be deleted: "+
			err.Error(), "yellow"))
	}
	credStore().Delete(name + "-key") // service-account key, when one was imported
	fmt.Printf("Removed profile %q and its locally stored credential.\n", name)
	fmt.Println(progress.C("Note: this deletes the LOCAL profile and secret only. To revoke access at the "+
		"provider, remove the app's consent/grant in the Microsoft or Google admin console.", "cyan"))
	return 0
}
