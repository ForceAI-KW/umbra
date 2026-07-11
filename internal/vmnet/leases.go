// Package vmnet resolves guest IPs from macOS bootpd's lease database.
package vmnet

import (
	"bufio"
	"os"
	"strconv"
	"strings"
)

const leasesFile = "/var/db/dhcpd_leases"

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

func LookupIP(leases []byte, mac string) (string, bool) {
	want := normalizeMAC(mac)
	if want == "" {
		return "", false
	}
	var ip string
	sc := bufio.NewScanner(strings.NewReader(string(leases)))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		switch {
		case line == "{":
			ip = ""
		case strings.HasPrefix(line, "ip_address="):
			ip = strings.TrimPrefix(line, "ip_address=")
		case strings.HasPrefix(line, "hw_address="):
			if normalizeMAC(strings.TrimPrefix(line, "hw_address=")) == want && ip != "" {
				return ip, true
			}
		}
	}
	return "", false
}

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
