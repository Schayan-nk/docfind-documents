package find

import (
	"os"
	"strings"
	"testing"
)

// Opening a document must not hand its name to a command interpreter.
//
// The Windows branch used to be:
//
//	exec.Command("cmd", "/c", "start", "", path)
//
// which runs cmd.exe -- an interpreter that re-parses its own command line and
// treats &, |, ^ and friends as syntax. Go does not protect against that,
// because Go's Windows argument escaping only wraps an argument in quotes when
// it contains a space or a tab. A file called
//
//	a&calc.docx
//
// has no space, so it reaches cmd.exe bare, and cmd.exe reads the ampersand as
// "end of this command, start the next one".
//
// The precondition is write access to the archive, which the document
// generator and the staff share have; the effect is a command running on the
// server as the service account, triggered by an administrator pressing Open.
// Neither half of that is exotic enough to leave alone.
//
// A note for whoever checks this claim next, because the obvious way to check
// it is wrong: exec.Cmd.String() does NOT show the command line Windows
// receives. It is a display helper that joins the arguments with spaces, and
// the real escaping happens in syscall.EscapeArg when the process starts. A
// first version of this test asserted against String() and proved nothing
// either way.
//
// The fix does not rest on the exact quoting rule in any case. cmd.exe re-parses
// whatever it is given, so the argument is not to quote carefully for it but
// not to involve it: TestOpenInShellUsesNoInterpreter, below, is the assertion
// that matters, and TestSafeToOpenRefusesInterpreterSyntax is the belt to its
// braces.

// The opener runs the file through a program that takes a path, not a command
// line to parse.
func TestOpenInShellUsesNoInterpreter(t *testing.T) {
	// Read the source rather than run it: actually opening a document during a
	// test would launch an application on whoever's machine is running this.
	b, err := os.ReadFile("server.go")
	if err != nil {
		t.Fatalf("cannot read server.go: %v", err)
	}
	src := string(b)

	if strings.Contains(src, `exec.Command("cmd", "/c", "start"`) {
		t.Error("openInShell still goes through cmd.exe. A document named \"a&calc.docx\" " +
			"is then two commands, and the second one is chosen by whoever could write " +
			"that file into the archive.")
	}
	// And whatever replaced it must still be a direct exec with the path as its
	// own argument -- not a string somebody assembled.
	if strings.Contains(src, "openInShell") && !strings.Contains(src, "safeToOpen(") {
		t.Error("openInShell no longer checks the path before handing it to the system " +
			"opener; that check is the second half of the fix")
	}
}
