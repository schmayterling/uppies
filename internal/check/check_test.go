package check

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestRunHTTP(t *testing.T) {
	release := make(chan struct{})
	mux := http.NewServeMux()
	mux.HandleFunc("/ok", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("status: healthy")) // a failed write shows up as a failed case below
	})
	mux.HandleFunc("/broken", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	})
	mux.HandleFunc("/moved", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/ok", http.StatusFound)
	})
	mux.HandleFunc("/slow", func(w http.ResponseWriter, r *http.Request) {
		<-release
	})
	server := httptest.NewServer(mux)
	defer server.Close()
	defer close(release)

	tests := []struct {
		name      string
		path      string
		keyword   string
		wantOK    bool
		wantError string
	}{
		{"a 200 answer is up", "/ok", "", true, ""},
		{"a 503 answer is down", "/broken", "", false, "status 503"},
		{"a redirect to a 200 page is up", "/moved", "", true, ""},
		{"a present keyword is up", "/ok", "healthy", true, ""},
		{"a missing keyword is down", "/ok", "degraded", false, `keyword "degraded" not found in the response`},
		{"no answer before the timeout is down", "/slow", "", false, "no answer within 200ms"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Run(context.Background(), KindHTTP, server.URL+tt.path, tt.keyword, 200*time.Millisecond)
			if got.OK != tt.wantOK || got.Error != tt.wantError {
				t.Fatalf("got ok=%v error=%q, want ok=%v error=%q", got.OK, got.Error, tt.wantOK, tt.wantError)
			}
		})
	}
}

func TestRunTCP(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	open := listener.Addr().String()
	if got := Run(context.Background(), KindTCP, open, "", time.Second); !got.OK {
		t.Fatalf("open port: got error %q, want ok", got.Error)
	}

	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	got := Run(context.Background(), KindTCP, open, "", time.Second)
	if got.OK || !strings.Contains(got.Error, "connection refused") {
		t.Fatalf("closed port: got ok=%v error=%q, want a connection refused failure", got.OK, got.Error)
	}
}

func TestValidateTarget(t *testing.T) {
	tests := []struct {
		name   string
		kind   string
		target string
		wantOK bool
	}{
		{"https url", KindHTTP, "https://example.com/health", true},
		{"url without a scheme", KindHTTP, "example.com/health", false},
		{"file url", KindHTTP, "file:///etc/passwd", false},
		{"host and port", KindTCP, "db.example.com:5432", true},
		{"host without a port", KindTCP, "db.example.com", false},
		{"unknown kind", "icmp", "example.com", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateTarget(tt.kind, tt.target)
			if (err == nil) != tt.wantOK {
				t.Fatalf("got err=%v, want ok=%v", err, tt.wantOK)
			}
		})
	}
}
