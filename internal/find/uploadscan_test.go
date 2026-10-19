package find

import "testing"

// MpCmdRun exits 2 both when it finds a threat and when it cannot scan at
// all; only its report of a threat is a threat. The failure text is the one
// this build machine's Defender printed on a harmless PDF.
func TestDefenderFailureIsNotAThreat(t *testing.T) {
	for name, tc := range map[string]struct {
		out    string
		threat bool
	}{
		"scan failed": {"CmdTool: Failed with hr = 0x80004005. Check C:\\Users\\x\\AppData\\Local\\Temp\\MpCmdRun.log for more information", false},
		"clean":       {"Scan starting...\r\nScan finished.\r\nScanning C:\\up\\note.pdf found no threats.", false},
		"threat": {"Scan starting...\r\nScan finished.\r\nScanning C:\\up\\eicar.com found 1 threats.\r\n\r\n" +
			"<===========================LIST OF DETECTED THREATS==========================>\r\n" +
			"----------------------------- Threat information ------------------------------\r\n" +
			"Threat                  : Virus:DOS/EICAR_Test_File\r\n", true},
	} {
		if got := defenderReportedThreat([]byte(tc.out)); got != tc.threat {
			t.Errorf("%s: threat = %v, want %v", name, got, tc.threat)
		}
	}
	if got := firstLine([]byte("  CmdTool: Failed\r\nsecond")); got != "CmdTool: Failed" {
		t.Errorf("firstLine = %q", got)
	}
}
