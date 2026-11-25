package web

import (
	"context"
	_ "embed" // zones.txt is compiled in
	"net/http"
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

// browserZoneCookie names the cookie in which static/tz.js keeps the
// visitor's browser timezone, so public pages can show times in it.
const browserZoneCookie = "tz"

// zoneChoice is the timezone a page shows its times in.
type zoneChoice struct {
	Zone *time.Location
	Name string
	// Asked is true when the visitor picked the zone in the address. Links
	// then carry it, as a browser's own zone needs no carrying.
	Asked bool
}

// loadZone returns the named zone when it is a real one.
func loadZone(name string) (*time.Location, bool) {
	if name == "" || len(name) > 64 {
		return nil, false
	}
	zone, err := time.LoadLocation(name)
	return zone, err == nil
}

// instanceZone is the install's default timezone: the one the instance
// settings name, or UTC, which the admin pages used before the setting.
func instanceZone(settings store.InstanceSettings) zoneChoice {
	if zone, ok := loadZone(settings.Timezone); ok {
		return zoneChoice{Zone: zone, Name: settings.Timezone}
	}
	return zoneChoice{Zone: time.UTC, Name: "UTC"}
}

// visitorZone picks the timezone a public page shows its times in: the one
// the visitor picked in the address, then their browser's, then the
// install's default.
func visitorZone(r *http.Request, settings store.InstanceSettings) zoneChoice {
	if name := r.URL.Query().Get("tz"); name != "" {
		if zone, ok := loadZone(name); ok {
			return zoneChoice{Zone: zone, Name: name, Asked: true}
		}
	}
	if cookie, err := r.Cookie(browserZoneCookie); err == nil {
		if zone, ok := loadZone(cookie.Value); ok {
			return zoneChoice{Zone: zone, Name: cookie.Value}
		}
	}
	return instanceZone(settings)
}

// adminZone is the zone the admin pages show and read times in: the
// install's default. A failed read falls back to the server's zone.
func (s *Server) adminZone(ctx context.Context) zoneChoice {
	instance, err := s.store.Instance(ctx)
	if err != nil {
		s.log.Error("read instance settings", "error", err)
	}
	return instanceZone(instance)
}

// zoneChoices lists the zones offered in a picker: the current one, then
// the install's default, then every other zone.
func zoneChoices(current, fallback string) []string {
	choices := make([]string, 0, len(offeredZones)+2)
	choices = append(choices, current)
	if fallback != current {
		choices = append(choices, fallback)
	}
	for _, name := range offeredZones {
		if name != current && name != fallback {
			choices = append(choices, name)
		}
	}
	return choices
}
