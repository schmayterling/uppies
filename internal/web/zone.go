package web

import (
	_ "embed" // zones.txt is compiled in
	"os"
	"slices"
	"strings"
	"time"

	"uppies/internal/store"
)

// zones.txt holds the zone names of the IANA timezone database, one per
// line, taken from its zone.tab. Go can load a zone by name but cannot list
// the names, so the list ships with the program.
//
//go:embed zones.txt
var zoneList string

// offeredZones are the timezones a visitor can pick on a status page: UTC
// and every zone of the database, in alphabetical order.
var offeredZones = append([]string{"UTC"}, strings.Fields(zoneList)...)

// systemZoneName returns the IANA name of the timezone the server runs in,
// or "UTC" when it cannot be told. Go only knows that zone as "Local", so the
// name is read from where the system keeps it.
func systemZoneName() string {
	candidates := []string{os.Getenv("TZ")}
	if target, err := os.Readlink("/etc/localtime"); err == nil {
		if _, name, found := strings.Cut(target, "zoneinfo/"); found {
			candidates = append(candidates, name)
		}
	}
	for _, name := range candidates {
		if name == "" || name == "Local" {
			continue
		}
		if _, err := time.LoadLocation(name); err == nil {
			return name
		}
	}
	return "UTC"
}

// pageZone picks the timezone a status page shows its times in: the one the
// visitor asked for when it is a real zone, otherwise the project's own.
func pageZone(project store.Project, asked string) (*time.Location, string) {
	for _, name := range []string{asked, project.Timezone} {
		if name == "" {
			continue
		}
		if zone, err := time.LoadLocation(name); err == nil {
			return zone, name
		}
	}
	return time.UTC, "UTC"
}

// zoneChoices lists the zones offered in the picker, with the current one first.
func zoneChoices(project store.Project, current string) []string {
	choices := []string{current}
	for _, name := range append([]string{project.Timezone}, offeredZones...) {
		if !slices.Contains(choices, name) {
			choices = append(choices, name)
		}
	}
	return choices
}
