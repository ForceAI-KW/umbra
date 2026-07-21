package doctor

import (
	"bufio"
	"fmt"
	"io"
	"regexp"
	"strings"
	"time"
)

var (
	// time=2026-07-19T22:25:49.262+03:00  and  time="2026-07-19T22:25:49+03:00"
	//
	// ANCHORED at the start of the line on purpose. slog writes the time field
	// first, so the real field is always at position 0. Unanchored, this
	// matched the first `time=` anywhere — including one inside a quoted
	// message body — which stamped a line with a fabricated timestamp instead
	// of dropping it. A wrong time is worse than no line: the cutoff and the
	// whole staleness guard are built on these values.
	timeRe = regexp.MustCompile(`^time="?([0-9T:.+\-]+)"?`)
	macRe  = regexp.MustCompile(`guest link ([0-9a-fA-F:]{17}) closed`)
)

// daemonStartMarker is logged once per umbrad start. It is the only reliable
// in-band signal of when the current daemon lifetime began.
//
// This string is a CONTRACT with cmd/umbrad/main.go, which emits it. If the
// daemon's log message changes and this is not updated in the same commit,
// ScanLog silently returns nothing and the netstack rung can never fire.
const daemonStartMarker = "umbrad listening"

// ScanLog parses umbrad.err.log and returns only the lines belonging to the
// CURRENT daemon lifetime, along with when that lifetime started.
//
// The cutoff is not an optimization — it is a correctness requirement. The
// log survives daemon restarts and host reboots, so lines from an already-fixed
// fault sit in the file indefinitely. Without the cutoff, a healthy host reports
// a dead netstack forever.
//
// When no start marker is present we cannot establish a cutoff, so we return
// nothing rather than risk convicting on stale evidence.
//
// A start marker whose OWN timestamp will not parse is an error, not a line to
// skip: skipping it would silently fall back to an older marker and widen the
// cutoff to cover an already-fixed fault — precisely what the cutoff prevents.
func ScanLog(r io.Reader) ([]LogLine, time.Time, error) {
	var all []LogLine
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		text := sc.Text()
		m := timeRe.FindStringSubmatch(text)
		var ts time.Time
		if m != nil {
			var err error
			ts, err = time.Parse(time.RFC3339Nano, m[1])
			if err != nil {
				m = nil
			}
		}
		if m == nil {
			// Fail closed: an unreadable daemon-start marker makes the whole
			// cutoff untrustworthy, so report that rather than guess.
			if strings.Contains(text, daemonStartMarker) {
				return nil, time.Time{}, fmt.Errorf("doctor: daemon-start marker has an unparseable timestamp, cannot establish a log cutoff: %q", text)
			}
			continue
		}
		l := LogLine{Time: ts, Text: text}
		if mm := macRe.FindStringSubmatch(text); mm != nil {
			l.MAC = mm[1]
		}
		all = append(all, l)
	}
	if err := sc.Err(); err != nil {
		return nil, time.Time{}, err
	}

	// Find the LAST start marker — that is the current lifetime.
	startIdx := -1
	for i, l := range all {
		if strings.Contains(l.Text, daemonStartMarker) {
			startIdx = i
		}
	}
	if startIdx < 0 {
		return nil, time.Time{}, nil
	}
	return all[startIdx:], all[startIdx].Time, nil
}
