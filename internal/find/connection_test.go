package find

import (
	"strings"
	"testing"
)

// The service worker answers a failed request from its cache for anything
// outside /api/, matching by path and ignoring the query (sw.js). A probe
// asking "is the server back?" must therefore ask under /api/, or the cache
// answers for the server and a dead network is announced as alive -- the
// header's pill cleared and offline.html sent back into another failure.
func TestConnectionProbesCannotBeAnsweredByTheCache(t *testing.T) {
	if !strings.Contains(readPage(t, "sw.js"), "if (url.pathname.startsWith('/api/')) return;") {
		t.Fatal("sw.js no longer leaves /api/ to the network; the probes below rely on it")
	}
	for _, page := range []string{"nav.js", "offline.html"} {
		src := readPage(t, page)
		if !strings.Contains(src, "('/api/me', { cache: 'no-store'") {
			t.Errorf("%s does not probe the server under /api/", page)
		}
		if strings.Contains(src, "favicon.ico?probe") {
			t.Errorf("%s still probes a file the service worker caches", page)
		}
	}
	// And only an /api/ answer counts as the network being back.
	if !strings.Contains(readPage(t, "nav.js"), "if (connDown && fromServer(input)) setConn(false)") {
		t.Error("nav.js treats any answer, cached or not, as the connection being back")
	}
}
