// Package engine runs every monitor's checks on schedule and records the results.
package engine

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"strings"
	"sync"
	"time"

	"uppies/internal/alert"
	"uppies/internal/check"
	"uppies/internal/health"
	"uppies/internal/store"
)

const (
	// maxConcurrentChecks bounds open connections when many monitors are due at once.
	maxConcurrentChecks = 32
	// rawCheckRetention is how long single check results are kept. Rollups stay.
	rawCheckRetention = 48 * time.Hour
	pruneEvery        = time.Hour
	// maxStartDelay spreads first checks out so monitors do not all fire together.
	maxStartDelay = 10 * time.Second
	// alertDeadline bounds one change's alert deliveries, retries included.
	alertDeadline  = time.Minute
	alertRetryWait = 2 * time.Second
)

// Engine owns one goroutine per active monitor.
type Engine struct {
	store *store.Store
	log   *slog.Logger
	// mailer sends email alerts. It is nil when no SMTP server is configured.
	mailer *alert.Mailer
	slots  chan struct{}
	reload chan struct{}
	// alerts tracks deliveries in flight so that shutdown can wait for them.
	alerts sync.WaitGroup
}

// New returns an engine that reads monitors from st. mailer may be nil.
func New(st *store.Store, log *slog.Logger, mailer *alert.Mailer) *Engine {
	return &Engine{
		store:  st,
		log:    log,
		mailer: mailer,
		slots:  make(chan struct{}, maxConcurrentChecks),
		reload: make(chan struct{}, 1),
	}
}

// Reload makes the engine pick up added, changed and removed monitors.
func (e *Engine) Reload() {
	select {
	case e.reload <- struct{}{}:
	default: // a reload is already pending and will see this change too
	}
}

// Run checks monitors until ctx is cancelled.
func (e *Engine) Run(ctx context.Context) error {
	prune := time.NewTicker(pruneEvery)
	defer prune.Stop()

	for {
		monitors, err := e.store.Monitors(ctx)
		if err != nil {
			return err
		}
		generation, stop := context.WithCancel(ctx)
		var loops sync.WaitGroup
		for _, m := range monitors {
			if m.Paused {
				continue
			}
			loops.Go(func() { e.loop(generation, m) })
		}

		reloaded := false
		for !reloaded {
			select {
			case <-ctx.Done():
				stop()
				loops.Wait()
				e.alerts.Wait()
				return nil
			case <-e.reload:
				reloaded = true
			case now := <-prune.C:
				e.prune(ctx, now)
			}
		}
		stop()
		loops.Wait()
	}
}

func (e *Engine) prune(ctx context.Context, now time.Time) {
	deleted, err := e.store.PruneChecks(ctx, now.Add(-rawCheckRetention).UnixMilli())
	if err != nil {
		e.log.Error("prune old checks", "error", err)
		return
	}
	e.log.Info("pruned old checks", "deleted", deleted)
}

func (e *Engine) loop(ctx context.Context, m store.Monitor) {
	wait := firstDelay(m, time.Now())
	for {
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}

		next, nextWait, err := e.checkOnce(ctx, m)
		switch {
		case ctx.Err() != nil:
			return
		case errors.Is(err, store.ErrNotFound):
			return // deleted while the check ran
		case err != nil:
			e.log.Error("record check", "monitor_id", m.ID, "error", err)
			wait = time.Duration(m.IntervalS) * time.Second
		default:
			m, wait = next, nextWait
		}
	}
}

// firstDelay is the wait before a loop's first check: the rest of the current
// interval when the monitor was checked recently, otherwise a short random delay.
func firstDelay(m store.Monitor, now time.Time) time.Duration {
	interval := time.Duration(m.IntervalS) * time.Second
	due := time.UnixMilli(m.LastCheckAtMs).Add(interval)
	if m.LastCheckAtMs > 0 && due.After(now) {
		return due.Sub(now)
	}
	return rand.N(min(interval, maxStartDelay))
}

// checkOnce runs one check for m, records it and returns the monitor with its
// new state and the wait before the next check.
func (e *Engine) checkOnce(ctx context.Context, m store.Monitor) (store.Monitor, time.Duration, error) {
	interval := time.Duration(m.IntervalS) * time.Second
	resting, err := e.store.InMaintenance(ctx, m.ID, time.Now().UnixMilli())
	if err != nil {
		return m, 0, err
	}
	if resting {
		// Nothing is checked during planned maintenance, so nothing alerts or counts as downtime.
		return m, interval, nil
	}
	var result check.Result
	if m.Kind == check.KindPush {
		lastPushAtMs, err := e.store.LastPushAtMs(ctx, m.ID)
		if err != nil {
			return m, 0, err
		}
		if lastPushAtMs == 0 {
			// The job has never reported in, so there is nothing to judge yet.
			return m, interval, nil
		}
		result = pushResult(lastPushAtMs, time.Now(), interval)
	} else {
		select {
		case e.slots <- struct{}{}:
		case <-ctx.Done():
			return m, 0, ctx.Err()
		}
		result = check.Run(ctx, m.Kind, m.Target, m.Keyword, time.Duration(m.TimeoutMs)*time.Millisecond)
		<-e.slots
	}
	if err := ctx.Err(); err != nil {
		// The check was cut short by a reload or shutdown. Its failure is not the target's.
		return m, 0, err
	}

	atMs := time.Now().UnixMilli()
	var elapsedMs int64
	if m.LastCheckAtMs > 0 {
		// A gap longer than two intervals means uppies was not running. That time is not counted.
		elapsedMs = min(atMs-m.LastCheckAtMs, 2*int64(m.IntervalS)*1000)
	}
	policy := health.Policy{Retries: m.Retries, RecoveryChecks: m.RecoveryChecks}
	step := health.Advance(m.Tracker, result.OK, atMs, elapsedMs, policy)

	var certExpiresAtMs int64
	if !result.CertExpiresAt.IsZero() {
		certExpiresAtMs = result.CertExpiresAt.UnixMilli()
	}
	err = e.store.RecordCheck(ctx, store.CheckResult{
		MonitorID: m.ID, AtMs: atMs, OK: result.OK, LatencyMs: result.Latency.Milliseconds(),
		Error: result.Error, CoveredMs: elapsedMs, Step: step, CertExpiresAtMs: certExpiresAtMs,
	})
	if err != nil {
		return m, 0, err
	}
	if certExpiresAtMs > 0 {
		m, err = e.warnAboutCertificate(ctx, m, result.CertExpiresAt, time.UnixMilli(atMs))
		if err != nil {
			return m, 0, err
		}
	}
	if step.Change != health.NoChange {
		e.log.Info("monitor changed", "monitor_id", m.ID, "name", m.Name, "status", step.Tracker.Status, "error", result.Error)
		e.sendAlerts(ctx, m, alert.Event{
			Monitor: m.Name,
			Target:  m.Target,
			Down:    step.Change == health.WentDown,
			Error:   result.Error,
			At:      time.UnixMilli(atMs),
			DownFor: time.Duration(atMs-m.StatusSinceMs) * time.Millisecond,
		})
		m.StatusSinceMs = atMs
	}

	m.Tracker = step.Tracker
	m.LastCheckAtMs = atMs
	wait := interval
	if m.Tracker.Retrying() {
		wait = time.Duration(m.RetryIntervalS) * time.Second
	}
	return m, wait, nil
}

// pushGrace is how late a job may report in before the check fails. It
// absorbs clock differences and the time a request takes to arrive.
const pushGrace = 10 * time.Second

// pushResult judges a push monitor: the job must have reported in within one interval.
func pushResult(lastPushAtMs int64, now time.Time, interval time.Duration) check.Result {
	silence := now.Sub(time.UnixMilli(lastPushAtMs))
	if silence <= interval+pushGrace {
		return check.Result{OK: true}
	}
	return check.Result{Error: fmt.Sprintf("no report for %s", silence.Round(time.Second))}
}

// certWarningDays are the days before expiry at which a warning goes out, soonest last.
var certWarningDays = []int{14, 7, 3, 1}

// certWarningDue returns the threshold to warn at now, or 0 for none. Each
// threshold warns once: warnedDays is the last one already alerted on.
func certWarningDue(daysLeft, warnedDays int) int {
	due := 0
	for _, threshold := range certWarningDays {
		if daysLeft <= threshold {
			due = threshold
		}
	}
	if due == 0 || (warnedDays != 0 && due >= warnedDays) {
		return 0
	}
	return due
}

// warnAboutCertificate alerts once per threshold as a certificate nears its
// expiry, and forgets past warnings once the certificate has been renewed.
func (e *Engine) warnAboutCertificate(ctx context.Context, m store.Monitor, expiresAt, now time.Time) (store.Monitor, error) {
	daysLeft := int(expiresAt.Sub(now).Hours() / 24)
	renewed := daysLeft > certWarningDays[0] && m.CertWarnedDays != 0
	due := certWarningDue(daysLeft, m.CertWarnedDays)
	switch {
	case renewed:
		m.CertWarnedDays = 0
	case due != 0:
		m.CertWarnedDays = due
		e.sendAlerts(ctx, m, alert.Event{Monitor: m.Name, Target: m.Target, At: now, CertDaysLeft: max(daysLeft, 1)})
	default:
		return m, nil
	}
	return m, e.store.SetCertWarnedDays(ctx, m.ID, m.CertWarnedDays)
}

// sendAlerts delivers a change to the monitor's channels in the background.
// Deliveries outlive a reload, and Run waits for them before it returns.
func (e *Engine) sendAlerts(ctx context.Context, m store.Monitor, event alert.Event) {
	channels, err := e.store.MonitorChannels(ctx, m.ID)
	if err != nil {
		e.log.Error("load alert channels", "monitor_id", m.ID, "error", err)
		return
	}
	if len(channels) == 0 {
		return
	}
	// The alert names the status pages that show the monitor. An alert still
	// goes out when they cannot be read, without them.
	pages, err := e.store.PageNames(ctx, m.ID)
	if err != nil {
		e.log.Error("load status pages for alert", "monitor_id", m.ID, "error", err)
	}
	event.Project = strings.Join(pages, ", ")
	if len(pages) == 0 {
		event.Project = "on no status page"
	}
	if event.Down {
		event.DownFor = 0
	}
	for _, channel := range channels {
		e.alerts.Go(func() {
			sendCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), alertDeadline)
			defer cancel()
			if err := alert.Deliver(sendCtx, e.mailer, channel, event, alertRetryWait); err != nil {
				e.log.Error("send alert", "channel_id", channel.ID, "kind", channel.Kind, "monitor_id", m.ID, "error", err)
			}
		})
	}
}

// Heartbeat requests url every interval until ctx is cancelled. Pointed at an
// outside service that expects regular requests, it gets someone told when
// uppies itself stops running.
func Heartbeat(ctx context.Context, url string, interval time.Duration, log *slog.Logger) {
	client := &http.Client{Timeout: 10 * time.Second}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		if err := beat(ctx, client, url); err != nil && ctx.Err() == nil {
			log.Error("send heartbeat", "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func beat(ctx context.Context, client *http.Client, url string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "uppies")
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	if err := resp.Body.Close(); err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Errorf("the heartbeat url answered with status %d", resp.StatusCode)
	}
	return nil
}
