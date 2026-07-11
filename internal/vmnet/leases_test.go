package vmnet

import "testing"

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
