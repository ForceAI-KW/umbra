// Package vmnet resolves guest IPs from macOS bootpd's lease database.
package vmnet

import (
	"bufio"
	"bytes"
	"os"
	"strconv"
	"strings"
)

// var (not const) so tests can point at a fixture file.
var leasesFile = "/var/db/dhcpd_leases"

// normalizeMAC parses each octet as hex so "a6:5e:0:11:2:33" == "a6:5e:00:11:02:33".
func normalizeMAC(s string) string {
	s = strings.TrimPrefix(strings.TrimSpace(s), "1,")
	parts := strings.Split(s, ":")
	for i, p := range parts {
		v, err := strconv.ParseUint(p, 16, 8)
		if err != nil {
			return ""
		}
		parts[i] = strconv.FormatUint(v, 16)
	}
	return strings.Join(parts, ":")
}

// LookupIP scans bootpd lease blocks for the given MAC. Fields are collected
// per block and evaluated at block close, so field order inside a block
// doesn't matter. First matching block wins — bootpd prepends fresh leases,
// so the first match is the most recent.
func LookupIP(leases []byte, mac string) (string, bool) {
	want := normalizeMAC(mac)
	if want == "" {
		return "", false
	}
	var ip, hw string
	match := func() bool { return ip != "" && hw == want }
	sc := bufio.NewScanner(bytes.NewReader(leases))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		switch {
		case line == "{":
			ip, hw = "", ""
		case line == "}":
			if match() {
				return ip, true
			}
		case strings.HasPrefix(line, "ip_address="):
			ip = strings.TrimPrefix(line, "ip_address=")
		case strings.HasPrefix(line, "hw_address="):
			hw = normalizeMAC(strings.TrimPrefix(line, "hw_address="))
		}
	}
	// tolerate a final block with no closing brace
	if match() {
		return ip, true
	}
	return "", false
}

// LookupIPFromFile resolves a MAC via /var/db/dhcpd_leases. A missing file
// means no lease yet (not an error) — the guest may still be booting.
func LookupIPFromFile(mac string) (string, bool, error) {
	b, err := os.ReadFile(leasesFile)
	if err != nil {
		if os.IsNotExist(err) {
			return "", false, nil
		}
		return "", false, err
	}
	ip, ok := LookupIP(b, mac)
	return ip, ok, nil
}
