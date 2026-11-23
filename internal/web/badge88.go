package web

import (
	"cmp"
	"fmt"
	"net/url"
	"strconv"
	"strings"
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
	fmt.Fprintf(&b, `<path fill="%s" d="M1 1h%dv%dh-%dz"/>`, badgeColours[spec.State], blockWidth, height-2, blockWidth)
	// One line on the block sits in its middle. Two lines share it.
	labelTop := 12
	if spec.Sub != "" {
		labelTop = 6
	}
	fmt.Fprintf(&b, `<path fill="#fff" d="%s%s"/>`,
		badgeText(spec.Label, centred(spec.Label, 1, blockWidth, 1), labelTop, 1),
		badgeText(spec.Sub, centred(spec.Sub, 1, blockWidth, 1), 18, 1))
	fmt.Fprintf(&b, `<path fill="%s" d="%s"/>`, ink, badgeText(spec.Value, centred(spec.Value, valueLeft, valueSpace, 2), 4, 2))
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
	return badgeSpec{Label: name, Sub: "24h", Value: value, Unit: unit, State: state, Title: title}
}

// badgeStateWords says a state in at most four characters, the width of the large text.
var badgeStateWords = map[string]string{stateOK: "up", stateDown: "down", stateWarn: "warn", stateNone: "wait"}

// statusSpec is the badge of a current state. name is whose state it is.
func statusSpec(name, state, word string) badgeSpec {
	return badgeSpec{
		Label: "now", Value: cmp.Or(word, badgeStateWords[state]), Unit: badgeWord(name, 9), State: state,
		Title: fmt.Sprintf("%s is %s", name, cmp.Or(word, badgeStateWords[state])),
	}
}

// uptimeSpec is the badge of an uptime figure over a number of days. uptime
// is the text uptimePercent gives, such as "99.94% uptime" or "no data yet".
func uptimeSpec(name, uptime string, days int, state string) badgeSpec {
	spec := badgeSpec{Label: "up", Sub: fmt.Sprintf("%dd", days), Value: "--", Unit: "% uptime", State: state}
	spec.Title = fmt.Sprintf("uptime of %s over %d days: no data", name, days)
	if number, _, measured := strings.Cut(uptime, "%"); measured {
		// Four characters hold "100" and "99.9". The figure is cut, never rounded up.
		spec.Value = strings.TrimSuffix(number[:min(len(number), 4)], ".")
		spec.Title = fmt.Sprintf("uptime of %s over %d days: %s%%", name, days, number)
	}
	return spec
}

// badgeOptions applies what the address asks for to a badge: "theme=light"
// for a white ground, and "label=" for other words on the block.
func badgeOptions(spec badgeSpec, query url.Values) badgeSpec {
	spec.Light = query.Get("theme") == "light"
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
