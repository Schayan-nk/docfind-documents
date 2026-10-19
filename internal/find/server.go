package find

import (
	"crypto/subtle"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Schayan-nk/docfind-documents/internal/audit"
	"github.com/Schayan-nk/docfind-documents/internal/auth"
	"github.com/Schayan-nk/docfind-documents/internal/dependencies"
	"github.com/Schayan-nk/docfind-documents/internal/docxhtml"
	"github.com/Schayan-nk/docfind-documents/internal/obs"
	"github.com/Schayan-nk/docfind-documents/internal/ooxml"
	"github.com/Schayan-nk/docfind-documents/internal/webauthn"
)

// The archive page: the folder tree, the search and the document pane.
//
//go:embed ui.html
var uiHTML []byte

// The document pane renders with docx-preview (client-side, in the browser
// engine) rather than the server: it reproduces headers, footers, footnotes
// and real page geometry, which internal/docxhtml deliberately does not
// attempt. Both libraries ship inside the binary like everything else here,
// so nothing is fetched from another site.
//
//go:embed vendor/docx-preview.min.js
var docxPreviewJS []byte

//go:embed vendor/jszip.min.js
var jszipJS []byte

//go:embed favicon.ico
var faviconICO []byte

// The sign-in page. A document of its own rather than a screen inside
// ui.html, which carries the whole archive interface.
//
//go:embed login.html
var loginHTML []byte

// The page the service worker serves when a navigation cannot reach us.
//
// Served from here as well as cached, so that the worker has something to
// precache in the first place and so that hitting /offline while online shows
// something sensible rather than a 404.
//
//go:embed offline.html
var offlineHTML []byte

//go:embed settings.html
var settingsHTML []byte

// rolesJS edits what each role gets by default (roletemplates.go).
//
//go:embed roles.js
var rolesJS []byte

// The Word editor's page (office.go).
//
//go:embed office.html
var officeHTML []byte

//go:embed office.js
var officeJS []byte

// The thought-orb engine, drawn on a 2D canvas: the loader nav.js shows while
// a page waits on the server.
//
//go:embed vendor/thinking-orbs.js
var thinkingOrbsJS []byte

// The file-upload control and the send button that flies, on the documents
// page. See upload.js for why the shadcn component it is modelled on could
// not simply be installed.
//
//go:embed upload.js
var uploadJS []byte

// Installable-app assets. Without a manifest and an apple-touch-icon, iOS
// Safari's "Add to Home Screen" produces a bookmark with a screenshot for an
// icon rather than something that opens standalone.
//
//go:embed manifest.webmanifest
var manifestJSON []byte

//go:embed sw.js
var serviceWorkerJS []byte

// The template behind the 404 and 500 pages. Both variants are built from it
// once at start-up; see errorpages.go.
//
//go:embed error.html
var errorHTML []byte

// The loading animation: one ring, a sweeping comet and seven orbiting
// particles, in pure CSS and SVG with no script and no image. Kept as its own
// file so exactly one copy exists -- the server injects it into every page,
// rather than it being written into each page for the first paint and again
// into nav.js for later waits. Styled by .ld in app.css.
//
//go:embed loader.html
var loaderHTML []byte

//go:embed icon-192.png
var icon192PNG []byte

//go:embed icon-512.png
var icon512PNG []byte

//go:embed apple-touch-icon.png
var appleTouchIconPNG []byte

// The maskable variant, which the manifest offers alongside the others.
//
// A launcher is entitled to crop a maskable icon to the inner circle -- about
// 80% of the side -- so this one carries the mark at a smaller scale with the
// margin that crop needs. The same artwork in the "any" icons would look
// needlessly small, which is why there are two rather than one.
//
//go:embed icon-maskable.png
var iconMaskablePNG []byte

// The same mark again at header size.
//
//go:embed icon.png
var iconPNG []byte

// The shared stylesheet. Served as its own file rather than inlined into each
// page so the browser fetches it once and every page is guaranteed to be
// looking at the same tokens.
//
//go:embed app.css
var appCSS []byte

// The uiverse-derived components (MIT), kept in their own file because they
// are a distinct body of work with its own attribution -- see the header
// there. Served as part of app.css rather than as a second stylesheet: every
// page would otherwise pay for another request, and these rules are not
// optional on any of them.
//
//go:embed uiverse.css
var uiverseCSS []byte

// styleSheet is the two joined, built once at start-up.
var styleSheet = func() []byte {
	out := make([]byte, 0, len(appCSS)+len(uiverseCSS)+1)
	out = append(out, appCSS...)
	out = append(out, '\n')
	return append(out, uiverseCSS...)
}()

// The shared navigation: one menu, defined once, rather than a copy per page
// drifting apart.
//
//go:embed nav.js
var navJS []byte

// datePickJS is the Jalali date picker, on the settings and editor pages.
//
//go:embed datepick.js
var datePickJS []byte

// glassNavJS is the glass dock, nav.js's navigation on a phone or tablet.
//
//go:embed glassnav.js
var glassNavJS []byte

// megaNavJS is the mega menu: on a desk, a group of the glass sidebar opens
// as a panel beside it. nav.js loads it there and nowhere else.
//
//go:embed meganav.js
var megaNavJS []byte

// twoStepJS draws the two-step sign-in card on the settings page.
//
//go:embed twostep.js
var twoStepJS []byte

// passkeysJS is the passkey button on the sign-in page and the passkey card
// on the settings page.
//
//go:embed passkeys.js
var passkeysJS []byte

// The person editor on the settings page's users list (useredit.js).
//
//go:embed useredit.js
var userEditJS []byte

// Server is the archive's HTTP side: its pages, the search and document API,
// the Word editor's doors, accounts and the audit trail.
type Server struct {
	ix *Index
	// office is the Word editor (office.go); nil when it is not configured.
	office *officeState
	token  string

	// rescan coalesces rescanSoon's requests into at most one scan running
	// plus one queued (see docsroutes.go). rescanOnce builds it on first use
	// rather than in a constructor, since most of this package's tests build
	// a Server literal directly rather than through one.
	rescanOnce sync.Once
	rescan     *scanCoalescer

	// latency is the recent response time of every route, for the p50/p95/
	// p99 an administrator sees. Built with the server so every request from
	// the first is counted.
	latency *obs.Latency

	// audit records every change and export (auditroutes.go). Always set: with
	// no database the events still go to the application log.
	audit *audit.Recorder
	// auditStore is where the trail can be searched from; nil without
	// PostgreSQL, and /api/audit then says so.
	auditStore auditStore

	// guard is nil when the server runs without accounts, where the start-up
	// token is the whole access model. It is set once accounts are
	// configured, which is what makes the server safe to expose beyond this
	// machine.
	guard *auth.Guard

	// sms is the SMS panel's settings (smssettings.go), nil when there is
	// nowhere to keep them.
	sms *smsState
	// idem remembers the answers to recent writes by their Idempotency-Key,
	// so a retried send is done once (idempotency.go). In memory, bounded.
	idem idemStore
	// perms is the permission catalogue. Read on every gated request, so it
	// is an atomic pointer rather than a map somebody edits in place: a data
	// race in the access model is the worst kind this application could have.
	perms atomic.Pointer[auth.Catalogue]
	// avatarDir holds the profile pictures, beside the accounts file. Empty
	// when accounts are off, in which case the picture routes answer 404.
	avatarDir string
	// secPolicyStore keeps the sign-in policy (securitypolicy.go); nil leaves
	// the default in force and the security centre's card read-only.
	secPolicyMu    sync.Mutex
	secPolicyStore appSettingsStore
	// roleTplStore keeps the edited role templates (roletemplates.go).
	roleTplStore appSettingsStore
	// quarantine is where uploads are checked before they reach the archive
	// (uploadcheck.go); beside the accounts file.
	quarantine string
	// spentChallenges are second-step challenges already used to sign in,
	// until they expire (twostep.go).
	spentChallenges sync.Map
	// codeMail mails second-step codes, and mailedCodes are the ones
	// outstanding (twostepmail.go).
	codeMail    codeSender
	mailedCodes map[string]*mailedCode
	mailedMu    sync.Mutex
	// passkeyRP is the relying party passkeys are bound to; nil keeps them
	// off. passkeyChallenges are the ceremonies in flight (passkeyroutes.go).
	passkeyRP         *webauthn.Relying
	passkeyChallenges sync.Map
	// deps is every dependency's state and circuit breaker. See
	// internal/dependencies.
	deps *dependencies.Registry
	// pg is PostgreSQL's failure level (1 up, 2 briefly down, 3 offline),
	// what the guard refuses changes by and the banner reads. See pglevel.go.
	pg pgLevel

	mu       sync.Mutex
	setup    string // why the archive is unavailable; empty once it is working
	onlineFn func(root string) error
}

// NewServer is a server for the archive ix, whose pages and API are gated on
// token until UseAccounts turns accounts on.
func NewServer(ix *Index, token string) *Server {
	return &Server{
		ix:      ix,
		token:   token,
		latency: obs.NewLatency(0),
		audit:   audit.NewRecorder(),
	}
}

// UseAccounts turns on user accounts, roles and token authentication.
//
// Until this is called the server behaves exactly as it always has, so the
// desktop use case is unaffected by any of the access control below.
func (s *Server) UseAccounts(users *auth.Store, signer *auth.Signer, trustProxyHeaders bool) {
	s.guard = &auth.Guard{
		Users:             users,
		Signer:            signer,
		Throttle:          auth.NewThrottle(8, 15*time.Minute, 15*time.Minute),
		TrustProxyHeaders: trustProxyHeaders,
	}
	// So the audit trail records the real visitor IP from the trusted front's
	// X-Forwarded-For tail, matching Guard.ClientIP, rather than the proxy's
	// own address for every action.
	audit.SetTrustProxyHeaders(trustProxyHeaders)
}

// UseSessions records where people are signed in, and where their pictures go.
//
// Both live beside the accounts file, because that is already the directory
// this application treats as its private state: it is created 0700 and holds
// the password hashes and the signing key.
func (s *Server) UseSessions(sessions *auth.Sessions, avatarDir string) {
	if s.guard != nil {
		s.guard.Sessions = sessions
	}
	s.avatarDir = avatarDir
}

// NeedsSetup puts the app into its connect screen instead of failing to start.
//
// An unreachable archive used to be fatal, which meant the one screen able to
// fix the problem was the one screen that could not open. Now the window comes
// up either way and asks for what it is missing.
func (s *Server) NeedsSetup(reason string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.setup = reason
}

// OnConnect registers what to do once the user supplies working details:
// normally check the folder, index it, and remember it.
func (s *Server) OnConnect(fn func(root string) error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.onlineFn = fn
}

func (s *Server) setupReason() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.setup
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	s.registerPages(mux)
	s.registerArchiveRoutes(mux)
	s.registerSignInRoutes(mux)
	s.registerAccountRoutes(mux)
	s.registerOfficeRoutes(mux)
	// Compression sits inside the security headers, so every response carries
	// the headers whether or not it was compressed. The static handlers above
	// serve their own pre-compressed bytes and pass straight through this;
	// what it catches is the JSON, which is the other place size matters -- a
	// page of a wide report is tens of kilobytes of repetitive text.
	//
	// Panic recovery goes INSIDE compression rather than outside it. A panic
	// has to be caught before compressResponses' deferred Close runs,
	// otherwise the partially buffered body is flushed to the client first and
	// there is no status line left to replace.
	//
	// The request log goes OUTSIDE everything, so the duration it records is
	// the whole of what the client waited for -- headers, compression and all
	// -- and the size is the bytes that actually crossed the wire. The route
	// is asked of the mux itself, which answers with the registered pattern
	// rather than the path; see obs.Middleware for why the path is never
	// logged.
	routeOf := func(r *http.Request) string {
		_, pattern := mux.Handler(r)
		return pattern
	}
	//
	// The audit trail sits just inside the request log, where the signed-in
	// user is already known, and outside the panic recovery, so an action that
	// crashed is still recorded -- as an error.
	return obs.Middleware(nil, routeOf, s.latency,
		audit.Middleware(s.audit, routeOf,
			securityHeaders(s.guard != nil && s.guard.TrustProxyHeaders,
				compressResponses(s.degradedGuard(recoverPanics(mux))))))
}

// registerPages serves the pages and what they load. Each page is a static
// shell that fetches its data from the gated APIs below.
func (s *Server) registerPages(mux *http.ServeMux) {
	// The archive is the whole application: the root goes to it.
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/documents", http.StatusFound)
	})
	// Every other address nothing serves: the 404 page for a browser, JSON for
	// anything else (errorpages.go). "GET /" is the mux's catch-all.
	mux.HandleFunc("GET /", writeNotFound)
	mux.HandleFunc("GET /documents", s.signedInPage(s.handlePage(injectAssets(uiHTML))))
	mux.HandleFunc("GET /vendor/docx-preview.min.js", s.handleVendorJS(docxPreviewJS))
	mux.HandleFunc("GET /vendor/jszip.min.js", s.handleVendorJS(jszipJS))
	mux.HandleFunc("GET /vendor/thinking-orbs.js", s.handleVendorJS(thinkingOrbsJS))
	mux.HandleFunc("GET /favicon.ico", s.handleFavicon())
	// versionAssetLinks stamps the font references inside the stylesheet the
	// same way handlePage stamps a page's references to it; see cachecontrol.go.
	mux.HandleFunc("GET /app.css", s.handleAsset(versionAssetLinks(styleSheet, assetStamp()), "text/css; charset=utf-8"))
	// nav.js loads the dock by address, so it is stamped like a page.
	mux.HandleFunc("GET /nav.js", s.handleAsset(versionAssetLinks(navJS, assetStamp()), "text/javascript; charset=utf-8"))
	mux.HandleFunc("GET /glassnav.js", s.handleAsset(glassNavJS, "text/javascript; charset=utf-8"))
	mux.HandleFunc("GET /meganav.js", s.handleAsset(megaNavJS, "text/javascript; charset=utf-8"))
	mux.HandleFunc("GET /datepick.js", s.handleAsset(datePickJS, "text/javascript; charset=utf-8"))
	mux.HandleFunc("GET /twostep.js", s.handleAsset(twoStepJS, "text/javascript; charset=utf-8"))
	mux.HandleFunc("GET /passkeys.js", s.handleAsset(passkeysJS, "text/javascript; charset=utf-8"))
	mux.HandleFunc("GET /useredit.js", s.handleAsset(userEditJS, "text/javascript; charset=utf-8"))
	mux.HandleFunc("GET /roles.js", s.handleAsset(rolesJS, "text/javascript; charset=utf-8"))
	mux.HandleFunc("GET /upload.js", s.handleAsset(uploadJS, "text/javascript; charset=utf-8"))
	// Third-party assets referenced by path: the icon sprite. Unauthenticated:
	// chrome, carrying nothing the token protects.
	s.registerStatic(mux)

	// The pages. Each is a static shell that fetches its data from the gated
	// APIs. signedInPage redirects a signed-out reader to sign in first
	// (pagegate.go); the sign-in page itself is the opposite.
	mux.HandleFunc("GET /login", s.signedOutPage(s.handlePage(injectAssets(loginHTML))))
	// Unauthenticated on purpose, like /login: it carries no data, and the
	// moment it is needed there is no server to ask about a session anyway.
	mux.HandleFunc("GET /offline", s.handlePage(offlineHTML))
	mux.HandleFunc("GET /settings", s.signedInPage(s.handlePage(injectAssets(settingsHTML))))
	// A page per category (/settings/security, /settings/users, ...): the same
	// page, which reads the category from the address and builds only it.
	mux.HandleFunc("GET /settings/{section}", s.signedInPage(s.handlePage(injectAssets(settingsHTML))))

	// Installable-app assets.
	mux.HandleFunc("GET /manifest.webmanifest", s.handleAsset(manifestJSON, "application/manifest+json"))
	mux.HandleFunc("GET /sw.js", s.handleServiceWorker())
	mux.HandleFunc("GET /icon.png", s.handleAsset(iconPNG, "image/png"))
	mux.HandleFunc("GET /icon-192.png", s.handleAsset(icon192PNG, "image/png"))
	mux.HandleFunc("GET /icon-512.png", s.handleAsset(icon512PNG, "image/png"))
	mux.HandleFunc("GET /icon-maskable.png", s.handleAsset(iconMaskablePNG, "image/png"))
	mux.HandleFunc("GET /apple-touch-icon.png", s.handleAsset(appleTouchIconPNG, "image/png"))
}

// registerOfficeRoutes is the Word editor (office.go, officesave.go).
func (s *Server) registerOfficeRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /office", s.signedInPage(s.handlePage(injectAssets(officeHTML))))
	mux.HandleFunc("GET /office.js", s.handleAsset(officeJS, "text/javascript; charset=utf-8"))
	mux.HandleFunc("GET /api/office/config", s.requirePermission(auth.PageDocuments, s.handleOfficeConfig))
	mux.HandleFunc("GET /api/office/versions", s.requirePermission(auth.PageDocuments, s.handleOfficeVersions))
	mux.HandleFunc("GET /api/office/version", s.requirePermission(auth.PageDocuments, s.handleOfficeVersionFile))
	mux.HandleFunc("POST /api/office/restore", s.mutating(s.requirePermission(auth.ActionDocsWrite, s.handleOfficeRestore)))
	// The document server's two doors: a signed link is the credential.
	mux.HandleFunc("GET /api/office/file", s.handleOfficeFile)
	mux.HandleFunc("POST /api/office/callback", s.handleOfficeCallback)
	// One pattern per method: a pattern with none would clash with "GET /".
	relay := s.officeRelay()
	for _, m := range []string{"GET", "POST", "PUT", "DELETE", "OPTIONS"} {
		mux.Handle(m+" "+officePrefix+"/", relay)
	}
}

// registerArchiveRoutes is reading the archive, and changing where it is
// read from.
func (s *Server) registerArchiveRoutes(mux *http.ServeMux) {
	// Reading the archive.
	mux.HandleFunc("GET /api/status", s.requirePermission(auth.PageDocuments, s.handleStatus))
	mux.HandleFunc("GET /api/tree", s.requirePermission(auth.PageDocuments, s.handleTree))
	mux.HandleFunc("GET /api/search", s.requirePermission(auth.PageDocuments, s.handleSearch))
	mux.HandleFunc("GET /api/doc", s.requirePermission(auth.PageDocuments, s.handleDoc))
	// What a document names -- amounts, terms, numbers, dates -- behind the
	// same gate as the document itself (docfacts.go).
	mux.HandleFunc("GET /api/doc/facts", s.requirePermission(auth.PageDocuments, s.handleDocFacts))
	// The folder controls. Reading a folder is the page's; adding to it,
	// renaming and deleting each carry their own permission. See docsroutes.go
	// for why the three differ.
	mux.HandleFunc("GET /api/docs/list", s.requirePermission(auth.PageDocuments, s.handleDocsList))
	mux.HandleFunc("POST /api/docs/folder", s.mutating(s.requirePermission(auth.ActionDocsEdit, s.handleDocsFolder)))
	mux.HandleFunc("POST /api/docs/upload", s.mutating(s.requirePermission(auth.ActionDocsUpload, s.handleDocsUpload)))
	mux.HandleFunc("POST /api/docs/rename", s.mutating(s.requirePermission(auth.ActionDocsEdit, s.handleDocsRename)))
	mux.HandleFunc("POST /api/docs/delete", s.mutating(s.requirePermission(auth.ActionDocsDelete, s.handleDocsDelete)))

	// Re-indexing.
	mux.HandleFunc("POST /api/rescan", s.mutating(s.requirePermission(auth.ActionReindex, s.handleRescan)))
	// Changing where documents are read from: administrators only, because it
	// reaches outside the archive the other endpoints are confined to.
	mux.HandleFunc("POST /api/connect", s.mutating(s.requirePermission(auth.ActionManageSettings, s.handleConnect)))
}

// registerSignInRoutes is signing in and out, which cannot require a
// session, and the one route a machine calls.
func (s *Server) registerSignInRoutes(mux *http.ServeMux) {
	// Sessions. /api/me is deliberately open: the sign-in page calls it to
	// find out whether anyone is signed in and whether a first administrator
	// still has to be created. It reveals nothing but that.
	mux.HandleFunc("GET /api/me", s.handleMe)
	mux.HandleFunc("POST /api/login", s.mutating(s.handleLogin))
	// The code after the password, for an account that signs in in two steps
	// (twostep.go). Open like /api/login: its challenge is the credential.
	mux.HandleFunc("POST /api/login/second-step", s.mutating(s.handleLoginSecondStep))
	mux.HandleFunc("POST /api/login/second-step/resend", s.mutating(s.handleLoginSecondStepResend))
	// Signing in with a passkey instead of a password (passkeyroutes.go).
	// Open like /api/login: the passkey's signature is the credential.
	mux.HandleFunc("POST /api/login/passkey/options", s.mutating(s.handlePasskeyLoginOptions))
	mux.HandleFunc("POST /api/login/passkey", s.mutating(s.handlePasskeyLogin))
	// A forgotten password, by a code texted to the account's phone
	// (passwordreset.go). Open like /api/login: the code is the credential.
	mux.HandleFunc("POST /api/login/forgot", s.mutating(s.handleForgotPassword))
	mux.HandleFunc("POST /api/login/reset", s.mutating(s.handleResetPassword))
	mux.HandleFunc("POST /api/logout", s.mutating(s.handleLogout))
	mux.HandleFunc("POST /api/first-admin", s.mutating(s.handleFirstAdmin))
}

// registerAccountRoutes is everything that exists only with accounts:
// managing them, sessions and settings.
func (s *Server) registerAccountRoutes(mux *http.ServeMux) {
	// Account management: administrators only.
	if s.AuthEnabled() {
		mux.HandleFunc("GET /api/users", s.requirePermission(auth.ActionManageUsers, s.handleListUsers))
		mux.HandleFunc("POST /api/users", s.mutating(s.requirePermission(auth.ActionManageUsers, s.handleCreateUser)))
		mux.HandleFunc("PUT /api/users", s.mutating(s.requirePermission(auth.ActionManageUsers, s.handleUpdateUser)))
		mux.HandleFunc("POST /api/users/password", s.mutating(s.requirePermission(auth.ActionManageUsers, s.handleSetPassword)))
		mux.HandleFunc("PUT /api/users/phone", s.mutating(s.requirePermission(auth.ActionManageUsers, s.handleSetUserPhone)))
		mux.HandleFunc("DELETE /api/users", s.mutating(s.requirePermission(auth.ActionManageUsers, s.handleDeleteUser)))

		// A person's own account. Every one of these resolves who it is
		// acting on from the caller's token, never from the request, so
		// RequireAuth is the whole gate.
		mux.HandleFunc("GET /api/account/sessions", s.guard.RequireAuth(s.handleSessions))
		mux.HandleFunc("POST /api/account/prefs", s.mutating(s.guard.RequireAuth(s.handleSetPrefs)))
		mux.HandleFunc("POST /api/account/phone", s.mutating(s.guard.RequireAuth(s.handleSetOwnPhone)))
		mux.HandleFunc("POST /api/account/menu", s.mutating(s.guard.RequireAuth(s.handleSetMenu)))
		mux.HandleFunc("POST /api/account/menu/hidden", s.mutating(s.guard.RequireAuth(s.handleSetMenuHidden)))
		mux.HandleFunc("POST /api/account/password", s.mutating(s.guard.RequireAuth(s.handleChangePassword)))
		mux.HandleFunc("POST /api/account/signout-others", s.mutating(s.guard.RequireAuth(s.handleSignOutOthers)))
		// The database's failure level, which every page asks once a minute
		// to decide on the offline banner. See pglevel.go.
		mux.HandleFunc("GET /api/db-state", s.guard.RequireAuth(s.handleDBState))

		// Two-step sign-in on one's own account (twostep.go). Each change asks
		// for the password again; an administrator can only turn it off for
		// somebody, never read or set their key.
		mux.HandleFunc("GET /api/account/two-step", s.guard.RequireAuth(s.handleTwoStep))
		mux.HandleFunc("POST /api/account/two-step/begin", s.mutating(s.guard.RequireAuth(s.handleTwoStepBegin)))
		mux.HandleFunc("POST /api/account/two-step/confirm", s.mutating(s.guard.RequireAuth(s.handleTwoStepConfirm)))
		mux.HandleFunc("POST /api/account/two-step/disable", s.mutating(s.guard.RequireAuth(s.handleTwoStepDisable)))
		mux.HandleFunc("POST /api/account/two-step/recovery", s.mutating(s.guard.RequireAuth(s.handleTwoStepRecovery)))
		mux.HandleFunc("POST /api/account/two-step/sms/begin", s.mutating(s.guard.RequireAuth(s.handleTwoStepSMSBegin)))
		mux.HandleFunc("POST /api/account/two-step/sms/confirm", s.mutating(s.guard.RequireAuth(s.handleTwoStepSMSConfirm)))
		mux.HandleFunc("POST /api/account/two-step/email/begin", s.mutating(s.guard.RequireAuth(s.handleTwoStepEmailBegin)))
		mux.HandleFunc("POST /api/account/two-step/email/confirm", s.mutating(s.guard.RequireAuth(s.handleTwoStepEmailConfirm)))
		mux.HandleFunc("POST /api/account/two-step/send-code", s.mutating(s.guard.RequireAuth(s.handleTwoStepSendCode)))
		// Passkeys on one's own account (passkeyroutes.go). Adding and
		// removing one asks for the password again.
		mux.HandleFunc("GET /api/account/passkeys", s.guard.RequireAuth(s.handlePasskeys))
		mux.HandleFunc("POST /api/account/passkeys/options", s.mutating(s.guard.RequireAuth(s.handlePasskeyOptions)))
		mux.HandleFunc("POST /api/account/passkeys", s.mutating(s.guard.RequireAuth(s.handlePasskeyAdd)))
		mux.HandleFunc("POST /api/account/passkeys/remove", s.mutating(s.guard.RequireAuth(s.handlePasskeyRemove)))
		mux.HandleFunc("POST /api/users/passkeys/reset", s.mutating(s.requirePermission(auth.ActionManageUsers, s.handlePasskeyReset)))
		mux.HandleFunc("POST /api/users/two-step/reset", s.mutating(s.requirePermission(auth.ActionManageUsers, s.handleTwoStepReset)))
		// The sign-in policy and the lockouts (securitypolicy.go).
		mux.HandleFunc("GET /api/admin/security/policy", s.requirePermission(auth.ActionManageSettings, s.handleSecurityPolicy))
		mux.HandleFunc("PUT /api/admin/security/policy", s.mutating(s.requirePermission(auth.ActionManageSettings, s.handleSaveSecurityPolicy)))
		mux.HandleFunc("GET /api/admin/security/lockouts", s.requirePermission(auth.ActionManageUsers, s.handleLockouts))
		mux.HandleFunc("POST /api/admin/security/unlock", s.mutating(s.requirePermission(auth.ActionManageUsers, s.handleUnlock)))

		// Pictures. GET is open to any signed-in person because a colleague's
		// face is drawn beside their name; POST and DELETE act on the caller
		// unless a name is given, and giving a name needs PermManageUsers.
		mux.HandleFunc("GET /api/account/photo", s.guard.RequireAuth(s.handleAvatar))
		mux.HandleFunc("POST /api/account/photo", s.mutating(s.guard.RequireAuth(s.handleAvatarUpload)))
		mux.HandleFunc("DELETE /api/account/photo", s.mutating(s.guard.RequireAuth(s.handleAvatarDelete)))
		// Who is using the application: administrative, as it is everybody's
		// activity.
		mux.HandleFunc("GET /api/presence", s.requirePermission(auth.ActionManageUsers, s.handlePresence))

		// Latency per route, background jobs and the process's vitals
		// (metricsroutes.go). Administrators only.
		mux.HandleFunc("GET /api/admin/metrics", s.requirePermission(auth.ActionManageSettings, s.handleMetrics))
		// The audit trail. Administrators only: it names who did what.
		mux.HandleFunc("GET /api/audit", s.requirePermission(auth.ActionManageUsers, s.handleAuditList))

		// The permission matrix: reading the catalogue and writing somebody
		// else's grants are both administrative acts.
		mux.HandleFunc("GET /api/permissions",
			s.requirePermission(auth.ActionManageUsers, s.handlePermissionCatalogue))
		mux.HandleFunc("GET /api/permissions/user",
			s.requirePermission(auth.ActionManageUsers, s.handleUserPermissions))
		mux.HandleFunc("PUT /api/permissions/user",
			s.mutating(s.requirePermission(auth.ActionManageUsers, s.handleSetUserPermissions)))
		mux.HandleFunc("PUT /api/users/profile", s.mutating(s.requirePermission(auth.ActionManageUsers, s.handleUserProfile)))
		// What a role gets by default, edited (roletemplates.go), and roles of
		// one's own.
		mux.HandleFunc("PUT /api/permissions/role",
			s.mutating(s.requirePermission(auth.ActionManageUsers, s.handleSaveRoleTemplate)))
		mux.HandleFunc("POST /api/permissions/roles",
			s.mutating(s.requirePermission(auth.ActionManageUsers, s.handleCreateRole)))
		mux.HandleFunc("PATCH /api/permissions/roles",
			s.mutating(s.requirePermission(auth.ActionManageUsers, s.handleRenameRole)))
		mux.HandleFunc("DELETE /api/permissions/roles",
			s.mutating(s.requirePermission(auth.ActionManageUsers, s.handleDeleteRole)))
	}
}

// securityHeaders sets the response headers a browser needs in order to defend
// this application on its own.
//
// None of these were present before, which an audit against a running instance
// confirmed. The one with immediate practical consequence is framing: without
// it, another site can load /settings in an invisible frame and lure an
// administrator into clicking the controls on it. The rest are depth -- the
// output encoding is what actually stops script injection here, and the policy
// is the second line for the day something slips through it.
// permissionsPolicy: every device and payment feature off; the archive's
// pages ask the browser for none of them.
const permissionsPolicy = "camera=(), microphone=(), display-capture=(), geolocation=(), payment=(), usb=(), " +
	"serial=(), hid=(), bluetooth=(), midi=(), interest-cohort=(), browsing-topics=()"

func securityHeaders(trustProxy bool, next http.Handler) http.Handler {
	// The baseline: no inline script at all. The embedded pages, which do carry
	// inline script, replace this with a policy naming the hash of each of
	// their own scripts -- see csp.go and handleAsset. Anything else served
	// from here has no script in it and gets the strict form.
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", cspDefault)
		// No API response may be stored by anything between here and the
		// reader. They are per-account by definition -- /api/me is literally
		// "who am I" -- and they went out with no Cache-Control at all and a
		// Vary naming only Accept-Encoding. A shared cache, which is exactly
		// what an office network is apt to have in front of it, could hold one
		// person's answer and hand it to the next person who asked.
		//
		// Set here rather than in each handler because there are sixty-three
		// of them and the one that gets forgotten is the one that matters.
		// no-store rather than Vary: Cookie -- the honest instruction is not
		// "key this by who asked" but "do not keep this".
		if strings.HasPrefix(r.URL.Path, "/api/") {
			h.Set("Cache-Control", "no-store, private")
			h.Set("Pragma", "no-cache")
			h.Add("Vary", "Cookie")
		}
		// frame-ancestors covers modern browsers; this covers the rest.
		h.Set("X-Frame-Options", "DENY")
		// Stops a browser second-guessing the type of a served document and
		// running it as HTML, which matters because the archive holds files
		// this application did not create.
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "same-origin")
		// Every powerful feature this application does not use, switched off,
		// so an injected script or a framed page cannot ask for it either. The
		// archive uses none of the devices: no camera, microphone, screen or
		// location.
		h.Set("Permissions-Policy", permissionsPolicy)
		h.Set("Cross-Origin-Opener-Policy", "same-origin")
		h.Set("Cross-Origin-Resource-Policy", "same-origin")
		// Only over HTTPS: sending HSTS on a plain connection is ignored by
		// browsers, and setting it before the certificate is in place would
		// lock people out the moment it were honoured. IsTLS also counts a
		// connection the trusted front terminated (X-Forwarded-Proto: https) --
		// otherwise, once a CDN terminates TLS and the origin sees plain
		// HTTP, r.TLS is always nil and HSTS would never be sent at all.
		if auth.IsTLS(r, trustProxy) {
			h.Set("Strict-Transport-Security", "max-age=31536000; includeSubDomains")
		}
		next.ServeHTTP(w, r)
	})
}

// guard checks the token, which the UI sends on every call.
//
// An empty token disables the check. That is a deliberate escape hatch for
// machines where copying a changing token each launch is more friction than the
// protection is worth: the listener is loopback-only either way, so what it
// gives up is protection from *other processes on the same machine*, not from
// the network.
func (s *Server) guardToken(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.token == "" {
			next(w, r)
			return
		}
		got := r.URL.Query().Get("t")
		if got == "" {
			got = r.Header.Get("X-Token")
		}
		if subtle.ConstantTimeCompare([]byte(got), []byte(s.token)) != 1 {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		next(w, r)
	}
}

// handleVendorJS serves one of the embedded, unmodified third-party bundles.
// Unauthenticated, same as handleUI: this is library code, not archive data,
// so it carries nothing the token is there to protect.
//
// These are the largest bodies the app serves -- Chart.js is 205 KB and the
// jszip/docx-preview pair another 173 KB -- and they went out with no
// validator and no precompression, so every cold load re-sent and re-gzipped
// them. As embedded assets they are the best possible 304 candidates: the
// bytes cannot change until the binary does.
func (s *Server) handleVendorJS(body []byte) http.HandlerFunc {
	return s.handleAsset(body, "text/javascript; charset=utf-8")
}

// handleFavicon serves the app's own icon for the browser tab shown when
// WebView2 is missing and the app falls back to an ordinary browser window;
// same-origin, unauthenticated, same reasoning as handleUI.
// An icon is requested on every page load and never changes within a build,
// so it is the one asset most worth answering with a 304. It is not gzipped:
// handleAsset only compresses text, and an .ico is already binary.
func (s *Server) handleFavicon() http.HandlerFunc {
	return s.handleAsset(faviconICO, "image/x-icon")
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, map[string]any{
		"count":        s.ix.Count(),
		"root":         s.ix.Root(),
		"types":        s.ix.Types(),
		"categories":   s.ix.Categories(),
		"needs_setup":  s.setupReason() != "",
		"setup_reason": s.setupReason(),
	})
}

func (s *Server) handleTree(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, map[string]any{"groups": s.ix.Tree()})
}

func (s *Server) handleSearch(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit"))
	if limit <= 0 || limit > 500 {
		limit = 200
	}

	started := time.Now()

	// Two ways of reading the same box.
	//
	// ?ask=1 reads the words as a question -- a document type, a Jalali date,
	// a reference number -- and returns what it understood alongside the
	// results. Without it the box behaves exactly as it always has: AND terms
	// and quoted phrases, with the sidebar supplying the filters.
	//
	// A flag rather than a guess. A query that happens to contain the word
	// فاکتور is not necessarily asking for invoices, and silently narrowing
	// somebody's search to a category they did not choose is the worst of both
	// readings: fewer results, and no sign of why.
	var parsed Query
	var interp *Interpretation
	if q.Get("ask") == "1" {
		var in Interpretation
		parsed, in = InterpretQuery(q.Get("q"), time.Now(), limit)
		// The sidebar still wins where it has an opinion. Somebody who has
		// clicked a category in the tree and then typed a question expects
		// both to apply.
		if v := q.Get("type"); v != "" {
			parsed.Type = v
		}
		if v := q.Get("category"); v != "" {
			parsed.Category = v
		}
		if v := q.Get("group"); v != "" {
			parsed.Group = v
		}
		if v := q.Get("ref"); v != "" {
			parsed.Ref = v
		}
		interp = &in
	} else {
		parsed = ParseQueryRef(q.Get("q"), q.Get("type"), q.Get("category"), q.Get("group"), q.Get("ref"), limit)
	}

	// SearchWithIdents runs the search above and, when the box also normalises
	// to an order, invoice, reference or tender number some document's text
	// or file name carries, puts that document first and marked exact -- see
	// internal/find/identindex.go.
	hits, total, corrected := s.ix.SearchWithIdents(q.Get("q"), parsed)

	out := make([]map[string]any, 0, len(hits))
	for _, h := range hits {
		out = append(out, map[string]any{
			"id":       h.Doc.ID,
			"ref":      h.Doc.Ref,
			"type":     h.Doc.Type,
			"category": h.Doc.Category,
			"group":    h.Doc.Group,
			"name":     h.Doc.Name,
			"size":     h.Doc.Size,
			"modified": h.Doc.ModTime.Format("2006-01-02 15:04"),
			"snippet":  h.Snippet,
			"exact":    h.Exact,
		})
	}
	// Comes back null on the common case -- nothing needed correcting -- which
	// is falsy in the page's JS and needs no special-casing there.
	//
	// "count" is how many results are in this response and "total" is how many
	// documents matched. They differ only when a query matches more than the
	// limit, which is exactly when the header must not claim the limit is the
	// answer.
	body := map[string]any{
		"results":   out,
		"count":     len(out),
		"total":     total,
		"took_ms":   time.Since(started).Milliseconds(),
		"corrected": corrected,
	}
	// Only present when the box was read as a question, so the page shows the
	// interpretation panel exactly when there is an interpretation to show.
	if interp != nil {
		body["interpretation"] = interp
	}
	writeJSON(w, body)
}

// handleDoc serves either the extracted text (for the viewer) or the file.
func (s *Server) handleDoc(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("id")
	doc, ok := s.ix.Doc(id)
	if !ok {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}

	// The formatted view: the document as it looks, not a transcript of it.
	if r.URL.Query().Get("mode") == "html" {
		body, err := s.renderHTML(id)
		if err != nil {
			writeJSON(w, map[string]any{
				"id": doc.ID, "name": doc.Name, "error": err.Error(),
				"text": doc.Text, // fall back to the words so the pane is never blank
			})
			return
		}
		writeJSON(w, map[string]any{
			"id":       doc.ID,
			"ref":      doc.Ref,
			"type":     doc.Type,
			"category": doc.Category,
			"name":     doc.Name,
			"size":     doc.Size,
			"modified": doc.ModTime.Format("2006-01-02 15:04"),
			"html":     body,
		})
		return
	}

	if r.URL.Query().Get("mode") == "text" {
		writeJSON(w, map[string]any{
			"id":       doc.ID,
			"ref":      doc.Ref,
			"type":     doc.Type,
			"category": doc.Category,
			"name":     doc.Name,
			"size":     doc.Size,
			"modified": doc.ModTime.Format("2006-01-02 15:04"),
			"text":     doc.Text,
		})
		return
	}

	// Through the archive root, not by path: OpenDoc.
	f, err := s.ix.OpenDoc(id)
	if errors.Is(err, fs.ErrNotExist) {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	if err != nil {
		http.Error(w, "cannot read file", http.StatusInternalServerError)
		return
	}
	defer f.Close()

	mime, inline := docContentType(doc.Name)
	disposition := "attachment"
	if inline {
		disposition = "inline"
	}
	// Set before ServeContent, which only sniffs a type when none is present.
	w.Header().Set("Content-Type", mime)
	w.Header().Set("Content-Disposition",
		fmt.Sprintf(`%s; filename="%s"; filename*=UTF-8''%s`,
			disposition, asciiName(doc.Name), url.PathEscape(doc.Name)))
	http.ServeContent(w, r, doc.Name, doc.ModTime, f)
}

// docTypes is the type to declare for each format the scanner picks up, and
// whether a browser should render it in place.
//
// Every document used to go out as a .docx attachment regardless of what it
// was, so a PDF arrived as a file the browser refused to open under a name
// that lied about its format, and an image could not be viewed at all.
//
// "inline" is safe here only because securityHeaders sets
// X-Content-Type-Options: nosniff on every response: the archive holds files
// this application did not create, and nosniff means the declared type is the
// only type the browser will consider. It is reserved for formats a browser
// renders natively -- TIFF is an image no browser displays, so it downloads.
var docTypes = map[string]struct {
	mime   string
	inline bool
}{
	".pdf":  {"application/pdf", true},
	".png":  {"image/png", true},
	".jpg":  {"image/jpeg", true},
	".jpeg": {"image/jpeg", true},
	".txt":  {"text/plain; charset=utf-8", true},
	".tif":  {"image/tiff", false},
	".tiff": {"image/tiff", false},
	".docx": {"application/vnd.openxmlformats-officedocument.wordprocessingml.document", false},
	".xlsx": {"application/vnd.openxmlformats-officedocument.spreadsheetml.sheet", false},
}

func docContentType(name string) (mime string, inline bool) {
	if t, ok := docTypes[strings.ToLower(filepath.Ext(name))]; ok {
		return t.mime, t.inline
	}
	// Files with no entry are still indexed by name and reference, so one can
	// reach this. Hand it over as bytes rather than guessing at the format.
	return "application/octet-stream", false
}

func (s *Server) handleRescan(w http.ResponseWriter, r *http.Request) {
	n, err := s.ix.Scan()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]any{"ok": true, "updated": n, "count": s.ix.Count()})
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	// HTML escaping stays on. It was once turned off "to keep Persian
	// readable", which was simply untrue -- the flag only governs < > and &,
	// and non-ASCII is emitted as UTF-8 either way. All it did was let a
	// literal </script> out of the encoder, which is exactly the sequence that
	// matters if this JSON is ever placed inside a page rather than fetched.
	enc := json.NewEncoder(w)
	_ = enc.Encode(v)
}

func asciiName(name string) string {
	var b strings.Builder
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9',
			r == '-', r == '_', r == '.', r == ' ':
			b.WriteRune(r)
		default:
			b.WriteRune('_')
		}
	}
	out := strings.Trim(b.String(), " ._")
	if out == "" {
		return "document.docx"
	}
	return out
}

// handleConnect sets the folder the archive is read from, for the setup screen:
// an administrator names it, and it is checked, indexed and made live before
// the answer comes back.
func (s *Server) handleConnect(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Path string `json:"path"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&req); err != nil {
		writeJSON(w, map[string]any{"ok": false, "error": "could not read the form"})
		return
	}
	req.Path = strings.TrimSpace(req.Path)
	if req.Path == "" {
		writeJSON(w, map[string]any{"ok": false, "error": "enter the folder holding the documents"})
		return
	}
	s.mu.Lock()
	fn := s.onlineFn
	s.mu.Unlock()
	if fn == nil {
		writeJSON(w, map[string]any{"ok": false, "error": "this server cannot change the folder"})
		return
	}
	if err := fn(req.Path); err != nil {
		writeJSON(w, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	s.mu.Lock()
	s.setup = ""
	s.mu.Unlock()
	writeJSON(w, map[string]any{"ok": true, "count": s.ix.Count()})
}

// renderHTML reads one document and lays it out for the viewer.
//
// It is read afresh each time rather than cached: the rendered form of a large
// document with pictures is far bigger than the text the index already holds,
// and the archive is on a network share where memory is the scarcer resource.
func (s *Server) renderHTML(id string) (string, error) {
	f, err := s.ix.OpenDoc(id)
	if errors.Is(err, fs.ErrNotExist) {
		return "", fmt.Errorf("not found")
	}
	if err != nil {
		return "", fmt.Errorf("could not read the file: %w", err)
	}
	data, err := io.ReadAll(f)
	f.Close()
	if err != nil {
		return "", fmt.Errorf("could not read the file: %w", err)
	}
	pkg, err := ooxml.Open(data)
	if err != nil {
		return "", fmt.Errorf("this file is not a readable Word document")
	}
	return docxhtml.Render(pkg, docxhtml.Options{})
}
