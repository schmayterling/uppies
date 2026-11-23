// Package alert tells the outside world when a monitor goes down or recovers.
package alert

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"time"

	"uppies/internal/store"
)

const (
	sendTimeout = 10 * time.Second
	// maxAttempts bounds retries of a delivery that failed for a passing reason.
	maxAttempts = 3
)

// Event is one confirmed change of a monitor.
type Event struct {
	Project string
	Monitor string
	Target  string
	Down    bool   // true when the monitor went down, false when it recovered
	Error   string // why the check failed, empty on recovery
	// CertDaysLeft is set for a certificate warning, which is neither down nor up.
	CertDaysLeft int
	At           time.Time
	// DownFor is how long the outage lasted. Set on recovery only.
	DownFor time.Duration
}

// Text is the one-line summary used by chat channels.
func (e Event) Text() string {
	if e.CertDaysLeft > 0 {
		return fmt.Sprintf("the certificate of %s expires in %d days. (%s)", e.Monitor, e.CertDaysLeft, e.Project)
	}
	if !e.Down {
		return fmt.Sprintf("%s is up again after %s. (%s)", e.Monitor, durationWords(e.DownFor), e.Project)
	}
	if e.Error == "" {
		return fmt.Sprintf("%s is down. (%s)", e.Monitor, e.Project)
	}
	return fmt.Sprintf("%s is down: %s. (%s)", e.Monitor, e.Error, e.Project)
}

func durationWords(d time.Duration) string {
	minutes := int(d.Round(time.Minute) / time.Minute)
	switch {
	case d < time.Minute:
		return "under a minute"
	case minutes < 60:
		return fmt.Sprintf("%d min", minutes)
	default:
		return fmt.Sprintf("%d h %d min", minutes/60, minutes%60)
	}
}

// webhookPayload is the body sent to a plain webhook.
type webhookPayload struct {
	Event          string `json:"event"` // "down", "up" or "certificate"
	Project        string `json:"project"`
	Monitor        string `json:"monitor"`
	Target         string `json:"target"`
	Error          string `json:"error"`
	At             string `json:"at"` // RFC 3339, UTC
	DownForSeconds int64  `json:"down_for_seconds"`
	Text           string `json:"text"`
}

// body builds the request body in the format the channel kind expects.
func body(kind string, e Event) ([]byte, error) {
	switch kind {
	case "discord":
		return json.Marshal(map[string]string{"content": e.Text()})
	case "slack":
		return json.Marshal(map[string]string{"text": e.Text()})
	case "webhook":
		name := "up"
		if e.Down {
			name = "down"
		}
		if e.CertDaysLeft > 0 {
			name = "certificate"
		}
		return json.Marshal(webhookPayload{
			Event: name, Project: e.Project, Monitor: e.Monitor, Target: e.Target, Error: e.Error,
			At: e.At.UTC().Format(time.RFC3339), DownForSeconds: int64(e.DownFor / time.Second), Text: e.Text(),
		})
	default:
		return nil, fmt.Errorf("unknown alert channel kind %q", kind)
	}
}

// ValidateURL reports whether target can be used as a channel URL.
func ValidateURL(target string) error {
	u, err := url.Parse(target)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return errors.New("the url must start with http:// or https:// and name a host")
	}
	return nil
}

var client = &http.Client{Timeout: sendTimeout}

// Send delivers one event to one channel. It retries a delivery that failed
// with a network error or a 429 or 5xx answer, waiting retryWait in between.
// A repeated alert is harmless, so the retry needs no deduplication.
func Send(ctx context.Context, channel store.AlertChannel, e Event, retryWait time.Duration) error {
	payload, err := body(channel.Kind, e)
	if err != nil {
		return err
	}
	for attempt := 1; ; attempt++ {
		retry, err := post(ctx, channel.URL, payload)
		if err == nil || !retry || attempt == maxAttempts {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(retryWait * time.Duration(attempt)):
		}
	}
}

// post makes one delivery attempt and reports whether a failure is worth retrying.
func post(ctx context.Context, target string, payload []byte) (retry bool, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(payload))
	if err != nil {
		return false, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "uppies")
	resp, err := client.Do(req)
	if err != nil {
		if urlErr, ok := errors.AsType[*url.Error](err); ok {
			// The wrapped error repeats the full URL, which can hold a secret token.
			err = urlErr.Err
		}
		return true, err
	}
	if err := resp.Body.Close(); err != nil {
		return false, err
	}
	if resp.StatusCode >= 200 && resp.StatusCode <= 299 {
		return false, nil
	}
	passing := resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500
	return passing, fmt.Errorf("the channel answered with status %d", resp.StatusCode)
}
