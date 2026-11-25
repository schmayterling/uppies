package web

import (
	"html"
	"regexp"
	"strings"
	"testing"
)

func TestUpdateMarkdown(t *testing.T) {
	cases := []struct {
		name, in, want string
	}{
		{"stars make emphasis", "**bold** and *italic*", "<p><strong>bold</strong> and <em>italic</em></p>\n"},
		{"two underscores underline", "__underlined__ and _italic_", "<p><u>underlined</u> and <em>italic</em></p>\n"},
		{"underscores inside a word stay", "snake_case and a__b__c", "<p>snake_case and a__b__c</p>\n"},
		{"strikethrough and code", "~~old~~ `new`", "<p><del>old</del> <code>new</code></p>\n"},
		{"dash hash starts small print", "fixed.\n-# times are utc", "<p>fixed.</p>\n<p class=\"md-subtext\">times are utc</p>\n"},
		{"dash hash needs a space and text", "-#tag\n-# ", "<p>-#tag<br>\n-#</p>\n"},
		{"a line break stays a line break", "one\ntwo", "<p>one<br>\ntwo</p>\n"},
		{"headings sit under the incident's h4", "# what happened\n### next", "<h5>what happened</h5>\n<h6>next</h6>\n"},
		{"html shows as typed", "<script>alert(1)</script> <primary>", "<p>&lt;script&gt;alert(1)&lt;/script&gt; &lt;primary&gt;</p>\n"},
		{"script links lose their address", "[x](javascript:alert(1))", "<p><a href=\"\">x</a></p>\n"},
		{"an image becomes a link", "![graph](https://example.com/g.png)", "<p><a href=\"https://example.com/g.png\">graph</a></p>\n"},
		{"bare addresses link", "see https://example.com", "<p>see <a href=\"https://example.com\">https://example.com</a></p>\n"},
		{"dashes under text are a rule", "text\n---", "<p>text</p>\n<hr>\n"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := renderMarkdown(c.in)
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != c.want {
				t.Errorf("got %q, want %q", got, c.want)
			}
		})
	}
}

// Every word of an update reaches the page, whatever markup surrounds it.
// Removing a block parser once dropped everything after an indented line.
func TestUpdateMarkdownKeepsEveryWord(t *testing.T) {
	in := "intro\n\n    indented alpha\n\n\ttabbed bravo\n\n<div>\ncharlie\n</div>\n\ndelta\n===\n> echo\n\n```\nfoxtrot\n```\n- golf\n\n-# hotel"
	out, err := renderMarkdown(in)
	if err != nil {
		t.Fatal(err)
	}
	shown := html.UnescapeString(regexp.MustCompile(`<[^>]*>`).ReplaceAllString(string(out), " "))
	for _, word := range []string{"intro", "alpha", "bravo", "charlie", "delta", "echo", "foxtrot", "golf", "hotel"} {
		if !strings.Contains(shown, word) {
			t.Errorf("the rendered update lost %q:\n%s", word, out)
		}
	}
}

func TestLongUpdate(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want bool
	}{
		{"nine short lines", strings.Repeat("line\n", 9), false},
		{"ten short lines", strings.Repeat("line\n", 10), true},
		{"a paragraph of nine rows", strings.Repeat("x", 899), false},
		{"a paragraph of ten rows", strings.Repeat("x", 900), true},
		{"wide characters count once", strings.Repeat("é", 899), false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := longUpdate(c.in); got != c.want {
				t.Errorf("got %v, want %v", got, c.want)
			}
		})
	}
}
