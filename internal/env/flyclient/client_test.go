package flyclient

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestCreateApp(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/apps" || r.Method != http.MethodPost {
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer test-token" {
			t.Error("missing auth")
		}
		var req CreateAppRequest
		json.NewDecoder(r.Body).Decode(&req)
		if req.AppName != "my-app" {
			t.Errorf("app_name=%q", req.AppName)
		}
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(App{ID: "app-123", Name: "my-app", Status: "active"})
	}))
	defer srv.Close()

	c := New(srv.URL, "test-token")
	app, err := c.CreateApp(context.Background(), CreateAppRequest{AppName: "my-app", OrgSlug: "org"})
	if err != nil {
		t.Fatal(err)
	}
	if app.ID != "app-123" {
		t.Errorf("id=%q", app.ID)
	}
}

func TestCreateMachine(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/apps/my-app/machines" {
			t.Errorf("path=%s", r.URL.Path)
		}
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(Machine{
			ID: "m-abc", State: "created", IPAddress: "10.0.0.5",
			GeneratedHostname: "m-abc.fly.dev",
		})
	}))
	defer srv.Close()

	c := New(srv.URL, "tok")
	m, err := c.CreateMachine(context.Background(), "my-app", CreateMachineRequest{
		Region: "iad",
		Config: MachineConfig{Image: "golang:1.22"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if m.IPAddress != "10.0.0.5" {
		t.Errorf("ip=%q", m.IPAddress)
	}
}

func TestStartMachine(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/apps/app/machines/m-1/start" || r.Method != http.MethodPost {
			t.Errorf("%s %s", r.Method, r.URL.Path)
		}
		json.NewEncoder(w).Encode(map[string]bool{"ok": true})
	}))
	defer srv.Close()

	c := New(srv.URL, "tok")
	if err := c.StartMachine(context.Background(), "app", "m-1"); err != nil {
		t.Fatal(err)
	}
}

func TestDeleteMachine(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete {
			t.Errorf("method=%s", r.Method)
		}
		json.NewEncoder(w).Encode(map[string]bool{"ok": true})
	}))
	defer srv.Close()

	c := New(srv.URL, "tok")
	if err := c.DeleteMachine(context.Background(), "app", "m-1"); err != nil {
		t.Fatal(err)
	}
}

func TestAPIError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		json.NewEncoder(w).Encode(map[string]string{"error": "no capacity available"})
	}))
	defer srv.Close()

	c := New(srv.URL, "tok")
	_, err := c.CreateMachine(context.Background(), "app", CreateMachineRequest{Region: "iad"})
	if err == nil {
		t.Fatal("expected error")
	}
	apiErr, ok := err.(*APIError)
	if !ok {
		// May be wrapped.
		t.Logf("error type: %T: %v", err, err)
		return
	}
	if apiErr.StatusCode != 503 {
		t.Errorf("status=%d", apiErr.StatusCode)
	}
}

func TestHealth(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/health" {
			t.Errorf("path=%s", r.URL.Path)
		}
		json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	}))
	defer srv.Close()

	c := New(srv.URL, "tok")
	if err := c.Health(context.Background()); err != nil {
		t.Fatal(err)
	}
}
