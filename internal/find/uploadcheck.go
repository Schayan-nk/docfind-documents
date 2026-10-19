package find

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/Schayan-nk/docfind-documents/internal/obs"
)

// What an upload has to prove before it reaches the archive.
//
// The name was already checked (docspath.go): an allowed extension, nothing
// that traverses, nothing Windows would reinterpret. But a name is a claim.
// "report.pdf" can be a program, and a Word document can carry macros; the
// archive is opened on staff machines, so what goes in has to be what it says.
//
// So an upload lands in a QUARANTINE folder outside the archive first, and
// only moves in once it has passed:
//
//   - its first bytes match its extension (a PDF starts %PDF-, an Office file
//     is a ZIP or an OLE compound file, an image has its own signature, and
//     text has no NUL bytes); a Windows program ("MZ") is refused whatever it
//     is called;
//   - an Office file carries no macros (vbaProject.bin in the new formats, a
//     _VBA_PROJECT stream in the old), and a ZIP carries no program, script
//     or shortcut;
//   - a ZIP is not a decompression bomb: at most 5,000 entries, at most 1 GB
//     unpacked, at most 100 times its packed size;
//   - Windows Defender, where the server has it, finds nothing.
//
// A refused file STAYS in quarantine\rejected\<day>\ with a note of who sent
// it, what it was called and why it was refused, for an administrator to look
// at; the folder is pruned after 30 days. A refused upload is also a line in
// logs\uploads\.

// quarantineDir is where uploads wait: the server's data folder when set,
// the temp folder otherwise.
func (s *Server) quarantineDir() string {
	if s.quarantine != "" {
		return s.quarantine
	}
	return filepath.Join(os.TempDir(), "docfind-quarantine")
}

// UseQuarantine sets the quarantine folder.
func (s *Server) UseQuarantine(dir string) { s.quarantine = dir }

// rejection is why a file was refused, in words for the person uploading.
type rejection struct{ reason string }

func (r rejection) Error() string { return r.reason }

func reject(format string, a ...any) error { return rejection{reason: fmt.Sprintf(format, a...)} }

// inspectUpload checks the file at path against the extension it claims.
func inspectUpload(path, ext string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	head := make([]byte, 8192)
	n, _ := io.ReadFull(f, head)
	head = head[:n]
	f.Close()

	if bytes.HasPrefix(head, []byte("MZ")) {
		return reject("این پرونده یک برنامهٔ اجرایی است، نه سند")
	}
	ole := bytes.HasPrefix(head, []byte{0xD0, 0xCF, 0x11, 0xE0, 0xA1, 0xB1, 0x1A, 0xE1})
	zipped := bytes.HasPrefix(head, []byte("PK\x03\x04"))

	switch ext {
	case ".pdf":
		if !bytes.HasPrefix(head, []byte("%PDF-")) {
			return reject("محتوای پرونده PDF نیست")
		}
	case ".docx", ".xlsx", ".pptx", ".odt", ".ods", ".odp", ".zip":
		if !zipped {
			return reject("محتوای پرونده با پسوند %s جور نیست", ext)
		}
		return inspectZip(path, ext)
	case ".doc", ".xls", ".ppt", ".msg":
		if !ole {
			return reject("محتوای پرونده با پسوند %s جور نیست", ext)
		}
		return inspectOLE(path)
	case ".rtf":
		if !bytes.HasPrefix(head, []byte(`{\rtf`)) {
			return reject("محتوای پرونده RTF نیست")
		}
	case ".png":
		if !bytes.HasPrefix(head, []byte("\x89PNG\r\n\x1a\n")) {
			return reject("محتوای پرونده تصویر PNG نیست")
		}
	case ".jpg", ".jpeg":
		if !bytes.HasPrefix(head, []byte{0xFF, 0xD8, 0xFF}) {
			return reject("محتوای پرونده تصویر JPEG نیست")
		}
	case ".gif":
		if !bytes.HasPrefix(head, []byte("GIF87a")) && !bytes.HasPrefix(head, []byte("GIF89a")) {
			return reject("محتوای پرونده تصویر GIF نیست")
		}
	case ".webp":
		if len(head) < 12 || string(head[:4]) != "RIFF" || string(head[8:12]) != "WEBP" {
			return reject("محتوای پرونده تصویر WebP نیست")
		}
	case ".tif", ".tiff":
		if !bytes.HasPrefix(head, []byte("II*\x00")) && !bytes.HasPrefix(head, []byte("MM\x00*")) {
			return reject("محتوای پرونده تصویر TIFF نیست")
		}
	case ".txt", ".csv", ".eml":
		if bytes.IndexByte(head, 0) >= 0 && !utf16Text(head) {
			return reject("این پرونده متن نیست")
		}
		if bytes.HasPrefix(head, []byte("#!")) {
			return reject("این پرونده یک اسکریپت است")
		}
	}
	return nil
}

// utf16Text allows a UTF-16 text file, which Notepad writes and which is full
// of NUL bytes by construction.
func utf16Text(b []byte) bool {
	return bytes.HasPrefix(b, []byte{0xFF, 0xFE}) || bytes.HasPrefix(b, []byte{0xFE, 0xFF})
}

// Names inside a ZIP that are never a document.
var dangerousInside = []string{
	".exe", ".dll", ".com", ".scr", ".pif", ".bat", ".cmd", ".ps1", ".psm1",
	".vbs", ".vbe", ".js", ".jse", ".wsf", ".wsh", ".hta", ".msi", ".msp",
	".jar", ".lnk", ".url", ".reg", ".cpl", ".sys", ".iso", ".img",
}

const (
	maxZipEntries  = 5000
	maxZipUnpacked = 1 << 30
	maxZipRatio    = 100
)

func inspectZip(path, ext string) error {
	zr, err := zip.OpenReader(path)
	if err != nil {
		return reject("پرونده فشرده خوانده نشد (آسیب‌دیده یا ناقص است)")
	}
	defer zr.Close()
	if len(zr.File) > maxZipEntries {
		return reject("پرونده فشرده بیش از %d مورد دارد", maxZipEntries)
	}
	st, err := os.Stat(path)
	if err != nil {
		return err
	}
	var unpacked uint64
	for _, f := range zr.File {
		unpacked += f.UncompressedSize64
		name := strings.ToLower(f.Name)
		// Macros, whatever the extension said: a .docx holding a VBA project
		// is a .docm in disguise.
		if strings.HasSuffix(name, "vbaproject.bin") || strings.Contains(name, "activex") {
			return reject("این سند ماکرو یا کنترل ActiveX دارد و پذیرفته نمی‌شود")
		}
		for _, bad := range dangerousInside {
			if strings.HasSuffix(name, bad) {
				return reject("پرونده فشرده یک برنامه یا اسکریپت دارد: %s", filepath.Base(f.Name))
			}
		}
	}
	if unpacked > maxZipUnpacked || (st.Size() > 0 && unpacked/uint64(st.Size()) > maxZipRatio) {
		return reject("پرونده فشرده پس از باز شدن بیش از اندازه بزرگ می‌شود")
	}
	return nil
}

// inspectOLE refuses an old-format Office file that carries macros. The VBA
// project is a stream named _VBA_PROJECT (stored as UTF-16 in the directory),
// so its presence anywhere in the file is the tell.
func inspectOLE(path string) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	marker := []byte{'_', 0, 'V', 0, 'B', 0, 'A', 0, '_', 0, 'P', 0, 'R', 0, 'O', 0, 'J', 0, 'E', 0, 'C', 0, 'T', 0}
	if bytes.Contains(raw, marker) || bytes.Contains(raw, []byte("_VBA_PROJECT")) {
		return reject("این سند ماکرو دارد و پذیرفته نمی‌شود")
	}
	return nil
}

// ---------------------------------------------------------------- antivirus

// defenderPath finds Windows Defender's command-line scanner, or "".
func defenderPath() string {
	if strings.EqualFold(strings.TrimSpace(os.Getenv("DOCFIND_UPLOAD_AV")), "off") {
		return ""
	}
	candidates := []string{filepath.Join(os.Getenv("ProgramFiles"), "Windows Defender", "MpCmdRun.exe")}
	if plat, _ := filepath.Glob(filepath.Join(os.Getenv("ProgramData"),
		"Microsoft", "Windows Defender", "Platform", "*", "MpCmdRun.exe")); len(plat) > 0 {
		candidates = append(plat[len(plat)-1:], candidates...) // the newest platform first
	}
	for _, c := range candidates {
		if st, err := os.Stat(c); err == nil && !st.IsDir() {
			return c
		}
	}
	return ""
}

// scanUpload runs Defender on one file. found is true only when Defender
// reported a threat; a scanner that could not run is not a clean result, and
// is logged as such, but does not refuse the upload -- the content checks
// above have already run.
//
// NOT THE EXIT CODE ALONE. MpCmdRun exits 2 when it finds a threat, and
// also when the scan itself fails ("CmdTool: Failed with hr = 0x80004005"):
// on a machine where Defender cannot scan -- another antivirus managing it,
// the service stopped -- reading 2 as "infected" refused every upload. A
// threat is exit 2 with Defender's own report of one in the output.
func scanUpload(path string) (found bool, err error) {
	mp := defenderPath()
	if mp == "" {
		return false, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, mp, "-Scan", "-ScanType", "3", "-File", path, "-DisableRemediation")
	out, err := cmd.CombinedOutput()
	var exit *exec.ExitError
	if errors.As(err, &exit) && exit.ExitCode() == 2 {
		if defenderReportedThreat(out) {
			return true, nil
		}
		return false, fmt.Errorf("defender could not scan: %s", firstLine(out))
	}
	return false, err
}

var (
	defenderThreatCount = regexp.MustCompile(`(?i)found\s+[1-9][0-9]*\s+threats?`)
	defenderThreatLine  = regexp.MustCompile(`(?im)^\s*threat\s*:`)
	defenderFailed      = regexp.MustCompile(`(?i)failed with hr\s*=`)
)

// defenderReportedThreat reads MpCmdRun's output: "Scanning <file> found 1
// threats." and a "Threat : <name>" line under LIST OF DETECTED THREATS.
func defenderReportedThreat(out []byte) bool {
	if defenderFailed.Match(out) {
		return false
	}
	return defenderThreatCount.Match(out) || defenderThreatLine.Match(out)
}

func firstLine(b []byte) string {
	s := strings.TrimSpace(string(b))
	if i := strings.IndexAny(s, "\r\n"); i >= 0 {
		s = s[:i]
	}
	return clipRunes(s, 200)
}

// ------------------------------------------------------------ quarantine keep

// keepRejected moves a refused upload into quarantine\rejected\<day>\ with a
// note beside it, and logs it.
func (s *Server) keepRejected(tmp, name, user, reason string) {
	day := time.Now().Format("2006-01-02")
	dir := filepath.Join(s.quarantineDir(), "rejected", day)
	obs.Default().Write("uploads", "warn", obs.Fields{"msg": "upload refused", "name": name,
		"user": user, "reason": reason})
	if err := os.MkdirAll(dir, 0o750); err != nil {
		os.Remove(tmp)
		return
	}
	stem := time.Now().Format("150405.000") + "-" + filepath.Base(tmp)
	kept := filepath.Join(dir, stem+".quarantined")
	if err := os.Rename(tmp, kept); err != nil {
		os.Remove(tmp)
		return
	}
	note, _ := json.MarshalIndent(map[string]any{
		"name": name, "user": user, "reason": reason, "at": time.Now().Format(time.RFC3339),
	}, "", "  ")
	_ = os.WriteFile(filepath.Join(dir, stem+".json"), note, 0o640)
	s.pruneQuarantine()
}

// pruneQuarantine removes rejected uploads older than 30 days.
func (s *Server) pruneQuarantine() {
	root := filepath.Join(s.quarantineDir(), "rejected")
	days, err := os.ReadDir(root)
	if err != nil {
		return
	}
	cutoff := time.Now().AddDate(0, 0, -30).Format("2006-01-02")
	for _, d := range days {
		if _, perr := time.Parse("2006-01-02", d.Name()); d.IsDir() && perr == nil && d.Name() < cutoff {
			_ = os.RemoveAll(filepath.Join(root, d.Name()))
		}
	}
}
