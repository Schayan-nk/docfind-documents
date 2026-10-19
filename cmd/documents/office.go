package main

import (
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/Schayan-nk/docfind-documents/internal/find"
)

// startOffice turns on the Word editor (internal/find/office.go) when
// DOCFIND_OFFICE_URL names the document server: ONLYOFFICE Docs on loopback
// beside this server, its secret in office.key beside the data
// (DOCFIND_OFFICE_KEY_FILE moves it), the versions each save replaces in
// office-versions there too. The document server reaches this server on its
// own port on loopback, or at DOCFIND_OFFICE_SELF_URL when that is not right.
func startOffice(app *find.Server, o options, stateDir string) {
	docs := strings.TrimSpace(os.Getenv("DOCFIND_OFFICE_URL"))
	if docs == "" {
		return
	}
	key := strings.TrimSpace(os.Getenv("DOCFIND_OFFICE_KEY_FILE"))
	if key == "" {
		key = filepath.Join(stateDir, "office.key")
	}
	self := strings.TrimSpace(os.Getenv("DOCFIND_OFFICE_SELF_URL"))
	if self == "" {
		host := strings.TrimSpace(o.bind)
		if host == "" || host == "0.0.0.0" || host == "::" || isLoopbackHost(host) {
			host = "127.0.0.1"
		}
		self = "http://" + net.JoinHostPort(host, strconv.Itoa(o.port))
	}
	if err := app.UseOffice(docs, self, key, filepath.Join(stateDir, "office-versions")); err != nil {
		logf("the Word editor is off: %v", err)
		return
	}
	logf("the Word editor is on: %s, reaching this server at %s", docs, self)
}
