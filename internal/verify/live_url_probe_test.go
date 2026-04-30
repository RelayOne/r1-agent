package verify

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestLiveURLProbeRunSuccess(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("deploy ok"))
	}))
	defer srv.Close()

	probe := LiveURLProbe{
		URL:            srv.URL,
		ExpectedStatus: "200",
		BodyContains:   "deploy",
	}

	result, err := probe.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !result.Success || result.StatusCode != http.StatusOK {
		t.Fatalf("result = %+v, want success with 200", result)
	}
}

func TestLiveURLProbeRunStatusFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer srv.Close()

	probe := LiveURLProbe{URL: srv.URL}
	result, err := probe.Run(context.Background())
	if err == nil {
		t.Fatal("expected error")
	}
	if result.StatusCode != http.StatusInternalServerError {
		t.Fatalf("status code = %d, want 500", result.StatusCode)
	}
}

func TestLiveURLProbeRunSideEffectFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("deploy ok"))
	}))
	defer srv.Close()

	probe := LiveURLProbe{URL: srv.URL}
	probe.SetSideEffectCheck(func(context.Context) error {
		return errors.New("db query found no row")
	})

	if _, err := probe.Run(context.Background()); err == nil {
		t.Fatal("expected side-effect error")
	}
}
