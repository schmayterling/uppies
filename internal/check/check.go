// Package check runs one availability check against a target.
package check

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"sync/atomic"
	"time"

	"golang.org/x/net/icmp"
	"golang.org/x/net/ipv4"
)

// Monitor kinds.
const (
	KindHTTP = "http"
	KindTCP  = "tcp"
	KindDNS  = "dns"
	KindPing = "ping"
	// KindPush monitors are not checked from here: the monitored job reports in.
	KindPush = "push"
)

// Kinds lists the monitor kinds in the order forms show them.
var Kinds = []string{KindHTTP, KindTCP, KindDNS, KindPing, KindPush}

// maxKeywordBody caps how much of a response is read when looking for a keyword.
const maxKeywordBody = 1 << 20

// Result is the outcome of one check.
type Result struct {
	OK      bool
	Latency time.Duration
	// Error says why the check failed, in words fit for the timeline. Empty when OK.
	Error string
	// CertExpiresAt is when the server's certificate expires. Zero unless an HTTPS check connected.
	CertExpiresAt time.Time
}

// The per-check deadline comes from the context, so the client itself has none.
var httpClient = &http.Client{}

// ValidateTarget reports whether target is well formed for the monitor kind.
func ValidateTarget(kind, target string) error {
	switch kind {
	case KindHTTP:
		u, err := url.Parse(target)
		if err != nil {
			return fmt.Errorf("not a valid url: %w", err)
		}
		if (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return errors.New("the url must start with http:// or https:// and name a host")
		}
		return nil
	case KindTCP:
		host, port, err := net.SplitHostPort(target)
		if err != nil || host == "" || port == "" {
			return errors.New("the target must be host:port, for example db.example.com:5432")
		}
		return nil
	case KindDNS, KindPing:
		if !hostnamePattern.MatchString(target) {
			return errors.New("the target must be a hostname or address without a scheme or port, for example example.com")
		}
		return nil
	case KindPush:
		return nil
	default:
		return fmt.Errorf("unknown monitor kind %q", kind)
	}
}

var hostnamePattern = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9.:-]*[A-Za-z0-9])?$`)

// Run performs one check and gives up after timeout.
func Run(ctx context.Context, kind, target, keyword string, timeout time.Duration) Result {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	start := time.Now()
	var err error
	var certExpiresAt time.Time
	switch kind {
	case KindHTTP:
		certExpiresAt, err = checkHTTP(ctx, target, keyword)
	case KindTCP:
		err = checkTCP(ctx, target)
	case KindDNS:
		err = checkDNS(ctx, target)
	case KindPing:
		err = checkPing(ctx, target)
	default:
		err = fmt.Errorf("unknown monitor kind %q", kind)
	}
	result := Result{OK: err == nil, Latency: time.Since(start), CertExpiresAt: certExpiresAt}
	if err == nil {
		return result
	}
	result.Error = err.Error()
	if errors.Is(err, context.DeadlineExceeded) {
		result.Error = fmt.Sprintf("no answer within %s", timeout)
	}
	return result
}

// checkHTTP also reports when the server's certificate expires, for HTTPS targets.
func checkHTTP(ctx context.Context, target, keyword string) (certExpiresAt time.Time, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return certExpiresAt, err
	}
	req.Header.Set("User-Agent", "uppies")
	resp, err := httpClient.Do(req)
	if err != nil {
		if urlErr, ok := errors.AsType[*url.Error](err); ok {
			// Drop the "Get <url>:" prefix. The timeline already names the monitor.
			return certExpiresAt, urlErr.Err
		}
		return certExpiresAt, err
	}
	defer func() {
		err = errors.Join(err, resp.Body.Close())
	}()
	if resp.TLS != nil && len(resp.TLS.PeerCertificates) > 0 {
		certExpiresAt = resp.TLS.PeerCertificates[0].NotAfter
	}

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return certExpiresAt, fmt.Errorf("status %d", resp.StatusCode)
	}
	if keyword == "" {
		return certExpiresAt, nil
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxKeywordBody))
	if err != nil {
		return certExpiresAt, fmt.Errorf("read body: %w", err)
	}
	if !bytes.Contains(body, []byte(keyword)) {
		return certExpiresAt, fmt.Errorf("keyword %q not found in the response", keyword)
	}
	return certExpiresAt, nil
}

// checkDNS passes when the name resolves to at least one address.
func checkDNS(ctx context.Context, target string) error {
	addresses, err := net.DefaultResolver.LookupHost(ctx, target)
	if err != nil {
		if dnsErr, ok := errors.AsType[*net.DNSError](err); ok {
			if dnsErr.IsNotFound {
				return errors.New("the name does not resolve")
			}
			return errors.New(dnsErr.Err)
		}
		return err
	}
	if len(addresses) == 0 {
		return errors.New("the name resolves to no address")
	}
	return nil
}

// pingSequence gives each echo request its own number, so a late reply to an
// earlier ping is not taken for the answer to this one.
var pingSequence atomic.Uint32

// checkPing sends one ICMP echo request over IPv4 and waits for its reply.
// It uses an unprivileged datagram socket, which Linux and macOS allow
// without root. Linux limits it to the groups in net.ipv4.ping_group_range.
func checkPing(ctx context.Context, target string) (err error) {
	addresses, err := net.DefaultResolver.LookupIP(ctx, "ip4", target)
	if err != nil || len(addresses) == 0 {
		return errors.New("the name has no IPv4 address")
	}
	conn, err := icmp.ListenPacket("udp4", "0.0.0.0")
	if err != nil {
		return fmt.Errorf("open a ping socket: %w", err)
	}
	defer func() {
		err = errors.Join(err, conn.Close())
	}()
	if deadline, ok := ctx.Deadline(); ok {
		if err := conn.SetDeadline(deadline); err != nil {
			return err
		}
	}

	sequence := int(pingSequence.Add(1) & 0xffff)
	request := icmp.Message{Type: ipv4.ICMPTypeEcho, Body: &icmp.Echo{Seq: sequence, Data: []byte("uppies")}}
	packet, err := request.Marshal(nil)
	if err != nil {
		return err
	}
	if _, err := conn.WriteTo(packet, &net.UDPAddr{IP: addresses[0]}); err != nil {
		return err
	}
	buffer := make([]byte, 1500)
	for {
		n, _, err := conn.ReadFrom(buffer)
		if err != nil {
			if netErr, ok := errors.AsType[net.Error](err); ok && netErr.Timeout() {
				return context.DeadlineExceeded
			}
			return err
		}
		const icmpv4 = 1 // protocol number, needed to parse the reply
		reply, err := icmp.ParseMessage(icmpv4, buffer[:n])
		if err != nil {
			return err
		}
		echo, isEcho := reply.Body.(*icmp.Echo)
		if reply.Type == ipv4.ICMPTypeEchoReply && isEcho && echo.Seq == sequence {
			return nil
		}
	}
}

func checkTCP(ctx context.Context, target string) error {
	var dialer net.Dialer
	conn, err := dialer.DialContext(ctx, "tcp", target)
	if err != nil {
		if opErr, ok := errors.AsType[*net.OpError](err); ok {
			// Drop the "dial tcp <addr>:" prefix for the same reason as above.
			return opErr.Err
		}
		return err
	}
	return conn.Close()
}
