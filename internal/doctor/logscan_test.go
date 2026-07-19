package doctor

import (
	"strings"
	"testing"
)

// The daemon writes two timestamp shapes — bare and quoted. Both must parse.
const sampleLog = `time=2026-07-19T22:25:49.262+03:00 level=INFO msg="netstack: guest link b2:71:f2:cb:76:64 closed: cannot receive packets from , disconnecting: cannot read size from socket"
time="2026-07-19T22:25:49+03:00" level=error msg="accept tcp 127.0.0.1:60952: use of closed network connection"
time=2026-07-19T22:33:00.856+03:00 level=INFO msg="umbrad listening" socket=/Users/x/.umbra/run/api.sock
time=2026-07-19T22:33:00.857+03:00 level=INFO msg=autostarting machine=fwb-ci5
`

func TestScanLogDropsLinesBeforeDaemonStart(t *testing.T) {
	lines, start, err := ScanLog(strings.NewReader(sampleLog))
	if err != nil {
		t.Fatalf("ScanLog returned error: %v", err)
	}
	if start.IsZero() {
		t.Fatal("daemon start time not detected")
	}
	// The two 22:25 netstack lines predate the 22:33 restart and must be gone.
	for _, l := range lines {
		if strings.Contains(l.Text, "cannot receive packets") {
			t.Errorf("stale pre-restart netstack line survived the cutoff: %q", l.Text)
		}
	}
	if len(lines) != 2 {
		t.Errorf("len(lines) = %d, want 2 (the listening line and the one after it)", len(lines))
	}
}

func TestScanLogExtractsMAC(t *testing.T) {
	const l = `time=2026-07-19T23:00:00.000+03:00 level=INFO msg="umbrad listening"
time=2026-07-19T23:01:00.000+03:00 level=INFO msg="netstack: guest link aa:bb:cc:dd:ee:ff closed: cannot receive packets from , disconnecting"
`
	lines, _, err := ScanLog(strings.NewReader(l))
	if err != nil {
		t.Fatalf("ScanLog returned error: %v", err)
	}
	var got string
	for _, ln := range lines {
		if ln.MAC != "" {
			got = ln.MAC
		}
	}
	if got != "aa:bb:cc:dd:ee:ff" {
		t.Errorf("MAC = %q, want %q", got, "aa:bb:cc:dd:ee:ff")
	}
}

func TestScanLogNoListeningLineKeepsNothing(t *testing.T) {
	// Without a daemon-start marker we cannot establish a cutoff, so we must
	// return no lines rather than risk convicting on stale evidence.
	const l = `time=2026-07-19T22:25:49.262+03:00 level=INFO msg="netstack: guest link b2:71:f2:cb:76:64 closed: cannot receive packets"
`
	lines, start, err := ScanLog(strings.NewReader(l))
	if err != nil {
		t.Fatalf("ScanLog returned error: %v", err)
	}
	if !start.IsZero() {
		t.Errorf("start = %v, want zero", start)
	}
	if len(lines) != 0 {
		t.Errorf("len(lines) = %d, want 0", len(lines))
	}
}

// F4. If the daemon-start marker's own timestamp is corrupt, the line is
// invisible to the cutoff search and an OLDER marker would be used instead —
// silently reintroducing exactly the stale evidence the cutoff exists to
// remove. That is an anomaly, so fail closed rather than convict on stale logs.
func TestScanLogUnparseableStartMarkerFailsClosed(t *testing.T) {
	const l = `time=2026-07-19T20:00:00.000+03:00 level=INFO msg="umbrad listening" socket=/x
time=2026-07-19T20:01:00.000+03:00 level=INFO msg="netstack: guest link aa:bb:cc:dd:ee:01 closed: cannot receive packets"
time=NOT-A-TIMESTAMP level=INFO msg="umbrad listening" socket=/x
time=2026-07-19T22:01:00.000+03:00 level=INFO msg=autostarting machine=fwb-ci5
`
	lines, start, err := ScanLog(strings.NewReader(l))
	if err == nil {
		t.Fatal("ScanLog accepted a daemon-start marker with an unparseable timestamp")
	}
	if len(lines) != 0 {
		t.Errorf("len(lines) = %d, want 0 (fail closed)", len(lines))
	}
	if !start.IsZero() {
		t.Errorf("start = %v, want zero", start)
	}
}

// F5. timeRe was unanchored, so it matched the first `time=` ANYWHERE in the
// line. A line with no leading timestamp field but a `time=` inside its message
// body was therefore accepted and stamped with the decoy's value — a fabricated
// timestamp, which is worse than dropping the line. Anchoring makes the regex
// describe the field it actually means.
func TestScanLogIgnoresDecoyTimeInMessageBody(t *testing.T) {
	const l = `time=2026-07-19T23:00:00.000+03:00 level=INFO msg="umbrad listening"
level=INFO msg="replaying from time=2020-01-01T00:00:00.000+00:00" machine=fwb-ci5
time=2026-07-19T23:05:00.000+03:00 level=INFO msg="probe slow: last time=200ms" machine=fwb-ci5
`
	lines, _, err := ScanLog(strings.NewReader(l))
	if err != nil {
		t.Fatalf("ScanLog returned error: %v", err)
	}
	// The decoy line has no real timestamp field and must be dropped entirely.
	if len(lines) != 2 {
		t.Fatalf("len(lines) = %d, want 2 — the decoy-only line was accepted with a fabricated Time: %+v", len(lines), lines)
	}
	want := "2026-07-19T23:05:00.000+03:00"
	if got := lines[1].Time.Format("2006-01-02T15:04:05.000-07:00"); got != want {
		t.Errorf("Time = %q, want %q", got, want)
	}
}
