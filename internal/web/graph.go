package web

import (
	"fmt"
	"net/url"
	"slices"
	"strings"

	"uppies/internal/store"
)

// graphFillInherit is the form value for "let the wider setting decide",
// which is stored as an empty string.
const graphFillInherit = "default"

// scopedGraphFills are the choices of a status page and of a monitor.
var scopedGraphFills = append([]string{graphFillInherit}, store.GraphFills...)

// graphFill picks how a graph is filled: the choice made for the monitor on
// this status page, else the page's, else the instance's.
func graphFill(instance store.InstanceSettings, project store.Project, placed string) string {
	for _, fill := range []string{placed, project.GraphFill, instance.GraphFill} {
		if fill != "" {
			return fill
		}
	}
	return "none"
}

// scopedGraphFill reads the graph fill of a status page or a monitor from a
// form. A missing field keeps the current value.
func scopedGraphFill(form url.Values, current string, problems map[string]string) string {
	switch value := form.Get("graph_fill"); {
	case value == "":
		return current
	case value == graphFillInherit:
		return ""
	case slices.Contains(store.GraphFills, value):
		return value
	default:
		problems["graph_fill"] = "choose one of the options."
		return current
	}
}

// graphArea is the shape under a graph's line.
type graphArea struct {
	// Fill is "gradient", "filled" or "none".
	Fill string
	// Points outline the area: the line, closed along the bottom of the box.
	Points string
	// ID names the gradient, which has to be unique on a page.
	ID string
}

// newGraphArea closes a line's points along the bottom of a box of the given
// height. A line of fewer than two points has no area under it.
func newGraphArea(fill, line string, height int, id string) graphArea {
	points := strings.Fields(line)
	if fill == "none" || len(points) < 2 {
		return graphArea{Fill: "none"}
	}
	firstX, _, _ := strings.Cut(points[0], ",")
	lastX, _, _ := strings.Cut(points[len(points)-1], ",")
	return graphArea{Fill: fill, Points: fmt.Sprintf("%s,%d %s %s,%d", firstX, height, line, lastX, height), ID: id}
}
