package store

import (
	"path/filepath"
	"slices"
	"testing"
)

func TestPublishedIncidentSummariesKeepTheirOrderAcrossPages(t *testing.T) {
	for _, backend := range []Backend{SQLite, TursoSQLite} {
		t.Run(string(backend), func(t *testing.T) {
			ctx := t.Context()
			s, err := OpenBackend(ctx, backend, filepath.Join(t.TempDir(), "test.db"), "")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := s.Close(); err != nil {
					t.Error(err)
				}
			})
			const startedAt = int64(1_800_000_000_000)
			west, err := s.CreateProject(ctx, "west", "West", startedAt)
			if err != nil {
				t.Fatal(err)
			}
			east, err := s.CreateProject(ctx, "east", "East", startedAt)
			if err != nil {
				t.Fatal(err)
			}
			create := func(projectID int64, title, status string, started int64, published bool) int64 {
				t.Helper()
				id, err := s.CreateIncident(ctx, Incident{
					ProjectID: projectID, Title: title, Impact: "minor", Status: status,
					StartedAtMs: started, Published: published,
				}, "details", started+60_000)
				if err != nil {
					t.Fatal(err)
				}
				return id
			}
			newest := create(west, "newest", "identified", startedAt+1000, true)
			oldest := create(east, "oldest", "resolved", startedAt-1000, true)
			lowerTie := create(west, "same time, lower ID", "resolved", startedAt, true)
			higherTie := create(east, "same time, higher ID", "monitoring", startedAt, true)
			create(east, "active draft", "investigating", startedAt+2000, false)
			create(west, "resolved draft", "resolved", startedAt-500, false)

			checkPage := func(list func(IncidentCursor) ([]PublishedIncidentSummary, error), before IncidentCursor, want []int64) {
				t.Helper()
				rows, err := list(before)
				if err != nil {
					t.Fatal(err)
				}
				got := make([]int64, 0, len(rows))
				for _, row := range rows {
					got = append(got, row.ID)
				}
				if !slices.Equal(got, want) {
					t.Fatalf("before %+v: incident IDs = %v, want %v", before, got, want)
				}
			}
			allTime := func(before IncidentCursor) ([]PublishedIncidentSummary, error) {
				return s.PublishedIncidentSummaries(ctx, before, 2)
			}
			checkPage(allTime, IncidentCursor{}, []int64{newest, higherTie})
			create(west, "published between requests", "investigating", startedAt+3000, true)
			afterHigherTie := IncidentCursor{StartedAtMs: startedAt, ID: higherTie}
			afterOldest := IncidentCursor{StartedAtMs: startedAt - 1000, ID: oldest}
			checkPage(allTime, afterHigherTie, []int64{lowerTie, oldest})
			checkPage(allTime, afterOldest, nil)

			create(east, "just before the interval", "resolved", startedAt-1001, true)
			withinInterval := func(before IncidentCursor) ([]PublishedIncidentSummary, error) {
				return s.PublishedIncidentSummariesBetween(ctx, before, startedAt-1000, startedAt+1000, 2)
			}
			checkPage(withinInterval, IncidentCursor{}, []int64{higherTie, lowerTie})
			checkPage(withinInterval, afterHigherTie, []int64{lowerTie, oldest})
			checkPage(withinInterval, afterOldest, nil)
		})
	}
}
