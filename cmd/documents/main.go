// Command documents is docfind's document archive on its own: a folder of
// Word and PDF files, indexed and searchable in Persian and English as they
// change, read in the browser, uploaded to and tidied by the people allowed
// to, and edited in place when an ONLYOFFICE document server is beside it.
//
// The archive is a folder given with -archive (or DOCFIND_ARCHIVE); accounts
// come from the file given with -users, and from PostgreSQL (DOCFIND_PG_*)
// when one is configured. Run with -add-user first to create the first
// administrator.
package main

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/Schayan-nk/docfind-documents/internal/auth"
	"github.com/Schayan-nk/docfind-documents/internal/find"
)

// isLoopbackHost reports whether an address only this machine can reach.
func isLoopbackHost(host string) bool {
	switch strings.ToLower(host) {
	case "127.0.0.1", "::1", "localhost":
		return true
	}
	if ip := net.ParseIP(host); ip != nil {
		return ip.IsLoopback()
	}
	return false
}

var version = "dev"

const defaultPort = 8310

func main() {
	if err := run(); err != nil {
		logf("error: %s", err.Error())
		os.Exit(1)
	}
}

// run is the archive server from start to finish: a one-shot command, or the
// server -- the index, the accounts, the editor -- until a signal ends it.
func run() error {
	o := parseOptions()
	o.loadEnv()
	if handled, err := o.runOneShot(); handled {
		return err
	}
	find.SetVersion(version)
	startAppLog(o.usersFile)
	defer stopAppLog()

	if o.usersFile == "" {
		return fmt.Errorf("the archive needs accounts: pass -users <path to the accounts file> (and create the first one with -add-user)")
	}
	token, err := accessToken(true)
	if err != nil {
		return err
	}
	// The archive first: the index is loaded from its cache and brought up to
	// date before anybody can search it. A folder that cannot be read is not
	// fatal -- the server starts and says why (setup mode).
	docs := newArchive()
	setupReason := docs.open(resolveArchiveRoot(o.archive))
	acc, err := openAccounts(o.usersFile, docs.ix)
	if err != nil {
		return err
	}
	if acc.pgDB != nil {
		defer acc.pgDB.Close()
	}
	ln, url, err := listen(o, acc.users, token)
	if err != nil {
		return err
	}

	app := find.NewServer(docs.ix, token)
	app.UsePermissions(auth.NewCatalogue())
	var closers cleanups
	defer func() { closers.run() }()
	closers.add(useAccounts(app, acc, o))

	srv, err := newHTTPServer(app)
	if err != nil {
		return err
	}
	if setupReason != "" {
		app.NeedsSetup(setupReason)
	}
	// The setup screen can name another folder; an administrator's only.
	app.OnConnect(docs.bringOnline)
	stopRescan := make(chan struct{})
	go docs.rescanEvery(o.rescan, stopRescan)
	go serve(srv, ln)
	logf("ready: %s", url)

	waitForSignal()
	close(stopRescan)
	docs.stopWatching()
	shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return srv.Shutdown(shutdown)
}

// waitForSignal blocks until the program is interrupted.
func waitForSignal() {
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	<-ctx.Done()
}
