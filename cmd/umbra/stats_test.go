package main

import "testing"

func TestParseGuestStats(t *testing.T) {
	// Canned output matching guestStatsScript's fixed command order:
	// loadavg, then `free -b` Mem:/Swap: lines, then `df -B1 --output=used,size /`.
	const out = `0.42 0.35 0.30 1/234 5678
Mem:    16588985856  4283904000  8000000000    12345678   3000000000  9000000000
Swap:    2147479552           0  2147479552
12345678900    53687091200
`
	gs, err := parseGuestStats(out)
	if err != nil {
		t.Fatalf("parseGuestStats returned error: %v", err)
	}
	if gs.Load != "0.42" {
		t.Errorf("Load = %q, want %q", gs.Load, "0.42")
	}
	if gs.MemTotal != 16588985856 {
		t.Errorf("MemTotal = %d, want %d", gs.MemTotal, 16588985856)
	}
	if gs.MemUsed != 4283904000 {
		t.Errorf("MemUsed = %d, want %d", gs.MemUsed, 4283904000)
	}
	if gs.SwapTotal != 2147479552 {
		t.Errorf("SwapTotal = %d, want %d", gs.SwapTotal, 2147479552)
	}
	if gs.SwapUsed != 0 {
		t.Errorf("SwapUsed = %d, want %d", gs.SwapUsed, 0)
	}
	if gs.DiskUsed != 12345678900 {
		t.Errorf("DiskUsed = %d, want %d", gs.DiskUsed, 12345678900)
	}
	if gs.DiskTotal != 53687091200 {
		t.Errorf("DiskTotal = %d, want %d", gs.DiskTotal, 53687091200)
	}
}

func TestParseGuestStatsErrors(t *testing.T) {
	cases := []struct {
		name string
		out  string
	}{
		{"empty", ""},
		{"too few lines", "0.42 0.35 0.30 1/234 5678\nMem:    16588985856  4283904000\n"},
		{"missing Mem/Swap", "0.42 0.35 0.30 1/234 5678\nfoo\nbar\n12345678900    53687091200\n"},
		{"malformed disk line", "0.42 0.35 0.30 1/234 5678\nMem:    16588985856  4283904000  0\nSwap:    2147479552 0 2147479552\nnotanumber\n"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := parseGuestStats(c.out); err == nil {
				t.Errorf("parseGuestStats(%q) expected error, got nil", c.out)
			}
		})
	}
}
