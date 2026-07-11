package vmnet

import (
	"os"
	"path/filepath"
	"testing"
)

const fixture = `{
	name=t1
	ip_address=192.168.64.5
	hw_address=1,a6:5e:0:11:2:33
	identifier=1,a6:5e:0:11:2:33
	lease=0x66b2c1de
}
{
	name=other
	ip_address=192.168.64.9
	hw_address=1,de:ad:be:ef:0:1
}`

func TestLookupIPNormalizesLeadingZeros(t *testing.T) {
	// config stores canonical form with leading zeros; leases file has them stripped
	ip, ok := LookupIP([]byte(fixture), "a6:5e:00:11:02:33")
	if !ok || ip != "192.168.64.5" {
		t.Fatalf("got %q %v", ip, ok)
	}
}

func TestLookupIPMiss(t *testing.T) {
	if _, ok := LookupIP([]byte(fixture), "aa:bb:cc:dd:ee:ff"); ok {
		t.Fatal("want miss")
	}
}

const fixtureReversedOrder = `{
	name=t1
	hw_address=1,a6:5e:0:11:2:33
	ip_address=192.168.64.5
}`

func TestLookupIPFieldOrderIndependent(t *testing.T) {
	ip, ok := LookupIP([]byte(fixtureReversedOrder), "a6:5e:00:11:02:33")
	if !ok || ip != "192.168.64.5" {
		t.Fatalf("hw-before-ip block: got %q %v", ip, ok)
	}
}

func TestLookupIPFirstMatchWinsAcrossDuplicates(t *testing.T) {
	dup := `{
	ip_address=192.168.64.20
	hw_address=1,aa:bb:cc:0:0:1
}
{
	ip_address=192.168.64.9
	hw_address=1,aa:bb:cc:0:0:1
}`
	ip, ok := LookupIP([]byte(dup), "aa:bb:cc:00:00:01")
	if !ok || ip != "192.168.64.20" {
		t.Fatalf("want first (freshest) lease 192.168.64.20, got %q %v", ip, ok)
	}
}

func TestLookupIPFromFileContract(t *testing.T) {
	orig := leasesFile
	defer func() { leasesFile = orig }()

	leasesFile = filepath.Join(t.TempDir(), "missing")
	if ip, ok, err := LookupIPFromFile("a6:5e:00:11:02:33"); ip != "" || ok || err != nil {
		t.Fatalf("missing file: want empty/false/nil, got %q %v %v", ip, ok, err)
	}

	f := filepath.Join(t.TempDir(), "leases")
	if err := os.WriteFile(f, []byte(fixture), 0o600); err != nil {
		t.Fatal(err)
	}
	leasesFile = f
	if ip, ok, err := LookupIPFromFile("a6:5e:00:11:02:33"); ip != "192.168.64.5" || !ok || err != nil {
		t.Fatalf("hit: got %q %v %v", ip, ok, err)
	}
	if _, ok, err := LookupIPFromFile("ff:ff:ff:ff:ff:ff"); ok || err != nil {
		t.Fatalf("miss: got ok=%v err=%v", ok, err)
	}
}
