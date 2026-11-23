package web

import (
	"cmp"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"uppies/internal/store"
)

// An 88 by 31 badge is drawn from rectangles only, glyphs included. With no
// font to look up, the same numbers give the same image byte for byte, in
// every browser and on every machine.

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

// badgeText returns the path data that draws text with its top left corner at
// x, y. Each run of lit pixels in a row becomes one rectangle. A character
// without a glyph leaves a gap.
func badgeText(text string, x, y, scale int) string {
	var d strings.Builder
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
				fmt.Fprintf(&d, "M%d %dh%dv%dh-%dz",
					x+(i*glyphAdvance+start)*scale, y+row*scale, (col-start+1)*scale, scale, (col-start+1)*scale)
			}
		}
	}
	return d.String()
}

// badgeValue writes a response time in at most four characters, with its unit.
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

// badgeSpec is everything an 88 by 31 badge shows. The same spec always
// draws the same image.
type badgeSpec struct {
	// Label and Sub are the two short lines on the coloured block, such as "p99" and "24h".
	Label, Sub string
	// Value is the large text, at most four characters, and Unit the small line under it.
	Value, Unit string
	// State picks the colour of the block.
	State string
	// Light draws dark text on a white ground. The default is light text on a dark one.
	Light bool
	// Title is the image's accessible name.
	Title string
	// Graph replaces the large text with a small chart: one column per entry,
	// each up to graphHeight pixels tall. Gap leaves a pixel between columns.
	Graph []graphColumn
	Gap   bool
	// Hue colours the block with a hue of the colour wheel, 0 to 360,
	// in place of the state's colour. Negative keeps the state's colour.
	Hue int
}

// graphColumn is one column of a badge's chart. An empty colour means the badge's own ink.
type graphColumn struct {
	Height int
	Colour string
}

// graphHeight is the height of a badge's chart in pixels.
const graphHeight = 16

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

// drawBadge draws an 88 by 31 badge: the label on a block in the state's
// colour, and the value with its unit beside it.
func drawBadge(spec badgeSpec) string {
	const (
		width, height = 88, 31
		blockWidth    = 30
		// The value has the space right of the block, inside the border.
		valueLeft  = 1 + blockWidth
		valueSpace = width - 1 - valueLeft
	)
	border, ground, ink, quiet := "#5b6166", "#16181a", "#fff", "#b6bcc2"
	if spec.Light {
		border, ground, ink, quiet = "#b6bcc2", "#fff", "#16181a", "#5b6166"
	}
	centred := func(text string, left, space, scale int) int { return left + (space-badgeTextWidth(text, scale))/2 }
	var b strings.Builder
	fmt.Fprintf(&b, `<svg xmlns="http://www.w3.org/2000/svg" width="%d" height="%d" viewBox="0 0 %d %d" shape-rendering="crispEdges" role="img" aria-label="%s"><title>%s</title>`,
		width, height, width, height, spec.Title, spec.Title)
	fmt.Fprintf(&b, `<path fill="%s" d="M0 0h%dv%dh-%dz"/><path fill="%s" d="M1 1h%dv%dh-%dz"/>`,
		border, width, height, width, ground, width-2, height-2, width-2)
	block := badgeColours[spec.State]
	if spec.Hue >= 0 {
		block = fmt.Sprintf("hsl(%d 45%% 32%%)", spec.Hue)
	}
	fmt.Fprintf(&b, `<path fill="%s" d="M1 1h%dv%dh-%dz"/>`, block, blockWidth, height-2, blockWidth)
	// One line on the block sits in its middle. Two lines share it.
	labelTop := 12
	if spec.Sub != "" {
		labelTop = 6
	}
	fmt.Fprintf(&b, `<path fill="#fff" d="%s%s"/>`,
		badgeText(spec.Label, centred(spec.Label, 1, blockWidth, 1), labelTop, 1),
		badgeText(spec.Sub, centred(spec.Sub, 1, blockWidth, 1), 18, 1))
	if len(spec.Graph) > 0 {
		// The chart stands on a line 4 pixels under the top of the value's place, centred like the value.
		step := 1
		if spec.Gap {
			step = 2
		}
		left := valueLeft + (valueSpace-(len(spec.Graph)*step-step+1))/2
		for i, column := range spec.Graph {
			if column.Height > 0 {
				fmt.Fprintf(&b, `<path fill="%s" d="M%d %dh1v%dh-1z"/>`, cmp.Or(column.Colour, ink), left+i*step, 3+graphHeight-column.Height, column.Height)
			}
		}
	} else {
		fmt.Fprintf(&b, `<path fill="%s" d="%s"/>`, ink, badgeText(spec.Value, centred(spec.Value, valueLeft, valueSpace, 2), 4, 2))
	}
	fmt.Fprintf(&b, `<path fill="%s" d="%s"/></svg>`, quiet, badgeText(spec.Unit, centred(spec.Unit, valueLeft, valueSpace, 1), 21, 1))
	return b.String()
}

// percentileBadge draws the 88 by 31 badge of one percentile: the percentile
// and its window on a block in the monitor's state colour, and the value beside it.
func percentileBadge(p int, sortedLatencies []int64, state string) string {
	return drawBadge(percentileSpec(p, sortedLatencies, state))
}

func percentileSpec(p int, sortedLatencies []int64, state string) badgeSpec {
	value, unit := badgeValue(percentile(sortedLatencies, p), len(sortedLatencies) > 0)
	name := fmt.Sprintf("p%d", p)
	title := fmt.Sprintf("%s response time over 24 hours: %s %s", name, value, unit)
	if len(sortedLatencies) == 0 {
		title = name + " response time over 24 hours: no data"
	}
	return badgeSpec{Label: name, Sub: "24h", Value: value, Unit: unit, State: state, Title: title, Hue: -1}
}

// badgeStateWords says a state in at most four characters, the width of the large text.
var badgeStateWords = map[string]string{stateOK: "up", stateDown: "down", stateWarn: "warn", stateNone: "wait"}

// statusSpec is the badge of a current state. name is whose state it is.
func statusSpec(name, state, word string) badgeSpec {
	return badgeSpec{
		Label: "now", Value: cmp.Or(word, badgeStateWords[state]), Unit: badgeWord(name, 9), State: state, Hue: -1,
		Title: fmt.Sprintf("%s is %s", name, cmp.Or(word, badgeStateWords[state])),
	}
}

// uptimeSpec is the badge of an uptime figure over a number of days. uptime
// is the text uptimePercent gives, such as "99.94% uptime" or "no data yet".
func uptimeSpec(name, uptime string, days int, state string) badgeSpec {
	spec := badgeSpec{Label: "up", Sub: fmt.Sprintf("%dd", days), Value: "--", Unit: "% uptime", State: state, Hue: -1}
	spec.Title = fmt.Sprintf("uptime of %s over %d days: no data", name, days)
	if number, _, measured := strings.Cut(uptime, "%"); measured {
		// Four characters hold "100" and "99.9". The figure is cut, never rounded up.
		spec.Value = strings.TrimSuffix(number[:min(len(number), 4)], ".")
		spec.Title = fmt.Sprintf("uptime of %s over %d days: %s%%", name, days, number)
	}
	return spec
}

// badgeBarDays is how many days a badge's uptime chart shows: one column and one gap per day.
const badgeBarDays = 27

// barsSpec is the badge with a small uptime chart: one column per day, the
// newest on the right, shorter and coloured on a day with downtime.
func barsSpec(name string, days []dayCell, state string) badgeSpec {
	heights := map[string]int{stateOK: graphHeight, stateWarn: 11, stateDown: 6, stateNone: graphHeight}
	spec := badgeSpec{Label: "up", Sub: fmt.Sprintf("%dd", len(days)), Unit: "per day", State: state, Gap: true, Hue: -1}
	spec.Title = fmt.Sprintf("uptime of %s per day over %d days", name, len(days))
	for _, day := range days {
		colour := badgeColours[day.State]
		if day.State == stateOK {
			colour = "#2f9e5b"
		}
		spec.Graph = append(spec.Graph, graphColumn{Height: heights[day.State], Colour: colour})
	}
	return spec
}

// lineSpec is the badge with a small response time chart: one column per
// half hour of the last day, as tall as that half hour's average against the slowest one.
func lineSpec(name string, buckets []store.CheckBucket, firstSlotMs int64, state string) badgeSpec {
	spec := badgeSpec{Label: "ms", Sub: "24h", Unit: "no data", State: state, Hue: -1}
	spec.Title = "response time of " + name + " over 24 hours: no data"
	var peak int64
	byStart := make(map[int64]int64, len(buckets))
	for _, b := range buckets {
		byStart[b.StartMs] = b.LatencyAvgMs
		peak = max(peak, b.LatencyAvgMs)
	}
	if peak == 0 {
		return spec
	}
	for i := range int(detailBuckets) {
		average := byStart[firstSlotMs+int64(i)*detailBucketMs]
		column := graphColumn{}
		if average > 0 {
			column.Height = max(1, int(average*graphHeight/peak))
		}
		spec.Graph = append(spec.Graph, column)
	}
	value, unit := badgeValue(peak, true)
	spec.Unit = "top " + value + unit
	spec.Title = fmt.Sprintf("response time of %s over 24 hours, at most %s %s per half hour", name, value, unit)
	return spec
}

// badgeOptions applies what the address asks for to a badge: "theme=light"
// for a white ground, "label=" for other words on the block, and "color="
// with a hue from 0 to 360 for a block in that colour.
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
