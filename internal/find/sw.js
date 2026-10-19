// Service worker for the installed app.
//
// Scope is deliberately narrow. This caches the application shell -- the pages
// and the vendored scripts listed below -- so the app opens on a phone with no
// signal instead of showing a browser error page. It never caches an API
// response or a document: those are business data behind a login, and a copy
// sitting in a phone's cache directory is a copy outside the access control
// this application exists to enforce.
//
// Bump CACHE when the shell changes; the activate handler drops older ones.
//
// v6: nav.js, app.css and offline.html all changed in the release that added
// the update notice. Network-first hides most of the cost of forgetting -- a
// reachable server always wins -- but it is exactly the reader with no server
// who would have been left holding the old shell, which is the one case this
// cache exists for.
//
// Only the shell's own addresses are kept, and by path alone. Before, the
// worker kept whatever page was opened, query string and all, so the
// cache's list of keys was a record of what the last person looked at. The name below is replaced with
// the build version when served (version.go), so the release carrying this
// drops those caches as it activates.
const CACHE = 'docfind-shell-v6';

// The page shown when a navigation fails and nothing in the cache matches.
//
// Before this, the fallback was the home page: somebody offline who asked for
// one page silently got another, with no indication that anything had gone
// wrong -- and then watched its panels fail one by one as their requests
// died. A page that says "you are offline" is a smaller
// thing than a page pretending to be somewhere else.
const OFFLINE = '/offline';

const SHELL = [
  '/',
  '/login',
  '/documents',
  '/settings',
  OFFLINE,
  // The shared stylesheet and navigation: without these the offline shell
  // renders as unstyled markup, which looks broken rather than offline.
  '/app.css',
  '/nav.js',
  '/icon.png',
  '/favicon.ico',
  '/manifest.webmanifest',
];

self.addEventListener('install', event => {
  event.waitUntil(
    caches.open(CACHE)
      // addAll fails the whole install if any single entry 404s, which would
      // leave the app with no worker at all; each is added independently so
      // one missing file cannot cost the rest. A page that answered with a
      // redirect -- installed while signed out, it is the sign-in page -- is
      // not kept under the page's name.
      .then(c => Promise.all(SHELL.map(url => fetch(url)
        .then(res => (res.ok && !res.redirected) ? c.put(url, res) : null)
        .catch(() => {}))))
  );
  // NOTE: no skipWaiting() here, and that is the point.
  //
  // It used to be called unconditionally, which meant a newly installed worker
  // seized control of every open page the instant it finished installing. The
  // page in front of somebody was then old HTML being served by a new worker --
  // and that is not a theoretical pairing. A release tightened the Content-
  // Security-Policy, the already-open reports page kept its old markup, the new
  // policy refused to run that markup's inline script, and it sat on
  // "در حال بارگذاری گزارش‌ها…" until somebody was told to reload.
  //
  // So the new worker now WAITS. It has installed, it has its caches, and it
  // does nothing until the page asks it to take over -- which the page only
  // does when somebody presses the button on the update notice. See the message
  // handler below, and the matching half in nav.js.
  //
  // The cost of this is honest and worth stating: an update that nobody accepts
  // is an update that does not happen. Somebody who ignores the notice stays on
  // the old version until they close every tab of the application. That is the
  // trade -- no surprise swaps underneath people, in exchange for updates that
  // wait to be wanted.
});

// The page asking this worker to take over.
//
// The only way a waiting worker ever activates, other than every tab closing.
// It is sent by nav.js when somebody presses "بارگذاری دوباره" on the update
// notice, so the swap happens at a moment they chose, immediately before the
// reload that brings the matching HTML down.
self.addEventListener('message', event => {
  if (event.data && event.data.type === 'SKIP_WAITING') self.skipWaiting();
});

// clients.claim() stays, even though skipWaiting() went.
//
// The two are easy to confuse and do different jobs. skipWaiting decides
// WHETHER a new worker displaces the old one; claim decides whether, once a
// worker is active, it takes charge of pages that loaded before it existed.
//
// Dropping claim as well would only hurt the FIRST visit: the page that
// installed the worker would finish its life uncontrolled, so that first visit
// gets no offline shell until a reload. There is nothing
// to be surprised by there -- no older worker is being displaced, because there
// is no older worker.
self.addEventListener('activate', event => {
  event.waitUntil(
    caches.keys()
      .then(keys => Promise.all(keys.filter(k => k !== CACHE).map(k => caches.delete(k))))
      .then(() => self.clients.claim())
  );
});

// The shell's own addresses, by path. Only these are ever put in the cache.
const SHELL_PATHS = new Set(SHELL);

self.addEventListener('fetch', event => {
  const req = event.request;
  const url = new URL(req.url);
  if (url.origin !== self.location.origin) return;

  // Signing out empties the cache as the request goes by -- untouched, it
  // still goes to the server. The server's answer carries Clear-Site-Data,
  // which does this and more; this is for a browser that ignores it.
  if (req.method === 'POST' && url.pathname === '/api/logout') {
    event.waitUntil(caches.keys().then(ks => Promise.all(ks.map(k => caches.delete(k)))).catch(() => {}));
    return;
  }
  if (req.method !== 'GET') return;

  // Never touch the API or document bytes. Serving a stale report or a
  // document to someone whose access was just revoked is exactly the failure
  // this app must not have, and a cached 401 would be worse still.
  if (url.pathname.startsWith('/api/')) return;

  // Network first for the shell, falling back to cache when offline: the
  // pages are small, and a stale UI that cannot talk to its own API is worth
  // less than a fresh one.
  //
  // KEPT ONLY IF IT IS THE SHELL, stored under its path with no query, and
  // never when the server said no-store. Anything else is fetched and passed
  // through untouched: the cache must hold the application, not a list of
  // which customer, report or case somebody opened.
  const shell = SHELL_PATHS.has(url.pathname);
  event.respondWith(
    fetch(req)
      .then(res => {
        const noStore = /no-store/i.test((res && res.headers.get('Cache-Control')) || '');
        if (shell && !noStore && res && res.ok && !res.redirected && res.type === 'basic') {
          const copy = res.clone();
          caches.open(CACHE).then(c => c.put(url.pathname, copy)).catch(() => {});
        }
        return res;
      })
      .catch(async () => {
        const hit = await caches.match(url.pathname, { ignoreSearch: true });
        if (hit) return hit;
        // Only a NAVIGATION gets the offline page. A stylesheet or a script
        // that is missing from the cache must fail as itself: handing it an
        // HTML document would have the browser parse markup as CSS, and the
        // page would render unstyled with no clue why.
        if (req.mode === 'navigate') {
          const off = await caches.match(OFFLINE);
          if (off) return off;
        }
        return Response.error();
      })
  );
});
