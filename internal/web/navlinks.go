package web

import (
	"fmt"
	"strings"

	"uppies/internal/alert"
)

// maxNavLinks keeps a status page's top bar to one line on most screens.
const maxNavLinks = 6

// navLink is one link in a status page's top bar.
type navLink struct {
	Label string
	URL   string
}

// parseNavLinks reads the top bar links from their settings text: one link
// per line, written as "label | url". Empty lines are skipped. It returns
// what is wrong with the text, or "" when all of it is usable.
func parseNavLinks(text string) (links []navLink, problem string) {
	for number, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		label, target, found := strings.Cut(line, "|")
		label, target = strings.TrimSpace(label), strings.TrimSpace(target)
		if !found || label == "" || len(label) > 40 {
			return nil, fmt.Sprintf("line %d: write a link as \"label | https://example.com\", with a label of up to 40 characters.", number+1)
		}
		if err := alert.ValidateURL(target); err != nil {
			return nil, fmt.Sprintf("line %d: %s.", number+1, err)
		}
		links = append(links, navLink{Label: label, URL: target})
	}
	if len(links) > maxNavLinks {
		return nil, fmt.Sprintf("keep the top bar to %d links.", maxNavLinks)
	}
	return links, ""
}
