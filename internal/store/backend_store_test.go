package store

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"uppies/internal/health"
)

// Server backends use dedicated empty databases supplied by the test runner.
func TestDatabaseBackendWorkflow(t *testing.T) {
	for _, target := range []struct {
		backend Backend
		env     string
	}{
		{SQLite, ""},
		{TursoSQLite, ""},
		{Postgres, "UPPIES_TEST_POSTGRES"},
		{MariaDB, "UPPIES_TEST_MARIADB"},
		{TursoPG, "UPPIES_TEST_TURSOPG"},
	} {
		t.Run(string(target.backend), func(t *testing.T) {
			dsn := os.Getenv(target.env)
			if target.env != "" && dsn == "" {
				t.Skip("set " + target.env + " to an empty test database")
			}
			ctx := t.Context()
			path := filepath.Join(t.TempDir(), "test.db")
			s, err := OpenBackend(ctx, target.backend, path, dsn)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := s.Close(); err != nil {
					t.Error(err)
				}
			})
			check := func(err error) {
				t.Helper()
				if err != nil {
					t.Fatal(err)
				}
			}

			const now = int64(1_800_000_000_000)
			facts, err := s.InstanceFacts(ctx)
			check(err)
			if facts.Users != 0 || facts.StatusPages != 0 || facts.Monitors != 0 {
				t.Fatal("the test requires an empty database")
			}
			if s.Backend() != target.backend || facts.Schema != len(migrations) {
				t.Fatalf("unexpected backend or schema: %s, %d", s.Backend(), facts.Schema)
			}
			created, err := s.CreateFirstUser(ctx, "admin", "test hash", now)
			check(err)
			if !created {
				t.Fatal("first admin was not created")
			}
			created, err = s.CreateFirstUser(ctx, "other", "test hash", now)
			check(err)
			if created {
				t.Fatal("first-user setup ran twice")
			}
			user, _, err := s.UserPasswordHash(ctx, "admin")
			check(err)
			check(s.CreateSession(ctx, "test session", user, now, now+60_000))
			sessionUser, err := s.SessionUser(ctx, "test session", now)
			check(err)
			if sessionUser != user {
				t.Fatalf("session owner = %d, want %d", sessionUser, user)
			}
			const permission = "monitors:read"
			digest := strings.Repeat("a", 64)
			check(s.CreateAPIToken(ctx, user, "smoke", digest, now, now+60_000, []string{permission}))
			allowed, err := s.APITokenAllows(ctx, digest, permission, now)
			check(err)
			if !allowed {
				t.Fatal("created token does not have its permission")
			}

			project, err := s.CreateProject(ctx, "page", "Page", now)
			check(err)
			if _, err := s.CreateProject(ctx, "page", "Duplicate", now); !errors.Is(err, ErrSlugTaken) {
				t.Fatalf("duplicate slug: %v", err)
			}
			monitor, err := createPlaced(ctx, s, project, now)
			check(err)
			check(s.PlaceMonitor(ctx, project, monitor))
			check(s.SetPageLayout(ctx, project, []string{"Service"}, []Placement{{MonitorID: monitor, Category: "Service", Display: "latency", ExcludeFromStatus: true}}))
			if err := s.SetPageLayout(ctx, project, nil, []Placement{{MonitorID: 7_777}}); !errors.Is(err, ErrUnknownMonitor) {
				t.Fatalf("invalid placement: %v", err)
			}
			categories, placed, err := s.PageLayout(ctx, project)
			check(err)
			if len(categories) != 1 || len(placed) != 1 || placed[0].MonitorID != monitor || !placed[0].ExcludeFromStatus {
				t.Fatalf("layout did not survive the failed replacement: %v %v", categories, placed)
			}

			check(s.SaveInstance(ctx, defaultInstance))
			check(s.SaveInstance(ctx, defaultInstance))
			settings, err := s.Instance(ctx)
			check(err)
			if settings != defaultInstance {
				t.Fatalf("instance settings: %+v", settings)
			}
			image := []byte{0, 255, 39, 92, 128}
			check(s.SetProjectImage(ctx, project, ImageFavicon, "image/png", image, now))
			check(s.SetInstanceImage(ctx, ImageInstanceFavicon, "image/png", image, now))
			_, got, err := s.ProjectImage(ctx, project, ImageFavicon)
			check(err)
			if !bytes.Equal(got, image) {
				t.Fatalf("project image bytes: %x", got)
			}

			policy := health.Policy{Retries: 0, RecoveryChecks: 1}
			failed := health.Advance(health.Tracker{Status: health.Up}, false, now, 60_000, policy)
			ok := health.Advance(failed.Tracker, true, now+60_000, 60_000, policy)
			check(s.RecordCheck(ctx, CheckResult{MonitorID: monitor, AtMs: now, Step: failed, CoveredMs: 60_000}))
			check(s.RecordCheck(ctx, CheckResult{MonitorID: monitor, AtMs: now + 60_000, OK: true, LatencyMs: 21, Step: ok, CoveredMs: 60_000}))
			buckets, err := s.CheckBuckets(ctx, monitor, now, 1_800_000)
			check(err)
			if len(buckets) != 1 || buckets[0].Checks != 2 || buckets[0].Failures != 1 || buckets[0].LatencyAvgMs != 21 {
				t.Fatalf("check history: %+v", buckets)
			}
			incidents, err := s.IncidentsByProject(ctx, project)
			check(err)
			if len(incidents) != 1 || incidents[0].Status != "resolved" {
				t.Fatalf("outage lifecycle: %+v", incidents)
			}

			check(s.DeleteProject(ctx, project))
			_, _, err = s.ProjectImage(ctx, project, ImageFavicon)
			if !errors.Is(err, ErrNotFound) {
				t.Fatalf("project image cascade: %v", err)
			}
		})
	}
}
