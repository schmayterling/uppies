package health

import "testing"

func TestAdvance(t *testing.T) {
	policy := Policy{Retries: 2, RecoveryChecks: 2}
	type result struct {
		ok         bool
		atMs       int64
		wantStatus Status
		wantChange Change
		wantDownMs int64
	}
	tests := []struct {
		name    string
		start   Status
		results []result
	}{
		{
			name:  "third failure in a row confirms the outage and two good checks end it",
			start: Up,
			results: []result{
				{false, 1_000, Up, NoChange, 0},
				{false, 21_000, Up, NoChange, 0},
				{false, 41_000, Down, WentDown, 40_000},
				{false, 101_000, Down, NoChange, 60_000},
				{true, 161_000, Down, NoChange, 60_000},
				{true, 221_000, Up, WentUp, 60_000},
				{true, 281_000, Up, NoChange, 0},
			},
		},
		{
			name:  "alternating failures never confirm an outage",
			start: Up,
			results: []result{
				{false, 1_000, Up, NoChange, 0},
				{true, 31_000, Up, NoChange, 0},
				{false, 61_000, Up, NoChange, 0},
				{true, 91_000, Up, NoChange, 0},
			},
		},
		{
			name:  "a failure during recovery restarts the recovery count",
			start: Down,
			results: []result{
				{true, 1_000, Down, NoChange, 0},
				{false, 61_000, Down, NoChange, 60_000},
				{true, 121_000, Down, NoChange, 60_000},
				{true, 181_000, Up, WentUp, 60_000},
			},
		},
		{
			name:  "a new monitor becomes up on its first good check without a change event",
			start: Unknown,
			results: []result{
				{true, 1_000, Up, NoChange, 0},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tracker := Tracker{Status: tt.start}
			var lastAtMs int64
			for i, r := range tt.results {
				var elapsedMs int64
				if i > 0 {
					elapsedMs = r.atMs - lastAtMs
				}
				step := Advance(tracker, r.ok, r.atMs, elapsedMs, policy)
				if step.Tracker.Status != r.wantStatus || step.Change != r.wantChange || step.DownMs != r.wantDownMs {
					t.Fatalf("result %d: got status=%s change=%q downMs=%d, want status=%s change=%q downMs=%d",
						i, step.Tracker.Status, step.Change, step.DownMs, r.wantStatus, r.wantChange, r.wantDownMs)
				}
				tracker = step.Tracker
				lastAtMs = r.atMs
			}
		})
	}
}

func TestRetrying(t *testing.T) {
	tests := []struct {
		name    string
		tracker Tracker
		want    bool
	}{
		{"failing but not confirmed", Tracker{Status: Up, FailStreak: 1}, true},
		{"confirmed down", Tracker{Status: Down, FailStreak: 3}, false},
		{"healthy", Tracker{Status: Up}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.tracker.Retrying(); got != tt.want {
				t.Fatalf("got %v, want %v", got, tt.want)
			}
		})
	}
}
