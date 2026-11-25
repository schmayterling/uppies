package web

import (
	"bytes"
	"fmt"
	"html/template"
	"strings"
	"unicode/utf8"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/parser"
	"github.com/yuin/goldmark/renderer"
	"github.com/yuin/goldmark/renderer/html"
	"github.com/yuin/goldmark/text"
	"github.com/yuin/goldmark/util"
)

// updateMarkdown renders the text of incident and maintenance updates. It is
// CommonMark with strikethrough and bare links, plus two additions from chat
// apps: __text__ underlines and a line starting with "-# " is small print.
//
// Two CommonMark rules are left out because they surprise people who write a
// status update rather than a document: raw HTML is shown as typed, and a line
// of dashes under text is a rule, not a heading.
var updateMarkdown = goldmark.New(
	goldmark.WithParser(parser.NewParser(
		parser.WithBlockParsers(
			util.Prioritized(subtextParser{}, 100),
			util.Prioritized(parser.NewThematicBreakParser(), 200),
			util.Prioritized(parser.NewListParser(), 300),
			util.Prioritized(parser.NewListItemParser(), 400),
			util.Prioritized(parser.NewCodeBlockParser(), 500),
			util.Prioritized(parser.NewATXHeadingParser(), 600),
			util.Prioritized(parser.NewFencedCodeBlockParser(), 700),
			util.Prioritized(parser.NewBlockquoteParser(), 800),
			util.Prioritized(parser.NewParagraphParser(), 1000),
		),
		parser.WithInlineParsers(
			util.Prioritized(parser.NewCodeSpanParser(), 100),
			util.Prioritized(parser.NewLinkParser(), 200),
			util.Prioritized(parser.NewAutoLinkParser(), 300),
			util.Prioritized(emphasisParser{}, 500),
		),
		parser.WithParagraphTransformers(parser.DefaultParagraphTransformers()...),
	)),
	goldmark.WithExtensions(extension.Strikethrough, extension.Linkify),
	// A line break in the text is a line break on the page, as it was before
	// updates were Markdown.
	goldmark.WithRendererOptions(
		html.WithHardWraps(),
		renderer.WithNodeRenderers(util.Prioritized(updateRenderer{}, 500)),
	),
)

// renderMarkdown returns an update's text as HTML. The renderer escapes
// everything it does not turn into markup, and drops script and data links.
func renderMarkdown(body string) (template.HTML, error) {
	var out bytes.Buffer
	if err := updateMarkdown.Convert([]byte(body), &out); err != nil {
		return "", fmt.Errorf("render update text: %w", err)
	}
	return template.HTML(out.String()), nil
}

const (
	// widestLineRunes is about how many characters fit on one line in the
	// widest column that shows updates.
	widestLineRunes = 100
	// collapseAfterRows is the estimated height at which an update collapses.
	// A collapsed update shows six lines (11-incidents.css). The threshold sits
	// well above that because Markdown syntax makes the source longer than the
	// text it shows.
	collapseAfterRows = 9
)

// longUpdate reports whether an update gets a read more toggle. The page has
// no script to measure the text, so this estimates its height from the source:
// every line takes at least one row, and a long line wraps every
// widestLineRunes characters. An update over the threshold is taller than the
// collapsed height at every width, so the toggle never opens onto nothing.
func longUpdate(body string) bool {
	rows := 0
	for line := range strings.Lines(body) {
		rows += 1 + utf8.RuneCountInString(line)/widestLineRunes
	}
	return rows > collapseAfterRows
}

var (
	kindUnderline = ast.NewNodeKind("Underline")
	kindSubtext   = ast.NewNodeKind("Subtext")
)

type underline struct{ ast.BaseInline }

func (n *underline) Kind() ast.NodeKind { return kindUnderline }

func (n *underline) Dump(source []byte, level int) { ast.DumpHelper(n, source, level, nil, nil) }

// subtext is a line of small print, written "-# text".
type subtext struct{ ast.BaseBlock }

func (n *subtext) Kind() ast.NodeKind { return kindSubtext }

func (n *subtext) Dump(source []byte, level int) { ast.DumpHelper(n, source, level, nil, nil) }

type subtextParser struct{}

func (subtextParser) Trigger() []byte { return []byte{'-'} }

func (subtextParser) Open(parent ast.Node, reader text.Reader, pc parser.Context) (ast.Node, parser.State) {
	line, segment := reader.PeekLine()
	start := pc.BlockOffset()
	if start < 0 || !bytes.HasPrefix(line[start:], []byte("-# ")) {
		return nil, parser.NoChildren
	}
	start += 3 + util.TrimLeftSpaceLength(line[start+3:])
	content := text.NewSegment(segment.Start+start-segment.Padding, segment.Start+len(line)-segment.Padding)
	content = content.TrimRightSpace(reader.Source())
	if content.Len() == 0 {
		return nil, parser.NoChildren
	}
	node := &subtext{}
	node.Lines().Append(content)
	reader.AdvanceToEOL()
	return node, parser.NoChildren
}

func (subtextParser) Continue(node ast.Node, reader text.Reader, pc parser.Context) parser.State {
	return parser.Close
}

func (subtextParser) Close(node ast.Node, reader text.Reader, pc parser.Context) {}

func (subtextParser) CanInterruptParagraph() bool { return true }

func (subtextParser) CanAcceptIndentedLine() bool { return false }

// emphasisParser is goldmark's emphasis parser with one change: a pair of
// underscores underlines instead of making text strong. Stars work as usual.
type emphasisParser struct{}

type emphasisDelimiters struct{ char byte }

var (
	starDelimiters       = &emphasisDelimiters{'*'}
	underscoreDelimiters = &emphasisDelimiters{'_'}
)

func (p *emphasisDelimiters) IsDelimiter(b byte) bool { return b == p.char }

func (p *emphasisDelimiters) CanOpenCloser(opener, closer *parser.Delimiter) bool {
	return opener.Char == closer.Char
}

func (p *emphasisDelimiters) OnMatch(consumes int) ast.Node {
	if p.char == '_' && consumes == 2 {
		return &underline{}
	}
	return ast.NewEmphasis(consumes)
}

func (emphasisParser) Trigger() []byte { return []byte{'*', '_'} }

func (emphasisParser) Parse(parent ast.Node, block text.Reader, pc parser.Context) ast.Node {
	before := block.PrecendingCharacter()
	line, segment := block.PeekLine()
	delimiters := starDelimiters
	if line[0] == '_' {
		delimiters = underscoreDelimiters
	}
	node := parser.ScanDelimiter(line, before, 1, delimiters)
	if node == nil {
		return nil
	}
	node.Segment = segment.WithStop(segment.Start + node.OriginalLength)
	block.Advance(node.OriginalLength)
	pc.PushDelimiter(node)
	return node
}

// updateRenderer draws the added nodes and changes two of goldmark's. Its
// writes ignore errors as goldmark's own renderers do: the buffered writer
// keeps the first error and Convert returns it when it flushes.
type updateRenderer struct{}

func (updateRenderer) RegisterFuncs(r renderer.NodeRendererFuncRegisterer) {
	r.Register(kindUnderline, renderUnderline)
	r.Register(kindSubtext, renderSubtext)
	r.Register(ast.KindHeading, renderHeading)
	r.Register(ast.KindImage, renderImageLink)
}

func renderUnderline(w util.BufWriter, source []byte, node ast.Node, entering bool) (ast.WalkStatus, error) {
	if entering {
		_, _ = w.WriteString("<u>")
	} else {
		_, _ = w.WriteString("</u>")
	}
	return ast.WalkContinue, nil
}

func renderSubtext(w util.BufWriter, source []byte, node ast.Node, entering bool) (ast.WalkStatus, error) {
	if entering {
		_, _ = w.WriteString(`<p class="md-subtext">`)
	} else {
		_, _ = w.WriteString("</p>\n")
	}
	return ast.WalkContinue, nil
}

// renderHeading moves headings under the update they sit in: the page's own
// headings run down to h4 for an incident, so "#" becomes h5 and the rest h6.
func renderHeading(w util.BufWriter, source []byte, node ast.Node, entering bool) (ast.WalkStatus, error) {
	level := min(node.(*ast.Heading).Level+4, 6)
	if entering {
		fmt.Fprintf(w, "<h%d>", level)
	} else {
		fmt.Fprintf(w, "</h%d>\n", level)
	}
	return ast.WalkContinue, nil
}

// renderImageLink shows an image as a link to it, named by its alt text. The
// page's security policy blocks images from other sites, so an embedded one
// would show as broken.
func renderImageLink(w util.BufWriter, source []byte, node ast.Node, entering bool) (ast.WalkStatus, error) {
	if !entering {
		_, _ = w.WriteString("</a>")
		return ast.WalkContinue, nil
	}
	destination := util.URLEscape(node.(*ast.Image).Destination, true)
	_, _ = w.WriteString(`<a href="`)
	if !html.IsDangerousURL(destination) {
		_, _ = w.Write(util.EscapeHTML(destination))
	}
	_, _ = w.WriteString(`">`)
	return ast.WalkContinue, nil
}
