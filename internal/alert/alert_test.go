package alert

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"uppies/internal/store"
)

func TestText(t *testing.T) {
	tests := []struct {
		name  string
		event Event
		want  string
	}{
		{"down with a reason", Event{Project: "acme", Monitor: "api", Down: true, Error: "status 503"}, "api is down: status 503. (acme)"},
		{"down without a reason", Event{Project: "acme", Monitor: "api", Down: true}, "api is down. (acme)"},
		{"up after seconds", Event{Project: "acme", Monitor: "api", DownFor: 59 * time.Second}, "api is up again after under a minute. (acme)"},
		{"up after minutes", Event{Project: "acme", Monitor: "api", DownFor: 4*time.Minute + 10*time.Second}, "api is up again after 4 min. (acme)"},
		{"up after hours", Event{Project: "acme", Monitor: "api", DownFor: 65 * time.Minute}, "api is up again after 1 h 5 min. (acme)"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.event.Text(); got != tt.want {
				t.Fatalf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestSendUsesTheFormatOfTheChannelKind(t *testing.T) {
	var received map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
		}
		received = nil
		if err := json.Unmarshal(raw, &received); err != nil {
			t.Error(err)
		}
	}))
	defer server.Close()
	event := Event{
		Project: "acme", Monitor: "api", Target: "https://example.com", Down: false,
		At: time.Date(2026, 11, 23, 4, 5, 6, 0, time.UTC), DownFor: 90 * time.Second,
	}
	const text = "api is up again after 2 min. (acme)"

	tests := []struct {
		kind string
		want map[string]any
	}{
		{"discord", map[string]any{"content": text}},
		{"slack", map[string]any{"text": text}},
		{"webhook", map[string]any{
			"event": "up", "project": "acme", "monitor": "api", "target": "https://example.com", "error": "",
			"at": "2026-11-23T04:05:06Z", "down_for_seconds": float64(90), "text": text,
		}},
	}
	for _, tt := range tests {
		t.Run(tt.kind, func(t *testing.T) {
			channel := store.AlertChannel{Kind: tt.kind, URL: server.URL}
			if err := Send(context.Background(), channel, event, time.Millisecond); err != nil {
				t.Fatal(err)
			}
			if len(received) != len(tt.want) {
				t.Fatalf("got body %v, want %v", received, tt.want)
			}
			for key, want := range tt.want {
				if received[key] != want {
					t.Fatalf("field %q: got %v, want %v", key, received[key], want)
				}
			}
		})
	}
}

func TestSendRetriesPassingFailuresOnly(t *testing.T) {
	tests := []struct {
		name         string
		statuses     []int // answers in order, the last one repeats
		wantAttempts int32
		wantOK       bool
	}{
		{"a 503 then a 200 is delivered on the second attempt", []int{503, 200}, 2, true},
		{"a 404 is not retried", []int{404}, 1, false},
		{"a channel that keeps failing is given up on after three attempts", []int{500}, 3, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var attempts atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				n := int(attempts.Add(1))
				w.WriteHeader(tt.statuses[min(n, len(tt.statuses))-1])
			}))
			defer server.Close()
			channel := store.AlertChannel{Kind: "slack", URL: server.URL}
			err := Send(context.Background(), channel, Event{Monitor: "api", Down: true}, time.Millisecond)
			if (err == nil) != tt.wantOK || attempts.Load() != tt.wantAttempts {
				t.Fatalf("got err=%v after %d attempt(s), want ok=%v after %d", err, attempts.Load(), tt.wantOK, tt.wantAttempts)
			}
		})
	}
}
