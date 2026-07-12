package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/ForceAI-KW/umbra/internal/registry"
	"github.com/ForceAI-KW/umbra/internal/vm"
)

type fakeLC struct{ states map[string]vm.State }

func (f *fakeLC) Start(ctx context.Context, n string) error {
	f.states[n] = vm.StateRunning
	return nil
}
func (f *fakeLC) Stop(ctx context.Context, n string) error { f.states[n] = vm.StateStopped; return nil }
func (f *fakeLC) Info(n string) vm.Info {
	s, ok := f.states[n]
	if !ok {
		s = vm.StateStopped
	}
	info := vm.Info{Name: n, State: s}
	if s == vm.StateRunning {
		info.IP = "192.168.64.7"
	}
	return info
}
func (f *fakeLC) List() []vm.Info { return nil }

// fakeForwarder is an in-memory Forwarder fake: it records every
// Expose/Unexpose call and keeps a live set for Forwards() to report.
type fakeForwarder struct {
	mu        sync.Mutex
	exposed   []exposeCall
	unexposed []unexposeCall
	forwards  []ForwardView
}

type exposeCall struct{ Protocol, Local, Remote string }
type unexposeCall struct{ Protocol, Local string }

func (f *fakeForwarder) Expose(protocol, local, remote string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.exposed = append(f.exposed, exposeCall{protocol, local, remote})
	f.forwards = append(f.forwards, ForwardView{Local: local, Remote: remote, Protocol: protocol})
	return nil
}

func (f *fakeForwarder) Unexpose(protocol, local string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.unexposed = append(f.unexposed, unexposeCall{protocol, local})
	kept := f.forwards[:0]
	for _, fw := range f.forwards {
		if fw.Protocol == protocol && fw.Local == local {
			continue
		}
		kept = append(kept, fw)
	}
	f.forwards = kept
	return nil
}

func (f *fakeForwarder) Forwards() ([]ForwardView, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]ForwardView(nil), f.forwards...), nil
}

func newTestServer(t *testing.T) (*httptest.Server, *fakeLC, *fakeForwarder) {
	reg := registry.New(t.TempDir())
	lc := &fakeLC{states: map[string]vm.State{}}
	fwd := &fakeForwarder{}
	s := NewServer(reg, lc,
		func(ctx context.Context, m *registry.Machine) error { return nil },
		func(ctx context.Context, m *registry.Machine) (string, error) { return "192.168.64.7", nil },
		fwd)
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	return ts, lc, fwd
}

func postJSON(t *testing.T, url string, body any) *http.Response {
	b, _ := json.Marshal(body)
	resp, err := http.Post(url, "application/json", bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func TestCreateStartStopDeleteFlow(t *testing.T) {
	ts, _, _ := newTestServer(t)

	resp := postJSON(t, ts.URL+"/v1/machines", map[string]any{
		"name": "t1", "cpus": 2, "memory_mib": 2048, "disk_gib": 20})
	if resp.StatusCode != 201 {
		t.Fatalf("create: %d", resp.StatusCode)
	}

	resp = postJSON(t, ts.URL+"/v1/machines/t1/start", nil)
	if resp.StatusCode != 200 {
		t.Fatalf("start: %d", resp.StatusCode)
	}
	var info vm.Info
	json.NewDecoder(resp.Body).Decode(&info)
	if info.IP != "192.168.64.7" || info.State != vm.StateRunning {
		t.Fatalf("start info: %+v", info)
	}

	req, _ := http.NewRequest(http.MethodDelete, ts.URL+"/v1/machines/t1", nil)
	resp, _ = http.DefaultClient.Do(req)
	if resp.StatusCode != 409 {
		t.Fatalf("delete-while-running: %d, want 409", resp.StatusCode)
	}

	resp = postJSON(t, ts.URL+"/v1/machines/t1/stop", nil)
	if resp.StatusCode != 200 {
		t.Fatalf("stop: %d", resp.StatusCode)
	}
	resp, _ = http.DefaultClient.Do(req)
	if resp.StatusCode != 204 {
		t.Fatalf("delete: %d", resp.StatusCode)
	}
}

func TestCreateRejectsInvalidName(t *testing.T) {
	ts, _, _ := newTestServer(t)
	resp := postJSON(t, ts.URL+"/v1/machines", map[string]any{"name": "Bad Name"})
	if resp.StatusCode != 400 {
		t.Fatalf("got %d, want 400", resp.StatusCode)
	}
}

// TestDoubleStartReturns200Both covers finding 5: Start is idempotent, so
// calling start twice in a row must return 200 both times, not error on the
// second call.
func TestDoubleStartReturns200Both(t *testing.T) {
	ts, _, _ := newTestServer(t)
	resp := postJSON(t, ts.URL+"/v1/machines", map[string]any{"name": "t2"})
	if resp.StatusCode != 201 {
		t.Fatalf("create: %d", resp.StatusCode)
	}

	resp = postJSON(t, ts.URL+"/v1/machines/t2/start", nil)
	if resp.StatusCode != 200 {
		t.Fatalf("first start: %d, want 200", resp.StatusCode)
	}
	resp = postJSON(t, ts.URL+"/v1/machines/t2/start", nil)
	if resp.StatusCode != 200 {
		t.Fatalf("second start: %d, want 200", resp.StatusCode)
	}
}

// TestCreateDuplicateReturns409 covers finding 5: creating a machine whose
// name already exists in the registry must be rejected with 409, not
// silently overwrite or 500.
func TestCreateDuplicateReturns409(t *testing.T) {
	ts, _, _ := newTestServer(t)
	resp := postJSON(t, ts.URL+"/v1/machines", map[string]any{"name": "t3"})
	if resp.StatusCode != 201 {
		t.Fatalf("first create: %d, want 201", resp.StatusCode)
	}
	resp = postJSON(t, ts.URL+"/v1/machines", map[string]any{"name": "t3"})
	if resp.StatusCode != 409 {
		t.Fatalf("duplicate create: %d, want 409", resp.StatusCode)
	}
}

// fakeZombieLC always reports a crashed-with-unconfirmed-stop (zombie)
// machine, regardless of name.
type fakeZombieLC struct{}

func (fakeZombieLC) Start(ctx context.Context, n string) error { return nil }
func (fakeZombieLC) Stop(ctx context.Context, n string) error  { return nil }
func (fakeZombieLC) Info(n string) vm.Info {
	return vm.Info{Name: n, State: vm.StateCrashed, Zombie: true}
}
func (fakeZombieLC) List() []vm.Info { return nil }

// TestDeleteZombieMachineReturns409 covers finding 4: a machine whose stop
// was never confirmed (State=Crashed, handle still live) must refuse
// delete just like a running machine — it may still be alive.
func TestDeleteZombieMachineReturns409(t *testing.T) {
	reg := registry.New(t.TempDir())
	s := NewServer(reg, fakeZombieLC{},
		func(ctx context.Context, m *registry.Machine) error { return nil },
		func(ctx context.Context, m *registry.Machine) (string, error) { return "", nil },
		&fakeForwarder{})
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)

	resp := postJSON(t, ts.URL+"/v1/machines", map[string]any{"name": "z1"})
	if resp.StatusCode != 201 {
		t.Fatalf("create: %d", resp.StatusCode)
	}

	req, _ := http.NewRequest(http.MethodDelete, ts.URL+"/v1/machines/z1", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 409 {
		t.Fatalf("delete zombie: %d, want 409", resp.StatusCode)
	}
	var e struct {
		Error string `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&e); err != nil {
		t.Fatal(err)
	}
	want := `machine "z1" crashed with an unconfirmed stop; run stop again before delete`
	if e.Error != want {
		t.Fatalf("error message = %q, want %q", e.Error, want)
	}
}

// newForwardTestServer is like newTestServer but its Provisioner assigns and
// persists a fixed IP (matching fakeLC.Info's hardcoded running-state IP),
// so forward tests have a real machine.IP to build local<->remote pairs
// from and to filter GET /forwards by.
func newForwardTestServer(t *testing.T) (*httptest.Server, *fakeLC, *fakeForwarder) {
	reg := registry.New(t.TempDir())
	lc := &fakeLC{states: map[string]vm.State{}}
	fwd := &fakeForwarder{}
	s := NewServer(reg, lc,
		func(ctx context.Context, m *registry.Machine) error {
			m.IP = "192.168.64.7"
			return reg.Save(m)
		},
		func(ctx context.Context, m *registry.Machine) (string, error) { return "192.168.64.7", nil },
		fwd)
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	return ts, lc, fwd
}

func TestForwardAddOnRunningMachine(t *testing.T) {
	ts, _, fwd := newForwardTestServer(t)
	if resp := postJSON(t, ts.URL+"/v1/machines", map[string]any{"name": "f1"}); resp.StatusCode != 201 {
		t.Fatalf("create: %d", resp.StatusCode)
	}
	if resp := postJSON(t, ts.URL+"/v1/machines/f1/start", nil); resp.StatusCode != 200 {
		t.Fatalf("start: %d", resp.StatusCode)
	}

	resp := postJSON(t, ts.URL+"/v1/machines/f1/forwards", map[string]any{"local_port": 2222, "guest_port": 22, "protocol": "tcp"})
	if resp.StatusCode != 201 {
		t.Fatalf("forward add: %d", resp.StatusCode)
	}
	var fv ForwardView
	if err := json.NewDecoder(resp.Body).Decode(&fv); err != nil {
		t.Fatal(err)
	}
	if fv.Local != "127.0.0.1:2222" || fv.Remote != "192.168.64.7:22" || fv.Protocol != "tcp" {
		t.Fatalf("forward view = %+v", fv)
	}

	if len(fwd.exposed) != 1 {
		t.Fatalf("want 1 Expose call, got %d", len(fwd.exposed))
	}
	if got := fwd.exposed[0]; got.Protocol != "tcp" || got.Local != "127.0.0.1:2222" || got.Remote != "192.168.64.7:22" {
		t.Fatalf("Expose called with %+v", got)
	}
}

func TestForwardAddDefaultsToTCP(t *testing.T) {
	ts, _, fwd := newForwardTestServer(t)
	postJSON(t, ts.URL+"/v1/machines", map[string]any{"name": "f2"})
	postJSON(t, ts.URL+"/v1/machines/f2/start", nil)

	resp := postJSON(t, ts.URL+"/v1/machines/f2/forwards", map[string]any{"local_port": 8080, "guest_port": 80})
	if resp.StatusCode != 201 {
		t.Fatalf("forward add: %d", resp.StatusCode)
	}
	if len(fwd.exposed) != 1 || fwd.exposed[0].Protocol != "tcp" {
		t.Fatalf("want default tcp protocol, got %+v", fwd.exposed)
	}
}

func TestForwardAddOnStoppedMachineReturns409(t *testing.T) {
	ts, _, _ := newForwardTestServer(t)
	postJSON(t, ts.URL+"/v1/machines", map[string]any{"name": "f3"})

	resp := postJSON(t, ts.URL+"/v1/machines/f3/forwards", map[string]any{"local_port": 2222, "guest_port": 22})
	if resp.StatusCode != 409 {
		t.Fatalf("forward add on stopped machine: %d, want 409", resp.StatusCode)
	}
}

func TestForwardAddOnMissingMachineReturns404(t *testing.T) {
	ts, _, _ := newForwardTestServer(t)
	resp := postJSON(t, ts.URL+"/v1/machines/nope/forwards", map[string]any{"local_port": 2222, "guest_port": 22})
	if resp.StatusCode != 404 {
		t.Fatalf("forward add on missing machine: %d, want 404", resp.StatusCode)
	}
}

func TestForwardRemove(t *testing.T) {
	ts, _, fwd := newForwardTestServer(t)
	postJSON(t, ts.URL+"/v1/machines", map[string]any{"name": "f4"})
	postJSON(t, ts.URL+"/v1/machines/f4/start", nil)
	postJSON(t, ts.URL+"/v1/machines/f4/forwards", map[string]any{"local_port": 2222, "guest_port": 22})

	req, _ := http.NewRequest(http.MethodDelete, ts.URL+"/v1/machines/f4/forwards/2222", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 204 {
		t.Fatalf("forward delete: %d, want 204", resp.StatusCode)
	}
	if len(fwd.unexposed) != 1 {
		t.Fatalf("want 1 Unexpose call, got %d", len(fwd.unexposed))
	}
	if got := fwd.unexposed[0]; got.Protocol != "tcp" || got.Local != "127.0.0.1:2222" {
		t.Fatalf("Unexpose called with %+v", got)
	}
}

func TestForwardRemoveMissingMachineReturns404(t *testing.T) {
	ts, _, _ := newForwardTestServer(t)
	req, _ := http.NewRequest(http.MethodDelete, ts.URL+"/v1/machines/nope/forwards/2222", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 404 {
		t.Fatalf("forward delete on missing machine: %d, want 404", resp.StatusCode)
	}
}

func TestForwardListFiltersByMachine(t *testing.T) {
	ts, _, fwd := newForwardTestServer(t)
	postJSON(t, ts.URL+"/v1/machines", map[string]any{"name": "f5"})
	postJSON(t, ts.URL+"/v1/machines/f5/start", nil)
	postJSON(t, ts.URL+"/v1/machines/f5/forwards", map[string]any{"local_port": 2222, "guest_port": 22})

	// A forward belonging to some other machine's guest IP must not appear.
	fwd.mu.Lock()
	fwd.forwards = append(fwd.forwards, ForwardView{Local: "127.0.0.1:9999", Remote: "192.168.64.99:22", Protocol: "tcp"})
	fwd.mu.Unlock()

	resp, err := http.Get(ts.URL + "/v1/machines/f5/forwards")
	if err != nil {
		t.Fatal(err)
	}
	var list []ForwardView
	if err := json.NewDecoder(resp.Body).Decode(&list); err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].Remote != "192.168.64.7:22" {
		t.Fatalf("filtered list = %+v", list)
	}
}
