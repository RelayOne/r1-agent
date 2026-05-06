package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/RelayOne/r1/internal/daemondisco"
)

func TestServeBindsLoopbackEphemeralPort(t *testing.T) {
	home := t.TempDir()
	ln, err := ServeLoopback(LoopbackOptions{
		HomeDir:  home,
		SockPath: "/run/test/r1.sock",
		Token:    "deadbeef",
		Version:  "r1-test",
		PID:      os.Getpid(),
	})
	if err != nil {
		t.Fatalf("ServeLoopback: %v", err)
	}
	defer ln.Close()

	if ln.Port == 0 {
		t.Fatalf("Port = 0; expected ephemeral non-zero")
	}
	if !strings.HasPrefix(ln.Addr(), "127.0.0.1:") {
		t.Errorf("Addr = %q, want 127.0.0.1:N", ln.Addr())
	}

	// Discovery file written at home/daemon.json.
	disc, err := daemondisco.ReadDiscoveryFrom(home)
	if err != nil {
		t.Fatalf("ReadDiscoveryFrom: %v", err)
	}
	if disc.Port != ln.Port {
		t.Errorf("disc.Port = %d, want %d", disc.Port, ln.Port)
	}
	if disc.Token != "deadbeef" {
		t.Errorf("disc.Token = %q, want deadbeef", disc.Token)
	}
	if disc.SockPath != "/run/test/r1.sock" {
		t.Errorf("disc.SockPath = %q", disc.SockPath)
	}
	if disc.Version != "r1-test" {
		t.Errorf("disc.Version = %q, want r1-test", disc.Version)
	}
	if disc.PID != os.Getpid() {
		t.Errorf("disc.PID = %d, want %d", disc.PID, os.Getpid())
	}

	// The listener actually accepts.
	c, err := net.DialTimeout("tcp", ln.Addr(), 1*time.Second)
	if err != nil {
		t.Fatalf("dial loopback: %v", err)
	}
	_ = c.Close()
}

func TestServeMintsTokenWhenEmpty(t *testing.T) {
	home := t.TempDir()
	ln, err := ServeLoopback(LoopbackOptions{HomeDir: home})
	if err != nil {
		t.Fatalf("ServeLoopback: %v", err)
	}
	defer ln.Close()
	if len(ln.Token) != daemondisco.TokenBytes*2 {
		t.Errorf("auto-minted token len = %d, want %d", len(ln.Token), daemondisco.TokenBytes*2)
	}
}

func TestServeRandomEphemeralPortDistinct(t *testing.T) {
	// Two consecutive ServeLoopback calls in different "homes"
	// must get distinct ports. Defends against accidentally
	// hardcoding a port.
	home1, home2 := t.TempDir(), t.TempDir()
	ln1, err := ServeLoopback(LoopbackOptions{HomeDir: home1})
	if err != nil {
		t.Fatalf("ServeLoopback 1: %v", err)
	}
	defer ln1.Close()
	ln2, err := ServeLoopback(LoopbackOptions{HomeDir: home2})
	if err != nil {
		t.Fatalf("ServeLoopback 2: %v", err)
	}
	defer ln2.Close()
	if ln1.Port == ln2.Port {
		t.Errorf("two ephemeral binds returned same port %d; want distinct", ln1.Port)
	}
}

func TestServeHTTPRoundTrip(t *testing.T) {
	home := t.TempDir()
	ln, err := ServeLoopback(LoopbackOptions{HomeDir: home})
	if err != nil {
		t.Fatalf("ServeLoopback: %v", err)
	}
	defer ln.Close()

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{"ok": "yes"})
	})

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() { errCh <- ln.ServeHTTP(ctx, mux) }()

	// Hit the listener.
	resp, err := http.Get(fmt.Sprintf("http://%s/healthz", ln.Addr()))
	if err != nil {
		t.Fatalf("GET /healthz: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), `"ok":"yes"`) {
		t.Errorf("unexpected body: %s", body)
	}
	cancel()
	select {
	case err := <-errCh:
		if err != nil {
			t.Errorf("ServeHTTP returned: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("ServeHTTP did not exit after ctx cancel")
	}
}

func TestServeRemovesDiscoveryOnClose(t *testing.T) {
	home := t.TempDir()
	ln, err := ServeLoopback(LoopbackOptions{HomeDir: home})
	if err != nil {
		t.Fatalf("ServeLoopback: %v", err)
	}
	if _, err := os.Stat(ln.DiscoveryPath); err != nil {
		t.Fatalf("discovery file missing after start: %v", err)
	}
	if err := ln.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, err := os.Stat(filepath.Join(home, daemondisco.FileName)); !os.IsNotExist(err) {
		t.Errorf("discovery file lingers after Close: %v", err)
	}
}
