package web

import (
	"cmp"
	"fmt"
	"html"
	"image"
	"net/url"
	"strconv"
	"strings"

	"uppies/internal/store"
)

// Both badge formats share the chart geometry and the built-in pixel font.

// badgeGlyphs is a 5 by 7 pixel font with the characters a badge needs.
var badgeGlyphs = map[rune][7]string{
	'0': {".###.", "#...#", "#..##", "#.#.#", "##..#", "#...#", ".###."},
	'1': {"..#..", ".##..", "..#..", "..#..", "..#..", "..#..", ".###."},
	'2': {".###.", "#...#", "....#", "...#.", "..#..", ".#...", "#####"},
	'3': {".###.", "#...#", "....#", "..##.", "....#", "#...#", ".###."},
	'4': {"...#.", "..##.", ".#.#.", "#..#.", "#####", "...#.", "...#."},
	'5': {"#####", "#....", "####.", "....#", "....#", "#...#", ".###."},
	'6': {".###.", "#....", "#....", "####.", "#...#", "#...#", ".###."},
	'7': {"#####", "....#", "...#.", "..#..", "..#..", "..#..", "..#.."},
	'8': {".###.", "#...#", "#...#", ".###.", "#...#", "#...#", ".###."},
	'9': {".###.", "#...#", "#...#", ".####", "....#", "....#", ".###."},
	'p': {".....", ".....", "####.", "#...#", "####.", "#....", "#...."},
	'm': {".....", ".....", "##.#.", "#.#.#", "#.#.#", "#.#.#", "#.#.#"},
	's': {".....", ".....", ".####", "#....", ".###.", "....#", "####."},
	'h': {"#....", "#....", "####.", "#...#", "#...#", "#...#", "#...#"},
	'a': {".....", ".....", ".###.", "....#", ".####", "#...#", ".####"},
	'b': {"#....", "#....", "####.", "#...#", "#...#", "#...#", "####."},
	'c': {".....", ".....", ".###.", "#....", "#....", "#...#", ".###."},
	'd': {"....#", "....#", ".####", "#...#", "#...#", "#...#", ".####"},
	'e': {".....", ".....", ".###.", "#...#", "#####", "#....", ".###."},
	'f': {"..##.", ".#...", "###..", ".#...", ".#...", ".#...", ".#..."},
	'g': {".....", ".....", ".####", "#...#", ".####", "....#", ".###."},
	'i': {"..#..", ".....", ".##..", "..#..", "..#..", "..#..", ".###."},
	'j': {"...#.", ".....", "..##.", "...#.", "...#.", "#..#.", ".##.."},
	'k': {"#....", "#....", "#..#.", "#.#..", "##...", "#.#..", "#..#."},
	'l': {".##..", "..#..", "..#..", "..#..", "..#..", "..#..", ".###."},
	'n': {".....", ".....", "####.", "#...#", "#...#", "#...#", "#...#"},
	'o': {".....", ".....", ".###.", "#...#", "#...#", "#...#", ".###."},
	'q': {".....", ".....", ".####", "#...#", ".####", "....#", "....#"},
	'r': {".....", ".....", "#.##.", "##..#", "#....", "#....", "#...."},
	't': {".#...", ".#...", "###..", ".#...", ".#...", ".#..#", "..##."},
	'u': {".....", ".....", "#...#", "#...#", "#...#", "#..##", ".##.#"},
	'v': {".....", ".....", "#...#", "#...#", "#...#", ".#.#.", "..#.."},
	'w': {".....", ".....", "#...#", "#...#", "#.#.#", "#.#.#", ".#.#."},
	'x': {".....", ".....", "#...#", ".#.#.", "..#..", ".#.#.", "#...#"},
	'y': {".....", ".....", "#...#", "#...#", ".####", "....#", ".###."},
	'z': {".....", ".....", "#####", "...#.", "..#..", ".#...", "#####"},
	'%': {"##..#", "##.#.", "...#.", "..#..", ".#...", ".#.##", "#..##"},
	'.': {".....", ".....", ".....", ".....", ".....", ".##..", ".##.."},
	'-': {".....", ".....", ".....", "#####", ".....", ".....", "....."},
}

// Glyphs are 5 pixels wide and sit 1 pixel apart.
const (
	glyphWidth   = 5
	glyphAdvance = glyphWidth + 1
)

// badgeTextWidth returns the width of text at a pixel scale.
func badgeTextWidth(text string, scale int) int {
	return (len(text)*glyphAdvance - 1) * scale
}

// badgeTextRects draws each run of lit pixels in one rectangle.
func badgeTextRects(text string, x, y, scale int) []image.Rectangle {
	var rects []image.Rectangle
	for i, char := range text {
		glyph := badgeGlyphs[char]
		for row, pixels := range glyph {
			for col := 0; col < len(pixels); col++ {
				if pixels[col] != '#' {
					continue
				}
				start := col
				for col+1 < len(pixels) && pixels[col+1] == '#' {
					col++
				}
				left := x + (i*glyphAdvance+start)*scale
				top := y + row*scale
				width := (col - start + 1) * scale
				rects = append(rects, image.Rect(left, top, left+width, top+scale))
			}
		}
	}
	return rects
}

func badgeRectPath(rects []image.Rectangle) string {
	var path strings.Builder
	for _, r := range rects {
		fmt.Fprintf(&path, "M%d %dh%dv%dh-%dz", r.Min.X, r.Min.Y, r.Dx(), r.Dy(), r.Dx())
	}
	return path.String()
}

// badgeValue formats a response time in milliseconds or tenths of a second.
func badgeValue(ms int64, known bool) (value, unit string) {
	switch {
	case !known:
		return "--", "ms"
	case ms < 10_000:
		return fmt.Sprintf("%d", ms), "ms"
	default:
		// Tenths of a second, rounded down: 12345 ms reads 12.3 s.
		return fmt.Sprintf("%d.%d", ms/1000, ms%1000/100), "s"
	}
}

// badgeSpec holds the two text overlays and the optional chart behind them.
type badgeSpec struct {
	Label, Window, Value string
	State                string
	Light                bool
	Title                string
	Bars                 []image.Rectangle
	Line                 []badgePoint
	// Hue overrides the state's accent when it is non-negative.
	Hue int
}

const (
	badgeWidth  = 88
	badgeHeight = 31
	badgeInset  = 5
)

// The same state colours as the public pages, lifted for dark badge charts.
var badgeDarkAccents = map[string]string{
	stateOK: "#b4e6a4", stateWarn: "#edcf8f", stateDown: "#ffb0b0", stateNone: "#b6bcc2",
}

// badgeWord cuts text down to what the pixel font can draw and a line can
// hold: lowercase letters, digits and a few signs, at most max characters.
func badgeWord(text string, max int) string {
	var word strings.Builder
	for _, char := range strings.ToLower(text) {
		if _, drawn := badgeGlyphs[char]; drawn || char == ' ' {
			word.WriteRune(char)
		}
		if word.Len() == max {
			break
		}
	}
	return strings.TrimSpace(word.String())
}

func badgeOverlays(spec badgeSpec) (labelRects, valueRects []image.Rectangle) {
	label := strings.TrimSpace(spec.Label + " " + spec.Window)
	scale := 2
	if badgeTextWidth(spec.Value, scale) > badgeWidth-2*badgeInset {
		scale = 1
	}
	x := badgeWidth - badgeInset - badgeTextWidth(spec.Value, scale)
	y := badgeHeight - 4 - 7*scale
	return badgeTextRects(label, badgeInset, 4, 1), badgeTextRects(spec.Value, x, y, scale)
}

// drawBadge layers a chart behind a small label and a prominent value.
func drawBadge(spec badgeSpec) string {
	palette := paletteForBadge(spec)
	title := html.EscapeString(spec.Title)
	var b strings.Builder
	fmt.Fprintf(&b, `<svg xmlns="http://www.w3.org/2000/svg" width="%d" height="%d" viewBox="0 0 %d %d" shape-rendering="crispEdges" role="img" aria-label="%s"><title>%s</title>`,
		badgeWidth, badgeHeight, badgeWidth, badgeHeight, title, title)
	fmt.Fprintf(&b, `<rect width="%d" height="%d" fill="%s"/><rect x="1" y="1" width="%d" height="%d" fill="%s"/>`,
		badgeWidth, badgeHeight, palette.edge.css, badgeWidth-2, badgeHeight-2, palette.background.css)
	if len(spec.Bars) > 0 {
		fmt.Fprintf(&b, `<path fill="%s" fill-opacity=".23" d="%s"/>`, palette.chart.css, badgeRectPath(spec.Bars))
	}
	if len(spec.Line) > 0 {
		fmt.Fprintf(&b, `<path fill="none" stroke="%s" stroke-opacity=".4" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round" shape-rendering="geometricPrecision" d="%s"/>`, palette.chart.css, badgeLinePath(spec.Line))
	}
	label, value := badgeOverlays(spec)
	fmt.Fprintf(&b, `<path fill="%s" d="%s"/><path fill="%s" d="%s"/></svg>`,
		palette.label.css, badgeRectPath(label), palette.value.css, badgeRectPath(value))
	return b.String()
}

// percentileBadge draws the 88x31 badge of one percentile.
func percentileBadge(p int, sortedLatencies []int64, state string) string {
	return drawBadge(percentileSpec(p, sortedLatencies, state))
}

func percentileSpec(p int, sortedLatencies []int64, state string) badgeSpec {
	name := fmt.Sprintf("p%d", p)
	spec := badgeSpec{Label: name, Window: "24h", Value: "--", State: state, Hue: -1}
	if len(sortedLatencies) == 0 {
		spec.Title = name + " response time over 24 hours: no data"
		return spec
	}
	value, unit := badgeValue(percentile(sortedLatencies, p), true)
	spec.Value = value + unit
	spec.Title = fmt.Sprintf("%s response time over 24 hours: %s %s", name, value, unit)
	// The line is a distribution of the same successful checks, from fastest to slowest.
	samples := sortedLatencies
	if len(samples) > int(detailBuckets) {
		samples = make([]int64, detailBuckets)
		for i := range samples {
			samples[i] = sortedLatencies[i*(len(sortedLatencies)-1)/(len(samples)-1)]
		}
	}
	spec.Line = badgeLine(samples, sortedLatencies[len(sortedLatencies)-1])
	return spec
}

var badgeStateWords = map[string]string{stateOK: "up", stateDown: "down", stateWarn: "warn", stateNone: "wait"}

// statusSpec is the badge of a current state. name is whose state it is.
func statusSpec(name, state, word string) badgeSpec {
	value := cmp.Or(word, badgeStateWords[state])
	return badgeSpec{
		Label: cmp.Or(badgeWord(name, 13), "status"), Value: value, State: state, Hue: -1,
		Title: fmt.Sprintf("%s is %s", name, value),
	}
}

// uptimeSpec is the badge of an uptime figure and its daily history.
func uptimeSpec(name, uptime string, days []dayCell, state string) badgeSpec {
	spec := badgeSpec{
		Label: "uptime", Window: fmt.Sprintf("%dd", len(days)), Value: "--",
		State: state, Hue: -1, Bars: badgeBars(days),
	}
	spec.Title = fmt.Sprintf("uptime of %s over %d days: no data", name, len(days))
	if number, _, measured := strings.Cut(uptime, "%"); measured {
		spec.Value = number + "%"
		spec.Title = fmt.Sprintf("uptime of %s over %d days: %s", name, len(days), spec.Value)
	}
	return spec
}

// badgeBarDays is the window of the fixed daily history badge.
const badgeBarDays = 27

// badgeBars leaves gaps for days without data. Long windows share one bar
// across adjacent days, sized by the worst day with data in that group.
func badgeBars(days []dayCell) []image.Rectangle {
	const plotWidth = badgeWidth - 2
	count := min(len(days), badgeBarDays)
	var bars []image.Rectangle
	for i := range count {
		start, end := i*len(days)/count, (i+1)*len(days)/count
		height := 0
		for _, day := range days[start:end] {
			var dayHeight int
			switch day.State {
			case stateOK:
				dayHeight = 22
			case stateWarn:
				dayHeight = 14
			case stateDown:
				dayHeight = 7
			default:
				continue
			}
			if height == 0 || dayHeight < height {
				height = dayHeight
			}
		}
		if height > 0 {
			left, right := 1+i*plotWidth/count, 1+(i+1)*plotWidth/count
			// Leave a one-pixel gap at the right of each slot.
			bars = append(bars, image.Rect(left, badgeHeight-1-height, right-1, badgeHeight-1))
		}
	}
	return bars
}

type badgePoint struct {
	image.Point
	connected bool
}

// badgeLine draws a line across a series with gaps wherever samples are negative.
func badgeLine(samples []int64, peak int64) []badgePoint {
	const plotInset, plotTop, plotBottom = 2, 4, badgeHeight - 3
	var points []badgePoint
	connected := false
	for i, ms := range samples {
		if ms < 0 {
			connected = false
			continue
		}
		x := plotInset + i*(badgeWidth-2*plotInset)/max(1, len(samples)-1)
		if len(samples) == 1 {
			x = badgeWidth / 2
		}
		y := plotBottom - int(float64(ms)/float64(max(1, peak))*(plotBottom-plotTop))
		points = append(points, badgePoint{Point: image.Pt(x, y), connected: connected})
		connected = true
	}
	return points
}

func badgeLinePath(points []badgePoint) string {
	var path strings.Builder
	for _, point := range points {
		if point.connected {
			fmt.Fprintf(&path, "L%d %d", point.X, point.Y)
		} else {
			// A rounded zero-length line is a dot when the sample has no neighbours.
			fmt.Fprintf(&path, "M%d %dh0", point.X, point.Y)
		}
	}
	return path.String()
}

// lineSpec shows half-hour averages and the highest one over the last day.
func lineSpec(name string, buckets []store.CheckBucket, firstSlotMs int64, state string) badgeSpec {
	spec := badgeSpec{Label: "peak", Window: "24h", Value: "--", State: state, Hue: -1}
	spec.Title = "response time of " + name + " over 24 hours: no data"
	var samples [detailBuckets]int64
	for i := range samples {
		samples[i] = -1
	}
	peak := int64(-1)
	for _, b := range buckets {
		slot := (b.StartMs - firstSlotMs) / detailBucketMs
		if b.StartMs < firstSlotMs || slot >= detailBuckets || b.Checks <= b.Failures {
			continue
		}
		samples[slot] = b.LatencyAvgMs
		peak = max(peak, b.LatencyAvgMs)
	}
	if peak < 0 {
		return spec
	}
	value, unit := badgeValue(peak, true)
	spec.Value, spec.Line = value+unit, badgeLine(samples[:], peak)
	spec.Title = fmt.Sprintf("response time of %s over 24 hours, at most %s %s per half hour", name, value, unit)
	return spec
}

// badgeOptions applies the theme, label and accent requested in the address.
func badgeOptions(spec badgeSpec, query url.Values) badgeSpec {
	spec.Light = query.Get("theme") == "light"
	if hue, err := strconv.Atoi(query.Get("color")); err == nil && hue >= 0 && hue <= 360 {
		spec.Hue = hue
	}
	if label := badgeWord(query.Get("label"), 4); label != "" {
		spec.Label = label
	}
	return spec
}

// badgeDays reads the "days" a badge's uptime covers: 1 to 365, else the fallback.
func badgeDays(query url.Values, fallback int) int {
	days, err := strconv.Atoi(query.Get("days"))
	if err != nil || days < 1 || days > 365 {
		return fallback
	}
	return days
}
