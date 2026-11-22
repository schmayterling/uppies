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
	"time"
)

// Monitor kinds.
const (
	KindHTTP = "http"
	KindTCP  = "tcp"
)

// maxKeywordBody caps how much of a response is read when looking for a keyword.
const maxKeywordBody = 1 << 20

// Result is the outcome of one check.
type Result struct {
	OK      bool
	Latency time.Duration
	// Error says why the check failed, in words fit for the timeline. Empty when OK.
	Error string
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
	default:
		return fmt.Errorf("unknown monitor kind %q", kind)
	}
}

// Run performs one check and gives up after timeout.
func Run(ctx context.Context, kind, target, keyword string, timeout time.Duration) Result {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	start := time.Now()
	var err error
	switch kind {
	case KindHTTP:
		err = checkHTTP(ctx, target, keyword)
	case KindTCP:
		err = checkTCP(ctx, target)
	default:
		err = fmt.Errorf("unknown monitor kind %q", kind)
	}
	result := Result{OK: err == nil, Latency: time.Since(start)}
	if err == nil {
		return result
	}
	result.Error = err.Error()
	if errors.Is(err, context.DeadlineExceeded) {
		result.Error = fmt.Sprintf("no answer within %s", timeout)
	}
	return result
}

func checkHTTP(ctx context.Context, target, keyword string) (err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "uppies")
	resp, err := httpClient.Do(req)
	if err != nil {
		if urlErr, ok := errors.AsType[*url.Error](err); ok {
			// Drop the "Get <url>:" prefix. The timeline already names the monitor.
			return urlErr.Err
		}
		return err
	}
	defer func() {
		err = errors.Join(err, resp.Body.Close())
	}()

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Errorf("status %d", resp.StatusCode)
	}
	if keyword == "" {
		return nil
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxKeywordBody))
	if err != nil {
		return fmt.Errorf("read body: %w", err)
	}
	if !bytes.Contains(body, []byte(keyword)) {
		return fmt.Errorf("keyword %q not found in the response", keyword)
	}
	return nil
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
