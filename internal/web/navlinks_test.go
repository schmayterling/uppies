package web

import "testing"

func TestParseNavLinks(t *testing.T) {
	links, problem := parseNavLinks("docs | https://docs.example.com\n\n  support|https://example.com/help  \n")
	want := []navLink{{"docs", "https://docs.example.com"}, {"support", "https://example.com/help"}}
	if problem != "" || len(links) != 2 || links[0] != want[0] || links[1] != want[1] {
		t.Fatalf("got %+v problem %q, want %+v", links, problem, want)
	}
	rejected := map[string]string{
		"a line without a separator": "just some words",
		"a script address":           "click | javascript:alert(1)",
		"a missing label":            " | https://example.com",
		"seven links":                "a|https://e.com\nb|https://e.com\nc|https://e.com\nd|https://e.com\ne|https://e.com\nf|https://e.com\ng|https://e.com",
	}
	for name, text := range rejected {
		if links, problem := parseNavLinks(text); problem == "" {
			t.Errorf("%s was accepted: %+v", name, links)
		}
	}
}
