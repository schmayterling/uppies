// Package engine runs every monitor's checks on schedule and records the results.
package engine

import (
	"context"
	"errors"
	"log/slog"
	"math/rand/v2"
	"sync"
	"time"

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
)

// Engine owns one goroutine per active monitor.
type Engine struct {
	store  *store.Store
	log    *slog.Logger
	slots  chan struct{}
	reload chan struct{}
}

// New returns an engine that reads monitors from st.
func New(st *store.Store, log *slog.Logger) *Engine {
	return &Engine{
		store:  st,
		log:    log,
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
	select {
	case e.slots <- struct{}{}:
	case <-ctx.Done():
		return m, 0, ctx.Err()
	}
	result := check.Run(ctx, m.Kind, m.Target, m.Keyword, time.Duration(m.TimeoutMs)*time.Millisecond)
	<-e.slots
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

	err := e.store.RecordCheck(ctx, store.CheckResult{
		MonitorID: m.ID, AtMs: atMs, OK: result.OK, LatencyMs: result.Latency.Milliseconds(),
		Error: result.Error, CoveredMs: elapsedMs, Step: step,
	})
	if err != nil {
		return m, 0, err
	}
	if step.Change != health.NoChange {
		e.log.Info("monitor changed", "monitor_id", m.ID, "name", m.Name, "status", step.Tracker.Status, "error", result.Error)
	}

	m.Tracker = step.Tracker
	m.LastCheckAtMs = atMs
	wait := time.Duration(m.IntervalS) * time.Second
	if m.Tracker.Retrying() {
		wait = time.Duration(m.RetryIntervalS) * time.Second
	}
	return m, wait, nil
}
