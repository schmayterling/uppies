package web

import (
	"cmp"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"math"
	"strconv"
)

type badgePaint struct {
	css string
	rgb color.NRGBA
}

type badgePalette struct {
	edge, background, chart, label, value badgePaint
}

func badgeHex(css string) badgePaint {
	rgb, _ := strconv.ParseUint(css[1:], 16, 24)
	if len(css) == 4 {
		return badgePaint{css, color.NRGBA{
			R: uint8(rgb>>8) * 17, G: uint8((rgb>>4)&15) * 17, B: uint8(rgb&15) * 17, A: 255,
		}}
	}
	return badgePaint{css, color.NRGBA{R: uint8(rgb >> 16), G: uint8(rgb >> 8), B: uint8(rgb), A: 255}}
}

func badgeHSL(hue, lightness int) badgePaint {
	// Use the CSS HSL-to-sRGB conversion for the PNG rendition of the swatch.
	const saturation = 0.45
	light := float64(lightness) / 100
	amplitude := saturation * min(light, 1-light)
	component := func(offset float64) uint8 {
		position := math.Mod(offset+float64(hue)/30, 12)
		value := light - amplitude*max(-1, min(position-3, 9-position, 1))
		return uint8(math.Round(value * 255))
	}
	return badgePaint{
		fmt.Sprintf("hsl(%d 45%% %d%%)", hue, lightness),
		color.NRGBA{R: component(0), G: component(8), B: component(4), A: 255},
	}
}

func paletteForBadge(spec badgeSpec) badgePalette {
	palette := badgePalette{
		edge:       badgeHex(cmp.Or(badgeColours[spec.State], badgeColours[stateNone])),
		background: badgeHex("#16181a"),
		chart:      badgeHex(cmp.Or(badgeDarkAccents[spec.State], badgeDarkAccents[stateNone])),
		label:      badgeHex("#d0d5d9"),
		value:      badgeHex("#fff"),
	}
	if spec.Light {
		palette.background, palette.value = palette.value, palette.background
		palette.label = badgeHex("#36424c")
		palette.chart = palette.edge
	}
	if spec.Hue >= 0 {
		palette.edge = badgeHSL(spec.Hue, 32)
		palette.chart = palette.edge
		if !spec.Light {
			palette.chart = badgeHSL(spec.Hue, 66)
		}
	}
	return palette
}

// rasterBadge layers the same shapes as the SVG at exactly 88x31 pixels.
func rasterBadge(spec badgeSpec) *image.NRGBA {
	bounds := image.Rect(0, 0, badgeWidth, badgeHeight)
	img := image.NewNRGBA(bounds)
	palette := paletteForBadge(spec)
	draw.Draw(img, bounds, image.NewUniform(palette.edge.rgb), image.Point{}, draw.Src)
	draw.Draw(img, bounds.Inset(1), image.NewUniform(palette.background.rgb), image.Point{}, draw.Src)

	barColor := palette.chart.rgb
	barColor.A = 59 // 23% opacity
	bars := image.NewUniform(barColor)
	for _, bar := range spec.Bars {
		draw.Draw(img, bar, bars, image.Point{}, draw.Over)
	}
	if len(spec.Line) > 0 {
		mask := image.NewAlpha(bounds)
		var previous image.Point
		for _, point := range spec.Line {
			from := point.Point
			if point.connected {
				from = previous
			}
			paintBadgeSegment(mask, from, point.Point)
			previous = point.Point
		}
		draw.DrawMask(img, bounds, image.NewUniform(palette.chart.rgb), image.Point{}, mask, image.Point{}, draw.Over)
	}

	label, value := badgeOverlays(spec)
	labelInk, valueInk := image.NewUniform(palette.label.rgb), image.NewUniform(palette.value.rgb)
	for _, rect := range label {
		draw.Draw(img, rect, labelInk, image.Point{}, draw.Src)
	}
	for _, rect := range value {
		draw.Draw(img, rect, valueInk, image.Point{}, draw.Src)
	}
	return img
}

// paintBadgeSegment draws a rounded 1.5px stroke. The mask prevents overlaps
// between adjacent segments from raising the opacity.
func paintBadgeSegment(mask *image.Alpha, from, to image.Point) {
	bounds := image.Rect(min(from.X, to.X)-1, min(from.Y, to.Y)-1, max(from.X, to.X)+2, max(from.Y, to.Y)+2)
	bounds = bounds.Intersect(mask.Bounds())
	dx, dy := float64(to.X-from.X), float64(to.Y-from.Y)
	lengthSquared := dx*dx + dy*dy
	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			offsetX, offsetY := float64(x-from.X)+0.5, float64(y-from.Y)+0.5
			position := 0.0
			if lengthSquared > 0 {
				position = min(1, max(0, (offsetX*dx+offsetY*dy)/lengthSquared))
			}
			distance := math.Hypot(offsetX-position*dx, offsetY-position*dy)
			coverage := min(1, max(0, 1.25-distance))
			opacity := uint8(math.Round(coverage * 102)) // 40% opacity
			if opacity > mask.AlphaAt(x, y).A {
				mask.SetAlpha(x, y, color.Alpha{A: opacity})
			}
		}
	}
}
