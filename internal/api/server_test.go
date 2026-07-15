package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
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

// fakeDocker is an in-memory Docker fake: Install/Start/Stop/Uninstall
// mutate a tiny bit of state so tests can assert the happy paths and error
// cases (e.g. Start before Install) without a real dockerbridge/dockerctx.
type fakeDocker struct {
	installed bool
	running   bool
}

func (f *fakeDocker) Install(ctx context.Context) (MachineView, error) {
	f.installed = true
	return MachineView{Machine: registry.Machine{Name: "docker", Role: "docker", CPUs: 2, MemoryMiB: 4096, DiskGiB: 40}}, nil
}
func (f *fakeDocker) Start(ctx context.Context) (MachineView, error) {
	if !f.installed {
		return MachineView{}, errors.New("docker not installed")
	}
	f.running = true
	return MachineView{Machine: registry.Machine{Name: "docker", Role: "docker"}, State: vm.StateRunning, IP: "192.168.127.10"}, nil
}
func (f *fakeDocker) Stop(ctx context.Context) error {
	f.running = false
	return nil
}
func (f *fakeDocker) Status(ctx context.Context) DockerStatus {
	return DockerStatus{Installed: f.installed, Running: f.running, IP: "192.168.127.10", Socket: "/tmp/docker.sock", ContextCurrent: f.running}
}
func (f *fakeDocker) Uninstall(ctx context.Context) error {
	f.installed, f.running = false, false
	return nil
}

func newTestServer(t *testing.T) (*httptest.Server, *fakeLC, *fakeForwarder) {
	ts, lc, fwd, _ := newTestServerWithDocker(t)
	return ts, lc, fwd
}

func newTestServerWithDocker(t *testing.T) (*httptest.Server, *fakeLC, *fakeForwarder, *fakeDocker) {
	reg := registry.New(t.TempDir())
	lc := &fakeLC{states: map[string]vm.State{}}
	fwd := &fakeForwarder{}
	dk := &fakeDocker{}
	s := NewServer(reg, lc,
		func(ctx context.Context, m *registry.Machine) error { return nil },
		func(ctx context.Context, m *registry.Machine) (string, error) { return "192.168.64.7", nil },
		fwd, dk, func() string { return "installed" })
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	return ts, lc, fwd, dk
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
		&fakeForwarder{}, &fakeDocker{}, func() string { return "installed" })
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
		fwd, &fakeDocker{}, func() string { return "installed" })
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

func TestForwardAddRejectsBadPortAndProtocol(t *testing.T) {
	ts, _, _ := newForwardTestServer(t)
	postJSON(t, ts.URL+"/v1/machines", map[string]any{"name": "fp"})
	postJSON(t, ts.URL+"/v1/machines/fp/start", nil)

	for _, body := range []map[string]any{
		{"local_port": 999999, "guest_port": 22},
		{"local_port": 2222, "guest_port": 0},
		{"local_port": 2222, "guest_port": 22, "protocol": "icmp"},
	} {
		if resp := postJSON(t, ts.URL+"/v1/machines/fp/forwards", body); resp.StatusCode != 400 {
			t.Fatalf("body %v: got %d, want 400", body, resp.StatusCode)
		}
	}
}

func TestForwardDeleteValidatesPortAndProtocol(t *testing.T) {
	ts, _, _ := newForwardTestServer(t)
	postJSON(t, ts.URL+"/v1/machines", map[string]any{"name": "fd"})

	req, _ := http.NewRequest(http.MethodDelete, ts.URL+"/v1/machines/fd/forwards/70000", nil)
	if resp, _ := http.DefaultClient.Do(req); resp.StatusCode != 400 {
		t.Fatalf("out-of-range port: got %d, want 400", resp.StatusCode)
	}
	req, _ = http.NewRequest(http.MethodDelete, ts.URL+"/v1/machines/fd/forwards/2222?protocol=icmp", nil)
	if resp, _ := http.DefaultClient.Do(req); resp.StatusCode != 400 {
		t.Fatalf("bad protocol: got %d, want 400", resp.StatusCode)
	}
}

func TestForwardRemoveRejectsUnownedPort(t *testing.T) {
	ts, _, _ := newForwardTestServer(t)
	postJSON(t, ts.URL+"/v1/machines", map[string]any{"name": "fo"})
	postJSON(t, ts.URL+"/v1/machines/fo/start", nil)
	// No forward exposed for fo — delete must 404, not blindly unexpose.
	req, _ := http.NewRequest(http.MethodDelete, ts.URL+"/v1/machines/fo/forwards/2222", nil)
	resp, _ := http.DefaultClient.Do(req)
	if resp.StatusCode != 404 {
		t.Fatalf("delete unowned forward: %d, want 404", resp.StatusCode)
	}
}

// TestCreateRejectsReservedDockerName covers the guard added for Task 5: the
// "docker" name is reserved for the umbra-managed docker VM, so a normal
// create must 400 with the documented hint, not silently create a machine
// that would collide with `umbra docker install`.
func TestCreateRejectsReservedDockerName(t *testing.T) {
	ts, _, _ := newTestServer(t)
	resp := postJSON(t, ts.URL+"/v1/machines", map[string]any{"name": "docker"})
	if resp.StatusCode != 400 {
		t.Fatalf("create %q: got %d, want 400", "docker", resp.StatusCode)
	}
	var e struct {
		Error string `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&e); err != nil {
		t.Fatal(err)
	}
	want := `"docker" is reserved — use 'umbra docker install'`
	if e.Error != want {
		t.Fatalf("error message = %q, want %q", e.Error, want)
	}
}

// TestListMachinesExcludesOnlyReservedDockerRole covers the visibility rule
// from research §4/§8 (Task 7 fix): only the reserved docker VM
// (Role == registry.ReservedDockerName) is hidden from the normal machines
// list. A ci-runner machine is a normal, user-visible machine and must
// appear — it's not an implementation detail like the docker VM.
func TestListMachinesExcludesOnlyReservedDockerRole(t *testing.T) {
	reg := registry.New(t.TempDir())
	lc := &fakeLC{states: map[string]vm.State{}}
	s := NewServer(reg, lc,
		func(ctx context.Context, m *registry.Machine) error { return nil },
		func(ctx context.Context, m *registry.Machine) (string, error) { return "192.168.64.7", nil },
		&fakeForwarder{}, &fakeDocker{}, func() string { return "installed" })
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)

	// Seed a normal machine and a ci-runner machine via the API, and the
	// reserved docker machine directly in the registry (its own create path
	// is POST /v1/docker/install, not POST /v1/machines — the API create
	// handler rejects the name).
	if resp := postJSON(t, ts.URL+"/v1/machines", map[string]any{"name": "dev"}); resp.StatusCode != 201 {
		t.Fatalf("create dev: %d", resp.StatusCode)
	}
	if resp := postJSON(t, ts.URL+"/v1/machines", map[string]any{"name": "fwb-ci2", "role": "ci-runner"}); resp.StatusCode != 201 {
		t.Fatalf("create fwb-ci2: %d", resp.StatusCode)
	}
	if err := reg.Save(&registry.Machine{Name: "docker", Role: "docker", CPUs: 2, MemoryMiB: 4096, DiskGiB: 40}); err != nil {
		t.Fatal(err)
	}

	resp, err := http.Get(ts.URL + "/v1/machines")
	if err != nil {
		t.Fatal(err)
	}
	var list []MachineView
	if err := json.NewDecoder(resp.Body).Decode(&list); err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, m := range list {
		names[m.Name] = true
	}
	if len(list) != 2 || !names["dev"] || !names["fwb-ci2"] {
		t.Fatalf("list = %+v, want [dev, fwb-ci2] (docker VM hidden, ci-runner visible)", list)
	}
}

// TestCreateWithCIRunnerRole covers Task 7: `--role ci-runner` must create
// successfully (201) and the returned machine must carry the role.
func TestCreateWithCIRunnerRole(t *testing.T) {
	ts, _, _ := newTestServer(t)
	resp := postJSON(t, ts.URL+"/v1/machines", map[string]any{"name": "fwb-ci2", "role": "ci-runner"})
	if resp.StatusCode != 201 {
		t.Fatalf("create with role ci-runner: %d, want 201", resp.StatusCode)
	}
	var mv MachineView
	if err := json.NewDecoder(resp.Body).Decode(&mv); err != nil {
		t.Fatal(err)
	}
	if mv.Role != "ci-runner" {
		t.Fatalf("role = %q, want ci-runner", mv.Role)
	}
}

// TestCreateRejectsDockerRole covers Task 7: the "docker" role is reserved
// for the machine created via `umbra docker install` — a normal create
// request must not be able to claim it, even if the name isn't "docker".
func TestCreateRejectsDockerRole(t *testing.T) {
	ts, _, _ := newTestServer(t)
	resp := postJSON(t, ts.URL+"/v1/machines", map[string]any{"name": "sneaky", "role": "docker"})
	if resp.StatusCode != 400 {
		t.Fatalf("create with role docker: %d, want 400", resp.StatusCode)
	}
	var e struct {
		Error string `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&e); err != nil {
		t.Fatal(err)
	}
	want := "invalid role (only 'ci-runner' allowed)"
	if e.Error != want {
		t.Fatalf("error message = %q, want %q", e.Error, want)
	}
}

func TestDockerInstallStartStopStatusUninstall(t *testing.T) {
	ts, _, _, dk := newTestServerWithDocker(t)

	resp := postJSON(t, ts.URL+"/v1/docker/install", nil)
	if resp.StatusCode != 201 {
		t.Fatalf("install: %d, want 201", resp.StatusCode)
	}
	var mv MachineView
	if err := json.NewDecoder(resp.Body).Decode(&mv); err != nil {
		t.Fatal(err)
	}
	if mv.Name != "docker" || mv.Role != "docker" {
		t.Fatalf("install response = %+v", mv)
	}
	if !dk.installed {
		t.Fatal("fakeDocker.installed should be true after install")
	}

	resp = postJSON(t, ts.URL+"/v1/docker/start", nil)
	if resp.StatusCode != 200 {
		t.Fatalf("start: %d, want 200", resp.StatusCode)
	}
	mv = MachineView{}
	if err := json.NewDecoder(resp.Body).Decode(&mv); err != nil {
		t.Fatal(err)
	}
	if mv.IP != "192.168.127.10" || mv.State != vm.StateRunning {
		t.Fatalf("start response = %+v", mv)
	}

	resp, err := http.Get(ts.URL + "/v1/docker/status")
	if err != nil {
		t.Fatal(err)
	}
	var st DockerStatus
	if err := json.NewDecoder(resp.Body).Decode(&st); err != nil {
		t.Fatal(err)
	}
	if !st.Installed || !st.Running || st.IP != "192.168.127.10" {
		t.Fatalf("status = %+v", st)
	}

	resp = postJSON(t, ts.URL+"/v1/docker/stop", nil)
	if resp.StatusCode != 204 {
		t.Fatalf("stop: %d, want 204", resp.StatusCode)
	}
	if dk.running {
		t.Fatal("fakeDocker.running should be false after stop")
	}

	resp = postJSON(t, ts.URL+"/v1/docker/uninstall", nil)
	if resp.StatusCode != 204 {
		t.Fatalf("uninstall: %d, want 204", resp.StatusCode)
	}
	if dk.installed {
		t.Fatal("fakeDocker.installed should be false after uninstall")
	}
}

// TestDockerStartBeforeInstallReturns500 covers the fake's error path: the
// interface reports a plain error (no special typing required at this
// layer), which the route surfaces as a 500 with the error message.
func TestDockerStartBeforeInstallReturns500(t *testing.T) {
	ts, _, _, _ := newTestServerWithDocker(t)
	resp := postJSON(t, ts.URL+"/v1/docker/start", nil)
	if resp.StatusCode != 500 {
		t.Fatalf("start before install: %d, want 500", resp.StatusCode)
	}
}

// newPatchTestServer builds a server with a directly-accessible registry and
// fakeLC so PATCH tests can seed machine state (reg.Save/Load) and running
// state (lc.states) without going through the HTTP create/start routes.
func newPatchTestServer(t *testing.T) (*httptest.Server, *registry.Registry, *fakeLC) {
	reg := registry.New(t.TempDir())
	lc := &fakeLC{states: map[string]vm.State{}}
	s := NewServer(reg, lc,
		func(ctx context.Context, m *registry.Machine) error { return nil },
		func(ctx context.Context, m *registry.Machine) (string, error) { return "192.168.64.7", nil },
		&fakeForwarder{}, &fakeDocker{}, func() string { return "installed" })
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	return ts, reg, lc
}

func patchJSON(t *testing.T, url string, body string) *http.Response {
	req, err := http.NewRequest(http.MethodPatch, url, bytes.NewReader([]byte(body)))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

// TestPatchMachineAutostartWhileRunning covers Task 2: autostart is mutable
// even while the machine is running (unlike cpu/memory/disk).
func TestPatchMachineAutostartWhileRunning(t *testing.T) {
	ts, reg, lc := newPatchTestServer(t)
	reg.Save(&registry.Machine{Name: "ci", CPUs: 2, MemoryMiB: 1024, DiskGiB: 10})
	lc.states["ci"] = vm.StateRunning

	resp := patchJSON(t, ts.URL+"/v1/machines/ci", `{"autostart":true}`)
	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("code=%d body=%s", resp.StatusCode, body)
	}
	m, err := reg.Load("ci")
	if err != nil {
		t.Fatal(err)
	}
	if !m.Autostart {
		t.Fatal("autostart not persisted")
	}
}

// TestPatchMachineResizeRefusedWhileRunning covers Task 2: cpu/memory/disk
// changes require the machine stopped — a running machine must 409.
func TestPatchMachineResizeRefusedWhileRunning(t *testing.T) {
	ts, reg, lc := newPatchTestServer(t)
	reg.Save(&registry.Machine{Name: "ci", CPUs: 2, MemoryMiB: 1024, DiskGiB: 10})
	lc.states["ci"] = vm.StateRunning

	resp := patchJSON(t, ts.URL+"/v1/machines/ci", `{"memory_mib":4096}`)
	if resp.StatusCode != 409 {
		t.Fatalf("want 409, got %d", resp.StatusCode)
	}
}

// TestPatchMachineDiskShrinkRefused covers Task 2: disk can only grow, never
// shrink (the guest filesystem can't be safely shrunk from the host side).
func TestPatchMachineDiskShrinkRefused(t *testing.T) {
	ts, reg, _ := newPatchTestServer(t)
	reg.Save(&registry.Machine{Name: "ci", CPUs: 2, MemoryMiB: 1024, DiskGiB: 60})

	resp := patchJSON(t, ts.URL+"/v1/machines/ci", `{"disk_gib":30}`)
	if resp.StatusCode != 400 {
		t.Fatalf("want 400, got %d", resp.StatusCode)
	}
}

func TestRosettaStatus(t *testing.T) {
	reg := registry.New(t.TempDir())
	lc := &fakeLC{states: map[string]vm.State{}}
	s := NewServer(reg, lc,
		func(ctx context.Context, m *registry.Machine) error { return nil },
		func(ctx context.Context, m *registry.Machine) (string, error) { return "192.168.64.7", nil },
		&fakeForwarder{}, &fakeDocker{}, func() string { return "notInstalled" })
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)

	resp, err := http.Get(ts.URL + "/v1/rosetta")
	if err != nil {
		t.Fatal(err)
	}
	var out struct {
		Available string `json:"available"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if out.Available != "notInstalled" {
		t.Fatalf("available = %q, want notInstalled", out.Available)
	}
}
