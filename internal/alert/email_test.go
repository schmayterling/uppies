package alert

import (
	"bufio"
	"context"
	"errors"
	"net"
	"strings"
	"testing"
	"time"

	"uppies/internal/store"
)

// fakeSMTP accepts one message and returns it on the channel. It speaks just
// enough of the protocol for a client without TLS or sign-in.
func fakeSMTP(t *testing.T) (addr string, received <-chan string) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = listener.Close() // the test is over, a failed close changes nothing
	})
	out := make(chan string, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer func() {
			_ = conn.Close() // same as above
		}()
		reader := bufio.NewReader(conn)
		reply := func(line string) {
			_, _ = conn.Write([]byte(line + "\r\n")) // a broken pipe ends the read loop below
		}
		reply("220 fake smtp")
		var transcript strings.Builder
		inData := false
		for {
			line, err := reader.ReadString('\n')
			if err != nil {
				return
			}
			transcript.WriteString(line)
			switch {
			case inData && line == ".\r\n":
				inData = false
				reply("250 stored")
			case inData:
			case strings.HasPrefix(line, "DATA"):
				inData = true
				reply("354 go ahead")
			case strings.HasPrefix(line, "QUIT"):
				reply("221 bye")
				out <- transcript.String()
				return
			default:
				reply("250 ok")
			}
		}
	}()
	return listener.Addr().String(), out
}

func TestDeliverSendsAnEmail(t *testing.T) {
	addr, received := fakeSMTP(t)
	mailer := &Mailer{Addr: addr, From: "uppies@example.com"}
	event := Event{
		Project: "acme", Monitor: "api\r\nBcc: attacker@example.com", Target: "https://example.com",
		Down: true, Error: "status 503", At: time.Date(2026, 11, 23, 4, 5, 6, 0, time.UTC),
	}
	channel := store.AlertChannel{Kind: "email", URL: "ops@example.com"}
	if err := Deliver(context.Background(), mailer, channel, event, time.Millisecond); err != nil {
		t.Fatal(err)
	}
	got := <-received
	for _, want := range []string{
		"MAIL FROM:<uppies@example.com>",
		"RCPT TO:<ops@example.com>",
		"Subject: api  Bcc: attacker@example.com is down: status 503. (acme)\r\n",
		"target: https://example.com\r\n",
		"time: 2026-11-23 04:05:06 UTC\r\n",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("the message is missing %q\n%s", want, got)
		}
	}
	// Headers end at the first empty line. A line break is harmless in the body after it.
	headers, _, _ := strings.Cut(got, "\r\n\r\n")
	if strings.Contains(headers, "\r\nBcc: attacker@example.com") {
		t.Error("a line break in the monitor name added a header")
	}
}

func TestDeliverWithoutAMailerExplainsWhatIsMissing(t *testing.T) {
	channel := store.AlertChannel{Kind: "email", URL: "ops@example.com"}
	err := Deliver(context.Background(), nil, channel, Event{Monitor: "api", Down: true}, time.Millisecond)
	if !errors.Is(err, ErrNoMailer) {
		t.Fatalf("got err=%v, want ErrNoMailer", err)
	}
}

func TestValidateEmail(t *testing.T) {
	tests := []struct {
		address string
		wantOK  bool
	}{
		{"ops@example.com", true},
		{"Ops Team <ops@example.com>", false},
		{"not an address", false},
		{"https://example.com/hook", false},
	}
	for _, tt := range tests {
		t.Run(tt.address, func(t *testing.T) {
			if err := ValidateEmail(tt.address); (err == nil) != tt.wantOK {
				t.Fatalf("got err=%v, want ok=%v", err, tt.wantOK)
			}
		})
	}
}
