package web

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"uppies/internal/store"
)

// The detail view of a monitor looks at its last day of checks, in half hours.
const (
	detailWindowMs = dayMs
	detailBucketMs = 30 * 60 * 1000
	detailBuckets  = detailWindowMs / detailBucketMs
)

// The response time chart is drawn in a box of this size, in SVG units.
const (
	chartWidth  = 480
	chartHeight = 96
)

// percentile returns the value that p percent of the sorted values do not
// exceed, by the nearest-rank method. It returns 0 for no values.
func percentile(sorted []int64, p int) int64 {
	if len(sorted) == 0 {
		return 0
	}
	// The rank is p percent of the count, rounded up, and counts from 1.
	rank := (len(sorted)*p + 99) / 100
	return sorted[max(rank, 1)-1]
}

// monitorDetail is what the detail view of one monitor shows.
type monitorDetail struct {
	// Figures are the numbers of the last day.
	Figures []figure
	// Samples is how many successful checks the figures rest on.
	Samples int
	// Line is the point list of the response time line. Slots has one entry
	// per half hour, oldest first, for the tooltips and the failure marks.
	Line  string
	Slots []chartSlot
	// Peak is the highest value on the chart, shown beside it as its scale.
	Peak int64
	// Days and Uptime are the uptime bars over the project's lookback.
	Days   []dayCell
	Uptime string
}

// chartSlot is one half hour of the response time chart.
type chartSlot struct {
	Tip    string
	Failed bool
}

// monitorDetail gathers a monitor's last day of checks and its uptime history.
func (s *Server) monitorDetail(ctx context.Context, project store.Project, m store.Monitor, zone *time.Location) (monitorDetail, error) {
	nowMs := s.now().UnixMilli()
	// The chart starts on a half hour, so that the slots line up with the clock.
	firstSlotMs := (nowMs/detailBucketMs - (detailBuckets - 1)) * detailBucketMs
	latencies, err := s.store.LatenciesSince(ctx, m.ID, firstSlotMs)
	if err != nil {
		return monitorDetail{}, err
	}
	buckets, err := s.store.CheckBuckets(ctx, m.ID, firstSlotMs, detailBucketMs)
	if err != nil {
		return monitorDetail{}, err
	}
	todayStartMs := store.DayStartMs(nowMs)
	rollups, err := s.store.DailyRollups(ctx, project.ID, todayStartMs-int64(project.HistoryDays-1)*dayMs)
	if err != nil {
		return monitorDetail{}, err
	}

	var detail monitorDetail
	detail.Days, detail.Uptime = uptimeStrip(rollups[m.ID], todayStartMs, project.HistoryDays)
	backfillDays(detail.Days, project.Backfill)
	detail.Samples = len(latencies)
	var checks, failures int64
	for _, b := range buckets {
		checks += b.Checks
		failures += b.Failures
	}
	detail.Figures = []figure{
		{Label: fmt.Sprintf("uptime, %d days", project.HistoryDays), Value: strings.TrimSuffix(detail.Uptime, " uptime")},
		{Label: "checks, 24 h", Value: strconv.FormatInt(checks, 10)},
		{Label: "failed, 24 h", Value: strconv.FormatInt(failures, 10)},
	}
	for _, p := range []int{50, 90, 95, 99} {
		value := "no data"
		if len(latencies) > 0 {
			value = fmt.Sprintf("%d ms", percentile(latencies, p))
		}
		detail.Figures = append(detail.Figures, figure{Label: fmt.Sprintf("p%d, 24 h", p), Value: value})
	}
	slowest := "no data"
	if len(latencies) > 0 {
		slowest = fmt.Sprintf("%d ms", latencies[len(latencies)-1])
	}
	detail.Figures = append(detail.Figures, figure{Label: "slowest, 24 h", Value: slowest})
	detail.Line, detail.Slots, detail.Peak = responseChart(buckets, firstSlotMs, zone)
	return detail, nil
}

// responseChart turns half-hour buckets into the points of a line and one
// slot per half hour. A half hour without successful checks breaks nothing:
// the line runs on from the last point to the next.
func responseChart(buckets []store.CheckBucket, firstSlotMs int64, zone *time.Location) (line string, slots []chartSlot, peak int64) {
	byStart := make(map[int64]store.CheckBucket, len(buckets))
	for _, b := range buckets {
		byStart[b.StartMs] = b
		peak = max(peak, b.LatencyAvgMs)
	}
	peak = max(peak, 1)
	var points strings.Builder
	slots = make([]chartSlot, detailBuckets)
	for i := range slots {
		startMs := firstSlotMs + int64(i)*detailBucketMs
		clock := time.UnixMilli(startMs).In(zone).Format("15:04")
		b, checked := byStart[startMs]
		switch {
		case !checked:
			slots[i].Tip = clock + ": no checks"
			continue
		case b.Failures == b.Checks:
			slots[i] = chartSlot{Tip: fmt.Sprintf("%s: all %d checks failed", clock, b.Checks), Failed: true}
			continue
		case b.Failures > 0:
			slots[i] = chartSlot{Tip: fmt.Sprintf("%s: %d ms on average, %d ms at most, %d of %d checks failed", clock, b.LatencyAvgMs, b.LatencyMaxMs, b.Failures, b.Checks), Failed: true}
		default:
			slots[i].Tip = fmt.Sprintf("%s: %d ms on average, %d ms at most", clock, b.LatencyAvgMs, b.LatencyMaxMs)
		}
		// Each point sits in the middle of its slot, and the line keeps 4 units clear of the edges.
		x := (float64(i) + 0.5) / float64(detailBuckets) * chartWidth
		y := float64(chartHeight-4) - float64(b.LatencyAvgMs)/float64(peak)*float64(chartHeight-8)
		fmt.Fprintf(&points, "%.1f,%.1f ", x, y)
	}
	return strings.TrimSpace(points.String()), slots, peak
}

// monitorPage is the data of monitor.html, the public detail view of one monitor.
type monitorPage struct {
	Chrome   chrome
	Project  store.Project
	Monitor  store.Monitor
	Images   map[string]int64
	NavLinks []navLink
	Zone     *time.Location
	ZoneName string
	TZ       string
	State    string
	Word     string
	Detail   monitorDetail
	// Badges are the percentiles there is an 88 by 31 image for.
	Badges []string
	// Origin is the scheme and host of this page, for the embed code.
	Origin string
}

// badgePercentiles maps the file names of the percentile badges to their percentile.
var badgePercentiles = map[string]int{"p90.svg": 90, "p95.svg": 95, "p99.svg": 99}

// publicMonitor returns the monitor a public address names, or ErrNotFound
// when it is hidden or belongs to another project, so that neither can be told apart from a missing one.
func (s *Server) publicMonitor(r *http.Request) (store.Project, store.Monitor, error) {
	project, err := s.store.ProjectBySlug(r.Context(), r.PathValue("slug"))
	if err != nil {
		return store.Project{}, store.Monitor{}, err
	}
	id, err := pathID(r)
	if err != nil {
		return store.Project{}, store.Monitor{}, err
	}
	m, err := s.store.Monitor(r.Context(), id)
	if err != nil {
		return store.Project{}, store.Monitor{}, err
	}
	if m.ProjectID != project.ID || !m.Public {
		return store.Project{}, store.Monitor{}, store.ErrNotFound
	}
	return project, m, nil
}

func (s *Server) handleMonitorPage(w http.ResponseWriter, r *http.Request) {
	project, m, err := s.publicMonitor(r)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	zone, zoneName := pageZone(project, r.URL.Query().Get("tz"))
	detail, err := s.monitorDetail(r.Context(), project, m, zone)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	images, err := s.store.ProjectImageVersions(r.Context(), project.ID)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	scheme := "http"
	if r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https" {
		scheme = "https"
	}
	page := monitorPage{
		Chrome:  chrome{Title: m.Name + " · " + project.Name, Unbranded: !project.ShowPoweredBy},
		Project: project, Monitor: m, Images: images, Zone: zone, ZoneName: zoneName, Detail: detail,
		Badges: []string{"p90", "p95", "p99"}, Origin: scheme + "://" + r.Host,
	}
	page.State, page.Word = componentState(m)
	if zoneName != project.Timezone {
		page.TZ = zoneName
	}
	// Saved links were checked when they were saved, so a problem here is not expected and shows none.
	page.NavLinks, _ = parseNavLinks(project.NavLinks)
	s.render(w, r, http.StatusOK, "monitor.html", page)
}

// handleMonitorBadge serves one percentile of a monitor's response time over
// the last day as an 88 by 31 image.
func (s *Server) handleMonitorBadge(w http.ResponseWriter, r *http.Request) {
	p, known := badgePercentiles[r.PathValue("badge")]
	if !known {
		http.NotFound(w, r)
		return
	}
	_, m, err := s.publicMonitor(r)
	if errors.Is(err, store.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		s.fail(w, r, err)
		return
	}
	latencies, err := s.store.LatenciesSince(r.Context(), m.ID, s.now().UnixMilli()-detailWindowMs)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	state, _ := componentState(m)
	w.Header().Set("Content-Type", "image/svg+xml")
	w.Header().Set("Cache-Control", "max-age=60")
	if _, err := io.WriteString(w, percentileBadge(p, latencies, state)); err != nil {
		s.log.Error("write badge", "path", r.URL.Path, "error", err)
	}
}
