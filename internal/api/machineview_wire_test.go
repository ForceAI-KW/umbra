package api

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/ForceAI-KW/umbra/internal/client"
	"github.com/ForceAI-KW/umbra/internal/registry"
	"github.com/ForceAI-KW/umbra/internal/vm"
)

// bootingLC is a Lifecycle whose machines are RUNNING but carry no readiness
// IP — the state every healthy guest is in for its whole ~90s boot window, and
// the exact state the wire had to carry a configured address through.
type bootingLC struct{ state vm.State }

func (b bootingLC) Start(context.Context, string) error { return nil }
func (b bootingLC) Stop(context.Context, string) error  { return nil }
func (b bootingLC) List() []vm.Info                     { return nil }
func (b bootingLC) Info(n string) vm.Info {
	// IP deliberately empty: umbrad publishes it only after readiness.
	return vm.Info{Name: n, State: b.state}
}

// THE TEST FIVE WAVES DID NOT HAVE. Every previous test for the
// configured-vs-runtime address distinction built a client.MachineView IN
// MEMORY, so it never exercised encoding/json — and encoding/json is precisely
// where the bug lived. registry.Machine.IP is tagged `json:"ip"`, and BOTH
// MachineView types declare their own shallower `IP` with the same tag, so the
// embedded field is silently dropped in each direction. The configured address
// therefore arrived at the CLI as "" on every real run, convicting every
// booting guest as `guest-no-ip` with a destroy-and-recreate instruction.
//
// This test crosses the real boundary: build the server-side view, marshal it
// exactly as the API does, unmarshal into the CLI's own type, and assert the
// configured address is still there. Any future field that tries to ride on
// the embedded registry.Machine under a colliding tag fails here.
func TestConfiguredIPSurvivesTheServerToClientWire(t *testing.T) {
	m := &registry.Machine{
		Name: "fwb-ci5",
		MAC:  "aa:bb:cc:dd:ee:01",
		IP:   "192.168.127.10", // the registry-configured address
	}
	s := &Server{lc: bootingLC{state: vm.StateRunning}}

	wire, err := json.Marshal(s.view(m))
	if err != nil {
		t.Fatalf("marshal server view: %v", err)
	}

	var got client.MachineView
	if err := json.Unmarshal(wire, &got); err != nil {
		t.Fatalf("unmarshal into client view: %v", err)
	}

	if got.ConfiguredIP != "192.168.127.10" {
		t.Fatalf("ConfiguredIP = %q after the round trip, want %q\nwire payload: %s",
			got.ConfiguredIP, "192.168.127.10", wire)
	}
	// The runtime address must stay independently empty — collapsing the two
	// back together is the whole defect.
	if got.IP != "" {
		t.Errorf("runtime IP = %q, want empty for a guest that has not passed readiness", got.IP)
	}
	if got.State != vm.StateRunning {
		t.Errorf("State = %q, want running", got.State)
	}
}

// The configured address must travel under a DISTINCT json key. Asserting on
// the key, not just the decoded value, is what stops someone "simplifying"
// this back into a collision that the value assertion above would still pass
// if both fields happened to hold the same address.
func TestConfiguredIPUsesADistinctWireKey(t *testing.T) {
	m := &registry.Machine{Name: "g", IP: "192.168.127.10"}
	s := &Server{lc: bootingLC{state: vm.StateRunning}}

	wire, err := json.Marshal(s.view(m))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var raw map[string]any
	if err := json.Unmarshal(wire, &raw); err != nil {
		t.Fatalf("unmarshal raw: %v", err)
	}
	if raw["configured_ip"] != "192.168.127.10" {
		t.Fatalf("payload has no distinct configured_ip key: %s", wire)
	}
	// Additive only: the watchdog reads `ip` as the runtime address and that
	// meaning must not shift.
	if _, present := raw["ip"]; present {
		t.Errorf("runtime ip must be omitted when empty, got: %s", wire)
	}
}

// A STOPPED machine still has a configured address. This is the shape the live
// API was observed to emit with no "ip" key at all.
func TestConfiguredIPSurvivesForAStoppedMachine(t *testing.T) {
	m := &registry.Machine{Name: "fwb-ci2", IP: "192.168.127.11"}
	s := &Server{lc: bootingLC{state: vm.StateStopped}}

	wire, err := json.Marshal(s.view(m))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got client.MachineView
	if err := json.Unmarshal(wire, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.ConfiguredIP != "192.168.127.11" {
		t.Fatalf("ConfiguredIP = %q for a stopped machine, want %q", got.ConfiguredIP, "192.168.127.11")
	}
}
