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
	// Area is the shape under the line, filled the way the settings say.
	Area graphArea
	// Days and Uptime are the uptime bars over the project's lookback.
	Days   []dayCell
	Uptime string
}

// chartSlot is one half hour of the response time chart.
type chartSlot struct {
	Tip    string
	Failed bool
}

// detailLook is how a monitor's detail view is drawn where it is shown: on a
// status page that is the page's own lookback, backfill, zone and graph fill.
type detailLook struct {
	Days     int
	Backfill string
	Fill     string
	Zone     *time.Location
}

// pageLook is the look of a status page's detail views, with the graph fill
// chosen for one monitor on that page.
func (s *Server) pageLook(ctx context.Context, project store.Project, placedFill string, zone *time.Location) (detailLook, error) {
	instance, err := s.store.Instance(ctx)
	return detailLook{
		Days: project.HistoryDays, Backfill: project.Backfill, Zone: zone,
		Fill: graphFill(instance, project, placedFill),
	}, err
}

// monitorDetail gathers a monitor's last day of checks and its uptime history.
func (s *Server) monitorDetail(ctx context.Context, m store.Monitor, look detailLook) (monitorDetail, error) {
	zone := look.Zone
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
	rollups, err := s.store.MonitorRollups(ctx, m.ID, todayStartMs-int64(look.Days-1)*dayMs)
	if err != nil {
		return monitorDetail{}, err
	}

	var detail monitorDetail
	detail.Days, detail.Uptime = uptimeStrip(rollups, todayStartMs, look.Days)
	backfillDays(detail.Days, look.Backfill)
	detail.Samples = len(latencies)
	var checks, failures int64
	for _, b := range buckets {
		checks += b.Checks
		failures += b.Failures
	}
	detail.Figures = []figure{
		{Label: fmt.Sprintf("uptime, %d days", look.Days), Value: strings.TrimSuffix(detail.Uptime, " uptime")},
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
	detail.Area = newGraphArea(look.Fill, detail.Line, chartHeight, "area-chart")
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
	Chrome  chrome
	Project store.Project
	Monitor store.Monitor
	// Category is the page's category the monitor is listed under, empty for none.
	Category string
	Images   map[string]int64
	NavLinks []navLink
	Zone     *time.Location
	ZoneName string
	TZ       string
	State    string
	Word     string
	Detail   monitorDetail
}

// badgeKinds are what a component's 88x31 badge can show.
var badgeKinds = []string{"status", "uptime", "bars", "line", "p50", "p90", "p95", "p99"}

// badgePercentiles maps the file names of the percentile badges to their percentile.
var badgePercentiles = map[string]int{"p90.svg": 90, "p95.svg": 95, "p99.svg": 99}

// publicMonitor returns the monitor a public address names, or ErrNotFound
// when the status page does not show it, so that a monitor on no page or on
// another one cannot be told apart from a missing one.
func (s *Server) publicMonitor(r *http.Request) (store.Project, store.PlacedMonitor, error) {
	project, err := s.store.ProjectBySlug(r.Context(), r.PathValue("slug"))
	if err != nil {
		return store.Project{}, store.PlacedMonitor{}, err
	}
	id, err := pathID(r)
	if err != nil {
		return store.Project{}, store.PlacedMonitor{}, err
	}
	shown, err := s.store.MonitorsByProject(r.Context(), project.ID)
	if err != nil {
		return store.Project{}, store.PlacedMonitor{}, err
	}
	for _, m := range shown {
		if m.ID == id {
			return project, m, nil
		}
	}
	return store.Project{}, store.PlacedMonitor{}, store.ErrNotFound
}

func (s *Server) handleMonitorPage(w http.ResponseWriter, r *http.Request) {
	project, placed, err := s.publicMonitor(r)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	m := placed.Monitor
	zone, zoneName := pageZone(project, r.URL.Query().Get("tz"))
	look, err := s.pageLook(r.Context(), project, placed.GraphFill, zone)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	detail, err := s.monitorDetail(r.Context(), m, look)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	images, err := s.store.ProjectImageVersions(r.Context(), project.ID)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	page := monitorPage{
		Chrome:  chrome{Title: m.Name + " · " + project.Name, Unbranded: !project.ShowPoweredBy, Favicon: s.faviconAddress(r.Context())},
		Project: project, Monitor: m, Category: placed.Category, Images: images, Zone: zone, ZoneName: zoneName, Detail: detail,
	}
	page.State, page.Word = componentState(m)
	if zoneName != project.Timezone {
		page.TZ = zoneName
	}
	// Saved links were checked when they were saved, so a problem here is not expected and shows none.
	page.NavLinks, _ = parseNavLinks(project.NavLinks)
	s.render(w, r, http.StatusOK, "monitor.html", page)
}

// handleMonitorBadge serves an 88x31 image of one monitor on a status
// page. "88x31.svg" takes what to show from the address: show=status, uptime,
// p50, p90, p95 or p99, with days= for the uptime, and theme= and label= for
// the look. "p90.svg", "p95.svg" and "p99.svg" are short for the percentiles.
func (s *Server) handleMonitorBadge(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	show := query.Get("show")
	if p, short := badgePercentiles[r.PathValue("badge")]; short {
		show = fmt.Sprintf("p%d", p)
	} else if r.PathValue("badge") != "88x31.svg" {
		http.NotFound(w, r)
		return
	}
	project, m, err := s.publicMonitor(r)
	if errors.Is(err, store.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		s.fail(w, r, err)
		return
	}
	state, word := componentState(m.Monitor)
	nowMs := s.now().UnixMilli()
	var spec badgeSpec
	switch show {
	case "", "status":
		spec = statusSpec(m.Name, state, badgeStateWords[state])
		if word == "paused" {
			spec = statusSpec(m.Name, state, "off")
		}
	case "uptime", "bars":
		days := badgeDays(query, project.HistoryDays)
		if show == "bars" {
			days = badgeBarDays
		}
		todayStartMs := store.DayStartMs(nowMs)
		rollups, err := s.store.MonitorRollups(r.Context(), m.ID, todayStartMs-int64(days-1)*dayMs)
		if err != nil {
			s.fail(w, r, err)
			return
		}
		history, uptime := uptimeStrip(rollups, todayStartMs, days)
		spec = uptimeSpec(m.Name, uptime, history, state)
		if show == "bars" {
			spec.Title = fmt.Sprintf("uptime of %s per day over %d days: %s", m.Name, days, uptime)
		}
	case "line":
		firstSlotMs := (nowMs/detailBucketMs - (detailBuckets - 1)) * detailBucketMs
		buckets, err := s.store.CheckBuckets(r.Context(), m.ID, firstSlotMs, detailBucketMs)
		if err != nil {
			s.fail(w, r, err)
			return
		}
		spec = lineSpec(m.Name, buckets, firstSlotMs, state)
	case "p50", "p90", "p95", "p99":
		p, _ := strconv.Atoi(strings.TrimPrefix(show, "p"))
		latencies, err := s.store.LatenciesSince(r.Context(), m.ID, nowMs-detailWindowMs)
		if err != nil {
			s.fail(w, r, err)
			return
		}
		spec = percentileSpec(p, latencies, state)
	default:
		http.NotFound(w, r)
		return
	}
	s.writeBadge(w, r, badgeOptions(spec, query))
}

// handlePageBadge serves an 88x31 image of a whole status page: its
// current state, or with show=uptime the uptime of everything it shows.
func (s *Server) handlePageBadge(w http.ResponseWriter, r *http.Request) {
	project, err := s.store.ProjectBySlug(r.Context(), r.PathValue("slug"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	page, err := s.statusPage(r.Context(), project, "")
	if err != nil {
		s.fail(w, r, err)
		return
	}
	query := r.URL.Query()
	var spec badgeSpec
	switch query.Get("show") {
	case "", "status":
		spec = statusSpec(project.Name, page.BannerState, pageBadgeWords[page.Overall])
	case "uptime":
		days := badgeDays(query, project.HistoryDays)
		todayStartMs := store.DayStartMs(s.now().UnixMilli())
		rollups, err := s.store.DailyRollups(r.Context(), project.ID, todayStartMs-int64(days-1)*dayMs)
		if err != nil {
			s.fail(w, r, err)
			return
		}
		combined := make(map[int64]store.DayRollup, days)
		for _, monitorDays := range rollups {
			for _, d := range monitorDays {
				day := combined[d.DayStartMs]
				day.DayStartMs = d.DayStartMs
				day.CoveredMs += d.CoveredMs
				day.DownMs += d.DownMs
				combined[d.DayStartMs] = day
			}
		}
		daily := make([]store.DayRollup, 0, len(combined))
		for _, day := range combined {
			daily = append(daily, day)
		}
		history, uptime := uptimeStrip(daily, todayStartMs, days)
		spec = uptimeSpec(project.Name, uptime, history, page.BannerState)
	default:
		http.NotFound(w, r)
		return
	}
	s.writeBadge(w, r, badgeOptions(spec, query))
}

// pageBadgeWords says a status page's overall state in at most four characters.
var pageBadgeWords = map[overallStatus]string{
	overallUp: "up", overallPartial: "down", overallDown: "down", overallIncident: "warn",
	overallWaiting: "wait", overallMaintenance: "fix",
}

func (s *Server) writeBadge(w http.ResponseWriter, r *http.Request, spec badgeSpec) {
	w.Header().Set("Content-Type", "image/svg+xml")
	w.Header().Set("Cache-Control", "max-age=60")
	if _, err := io.WriteString(w, drawBadge(spec)); err != nil {
		s.log.Error("write badge", "path", r.URL.Path, "error", err)
	}
}
