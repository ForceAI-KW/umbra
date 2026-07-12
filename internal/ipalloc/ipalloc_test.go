package ipalloc

import "testing"

func TestAllocateSkipsUsedAndGateway(t *testing.T) {
	ip, err := Allocate("192.168.127.0/24", "192.168.127.1", 10, []string{"192.168.127.10", "192.168.127.11"})
	if err != nil {
		t.Fatal(err)
	}
	if ip != "192.168.127.12" {
		t.Fatalf("got %s, want 192.168.127.12", ip)
	}
}

func TestAllocateFirstFree(t *testing.T) {
	ip, err := Allocate("192.168.127.0/24", "192.168.127.1", 10, nil)
	if err != nil || ip != "192.168.127.10" {
		t.Fatalf("got %s %v", ip, err)
	}
}

func TestValidateRejectsOutOfSubnetAndGateway(t *testing.T) {
	if Validate("192.168.127.0/24", "192.168.127.1", "192.168.127.1") == nil {
		t.Fatal("gateway must be rejected")
	}
	if Validate("192.168.127.0/24", "192.168.127.1", "10.0.0.5") == nil {
		t.Fatal("out-of-subnet must be rejected")
	}
	if err := Validate("192.168.127.0/24", "192.168.127.1", "192.168.127.10"); err != nil {
		t.Fatalf("valid ip rejected: %v", err)
	}
}
