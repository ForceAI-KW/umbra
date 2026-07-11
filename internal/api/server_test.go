package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
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

func newTestServer(t *testing.T) (*httptest.Server, *fakeLC) {
	reg := registry.New(t.TempDir())
	lc := &fakeLC{states: map[string]vm.State{}}
	s := NewServer(reg, lc,
		func(ctx context.Context, m *registry.Machine) error { return nil },
		func(ctx context.Context, m *registry.Machine) (string, error) { return "192.168.64.7", nil })
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	return ts, lc
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
	ts, _ := newTestServer(t)

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
	ts, _ := newTestServer(t)
	resp := postJSON(t, ts.URL+"/v1/machines", map[string]any{"name": "Bad Name"})
	if resp.StatusCode != 400 {
		t.Fatalf("got %d, want 400", resp.StatusCode)
	}
}
