// Package health decides when a monitor is up or down from a stream of check results.
package health

// Status is the confirmed state of a monitor.
type Status string

const (
	Unknown Status = "unknown"
	Up      Status = "up"
	Down    Status = "down"
)

// Change is a confirmed status change produced by one check result.
type Change string

const (
	NoChange Change = ""
	WentDown Change = "down"
	WentUp   Change = "up"
)

// Policy says how many results it takes to confirm a change.
type Policy struct {
	// Retries is the number of failed checks tolerated before the monitor is down.
	Retries int
	// RecoveryChecks is the number of consecutive good checks that end an outage.
	RecoveryChecks int
}

// Tracker is the state kept per monitor between checks.
type Tracker struct {
	Status         Status
	FailStreak     int
	OKStreak       int
	FailingSinceMs int64 // time of the first failure of the current streak, 0 when not failing
}

// Retrying reports whether the monitor has failed but is not yet confirmed down.
func (t Tracker) Retrying() bool {
	return t.FailStreak > 0 && t.Status != Down
}

// Step is the result of applying one check result to a tracker.
type Step struct {
	Tracker Tracker
	Change  Change
	// DownMs is the downtime this check adds: the time since the previous check
	// when the monitor was already down, plus the whole failing streak at the
	// moment an outage is confirmed.
	DownMs int64
}

// Advance applies one check result taken at atMs. elapsedMs is the time since
// the previous check, 0 for the first one.
func Advance(t Tracker, ok bool, atMs, elapsedMs int64, p Policy) Step {
	step := Step{Tracker: t}
	if t.Status == Down {
		step.DownMs = elapsedMs
	}
	next := &step.Tracker

	if ok {
		next.FailStreak = 0
		next.FailingSinceMs = 0
		next.OKStreak++
		switch {
		case t.Status == Unknown:
			next.Status = Up
		case t.Status == Down && next.OKStreak >= p.RecoveryChecks:
			next.Status = Up
			step.Change = WentUp
		}
		return step
	}

	next.OKStreak = 0
	next.FailStreak++
	if next.FailingSinceMs == 0 {
		next.FailingSinceMs = atMs
	}
	if t.Status != Down && next.FailStreak > p.Retries {
		next.Status = Down
		step.Change = WentDown
		step.DownMs += atMs - next.FailingSinceMs
	}
	return step
}
