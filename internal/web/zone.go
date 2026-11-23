package web

import (
	_ "embed" // zones.txt is compiled in
	"os"
	"strings"
	"time"

	"uppies/internal/store"
)

// zones.txt lists the names in Go's lib/time/zoneinfo.zip, except UTC.
// It includes fixed offsets and compatibility aliases from the bundled database.
// Regenerate it from the Go toolchain's archive when updating the list.
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
	choices := make([]string, 0, len(offeredZones)+2)
	choices = append(choices, current)
	if project.Timezone != current {
		choices = append(choices, project.Timezone)
	}
	for _, name := range offeredZones {
		if name != current && name != project.Timezone {
			choices = append(choices, name)
		}
	}
	return choices
}
