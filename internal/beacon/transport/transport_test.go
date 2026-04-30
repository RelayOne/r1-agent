package transport

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHTTPRoundTrip(t *testing.T) {
	t.Parallel()
	got := make(chan Envelope, 1)
	srv := httptest.NewServer(HTTPHandler(func(ctx context.Context, env Envelope) error {
		got <- env
		return nil
	}))
	defer srv.Close()

	env := Envelope{Channel: ChannelTrustSignal, Body: json.RawMessage(`{"kind":"pause"}`)}
	if err := Post(context.Background(), srv.Client(), srv.URL, env); err != nil {
		t.Fatalf("Post() error = %v", err)
	}
	select {
	case seen := <-got:
		if seen.Channel != ChannelTrustSignal {
			t.Fatalf("channel = %q, want %q", seen.Channel, ChannelTrustSignal)
		}
	default:
		t.Fatal("handler did not receive envelope")
	}
}

func TestWebSocketRoundTrip(t *testing.T) {
	t.Parallel()
	got := make(chan Envelope, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := ServeWS(w, r, func(ctx context.Context, env Envelope) error {
			got <- env
			return nil
		}); err != nil {
			t.Errorf("ServeWS() error = %v", err)
		}
	}))
	defer srv.Close()

	env := Envelope{Channel: ChannelSessionFrame, Session: "sess-1", Body: json.RawMessage(`{"counter":1}`)}
	url := "ws" + srv.URL[len("http"):]
	if err := DialAndSendWS(context.Background(), url, env); err != nil {
		t.Fatalf("DialAndSendWS() error = %v", err)
	}
	select {
	case seen := <-got:
		if seen.Session != "sess-1" {
			t.Fatalf("session = %q, want sess-1", seen.Session)
		}
	default:
		t.Fatal("handler did not receive websocket envelope")
	}
}
