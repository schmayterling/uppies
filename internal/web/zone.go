package web

import (
	"os"
	"slices"
	"strings"
	"time"

	"uppies/internal/store"
)

// offeredZones are the timezones a visitor can pick on a status page, besides
// the page's own one. One per broad region keeps the list short.
var offeredZones = []string{
	"UTC", "America/Los_Angeles", "America/New_York", "America/Sao_Paulo", "Europe/London", "Europe/Berlin",
	"Africa/Lagos", "Asia/Dubai", "Asia/Kolkata", "Asia/Singapore", "Asia/Manila", "Asia/Tokyo", "Australia/Sydney",
}

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
