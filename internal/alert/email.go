package alert

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/mail"
	"net/smtp"
	"strings"
	"time"

	"uppies/internal/store"
)

// Mailer sends alert emails through one SMTP server.
type Mailer struct {
	Addr     string // host:port of the SMTP server
	From     string // the sender address
	Username string // empty for a server that needs no sign-in
	Password string
}

// ErrNoMailer is returned for an email channel when uppies has no SMTP server configured.
var ErrNoMailer = errors.New("email is not set up: start uppies with -smtp-addr and -smtp-from")

// ValidateEmail reports whether address can be used as an email channel.
func ValidateEmail(address string) error {
	parsed, err := mail.ParseAddress(address)
	if err != nil || parsed.Address != address {
		return errors.New("enter a plain email address such as ops@example.com")
	}
	return nil
}

// Deliver sends one event to one channel of any kind. mailer may be nil when
// email is not configured.
func Deliver(ctx context.Context, mailer *Mailer, channel store.AlertChannel, e Event, retryWait time.Duration) error {
	if channel.Kind != "email" {
		return Send(ctx, channel, e, retryWait)
	}
	if mailer == nil {
		return ErrNoMailer
	}
	return mailer.send(ctx, channel.URL, e)
}

// message builds the email. Line breaks are stripped from header values so
// that a monitor name cannot add headers of its own.
func (m *Mailer) message(to string, e Event) []byte {
	oneLine := strings.NewReplacer("\r", " ", "\n", " ")
	var b strings.Builder
	fmt.Fprintf(&b, "From: %s\r\n", oneLine.Replace(m.From))
	fmt.Fprintf(&b, "To: %s\r\n", oneLine.Replace(to))
	fmt.Fprintf(&b, "Subject: %s\r\n", oneLine.Replace(e.Text()))
	fmt.Fprintf(&b, "Date: %s\r\n", e.At.UTC().Format(time.RFC1123Z))
	b.WriteString("MIME-Version: 1.0\r\nContent-Type: text/plain; charset=utf-8\r\n\r\n")
	fmt.Fprintf(&b, "%s\r\n\r\n", e.Text())
	fmt.Fprintf(&b, "project: %s\r\nmonitor: %s\r\n", e.Project, e.Monitor)
	if e.Target != "" {
		fmt.Fprintf(&b, "target: %s\r\n", e.Target)
	}
	fmt.Fprintf(&b, "time: %s\r\n", e.At.UTC().Format("2006-01-02 15:04:05 UTC"))
	return []byte(b.String())
}

func (m *Mailer) send(ctx context.Context, to string, e Event) (err error) {
	host, _, err := net.SplitHostPort(m.Addr)
	if err != nil {
		return fmt.Errorf("smtp address %q: %w", m.Addr, err)
	}
	ctx, cancel := context.WithTimeout(ctx, 2*sendTimeout)
	defer cancel()
	var dialer net.Dialer
	conn, err := dialer.DialContext(ctx, "tcp", m.Addr)
	if err != nil {
		return fmt.Errorf("reach the smtp server: %w", err)
	}
	deadline, _ := ctx.Deadline() // always set by the timeout above
	if err := conn.SetDeadline(deadline); err != nil {
		return errors.Join(err, conn.Close())
	}
	client, err := smtp.NewClient(conn, host)
	if err != nil {
		return errors.Join(fmt.Errorf("greet the smtp server: %w", err), conn.Close())
	}
	defer func() {
		if err != nil {
			err = errors.Join(err, client.Close())
		}
	}()

	if ok, _ := client.Extension("STARTTLS"); ok {
		if err := client.StartTLS(&tls.Config{ServerName: host}); err != nil {
			return fmt.Errorf("start tls: %w", err)
		}
	}
	if m.Username != "" {
		// PlainAuth refuses to send the password over a connection that is not encrypted.
		if err := client.Auth(smtp.PlainAuth("", m.Username, m.Password, host)); err != nil {
			return fmt.Errorf("sign in to the smtp server: %w", err)
		}
	}
	if err := client.Mail(m.From); err != nil {
		return err
	}
	if err := client.Rcpt(to); err != nil {
		return err
	}
	body, err := client.Data()
	if err != nil {
		return err
	}
	if _, err := body.Write(m.message(to, e)); err != nil {
		return err
	}
	if err := body.Close(); err != nil {
		return err
	}
	return client.Quit()
}
