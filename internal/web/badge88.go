package web

import (
	"fmt"
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

// percentileBadge draws the 88 by 31 badge of one percentile: the percentile
// and its window on a block in the monitor's state colour, and the value beside it.
func percentileBadge(p int, sortedLatencies []int64, state string) string {
	const (
		width, height = 88, 31
		blockWidth    = 30
		// The value has the space right of the block, inside the border.
		valueLeft  = 1 + blockWidth
		valueSpace = width - 1 - valueLeft
	)
	value, unit := badgeValue(percentile(sortedLatencies, p), len(sortedLatencies) > 0)
	name := fmt.Sprintf("p%d", p)
	label := fmt.Sprintf("%s response time over 24 hours: %s %s", name, value, unit)
	if len(sortedLatencies) == 0 {
		label = name + " response time over 24 hours: no data"
	}
	nameLeft := 1 + (blockWidth-badgeTextWidth(name, 1))/2
	var b strings.Builder
	fmt.Fprintf(&b, `<svg xmlns="http://www.w3.org/2000/svg" width="%d" height="%d" viewBox="0 0 %d %d" shape-rendering="crispEdges" role="img" aria-label="%s"><title>%s</title>`,
		width, height, width, height, label, label)
	fmt.Fprintf(&b, `<path fill="#5b6166" d="M0 0h%dv%dh-%dz"/><path fill="#16181a" d="M1 1h%dv%dh-%dz"/>`,
		width, height, width, width-2, height-2, width-2)
	fmt.Fprintf(&b, `<path fill="%s" d="M1 1h%dv%dh-%dz"/>`, badgeColours[state], blockWidth, height-2, blockWidth)
	fmt.Fprintf(&b, `<path fill="#fff" d="%s%s%s"/>`,
		badgeText(name, nameLeft, 6, 1), badgeText("24h", nameLeft, 18, 1),
		badgeText(value, valueLeft+(valueSpace-badgeTextWidth(value, 2))/2, 4, 2))
	fmt.Fprintf(&b, `<path fill="#b6bcc2" d="%s"/></svg>`,
		badgeText(unit, valueLeft+(valueSpace-badgeTextWidth(unit, 1))/2, 21, 1))
	return b.String()
}
