package ui

// Release notes on the update page. Gio has no Markdown widget, and the
// notes are the CHANGELOG's own Markdown, so they are read into a few
// kinds of blocks (headings, bullets, paragraphs, code) with the inline
// marks (**bold**, `code`, [links](…)) dropped, and drawn as labels.

import (
	"image"
	"regexp"
	"strings"

	"gioui.org/font"
	"gioui.org/layout"
	"gioui.org/unit"
	"gioui.org/widget/material"

	"github.com/Protomothis/smartthings-pc-control/internal/release"
)

type noteKind int

const (
	noteText    noteKind = iota // a paragraph
	noteHeading                 // any # level
	noteBullet                  // "- x", "* x", "1. x"
	noteCode                    // a line of a fenced or indented code block
	noteGap                     // a blank line between blocks
)

// noteBlock is one block of a release's notes.
type noteBlock struct {
	kind   noteKind
	text   string
	marker string // a bullet's "•" or "1."
	level  int    // a bullet's nesting, 0…2
}

var (
	reHeading = regexp.MustCompile(`^(#{1,6})\s+(.*?)\s*#*\s*$`)
	reBullet  = regexp.MustCompile(`^([-*+])\s+(.*)$`)
	reOrdered = regexp.MustCompile(`^(\d{1,3}[.)])\s+(.*)$`)
	reRule    = regexp.MustCompile(`^(\*\s*){3,}$|^(-\s*){3,}$|^(_\s*){3,}$`)
	reImage   = regexp.MustCompile(`!\[([^\]]*)\]\([^)]*\)`)
	reLink    = regexp.MustCompile(`\[([^\]]+)\]\([^)]*\)`)
	reRefLink = regexp.MustCompile(`\[([^\]]+)\]\[[^\]]*\]`)
	reTag     = regexp.MustCompile(`(?i)<br\s*/?>|</?(b|i|em|strong|code|sub|sup|kbd|details|summary)>`)
)

// noteBlocks reads Markdown into blocks.
func noteBlocks(md string) []noteBlock {
	md = strings.ReplaceAll(md, "\r\n", "\n")
	var out []noteBlock
	fenced := false
	gap := func() {
		if len(out) > 0 && out[len(out)-1].kind != noteGap {
			out = append(out, noteBlock{kind: noteGap})
		}
	}
	for _, raw := range strings.Split(md, "\n") {
		raw = strings.TrimRight(raw, " \t")
		trimmed := strings.TrimLeft(raw, " \t")
		if strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~") {
			fenced = !fenced
			continue
		}
		if fenced {
			out = append(out, noteBlock{kind: noteCode, text: raw})
			continue
		}
		if trimmed == "" {
			gap()
			continue
		}
		indent := indentWidth(raw)
		var prev *noteBlock
		if len(out) > 0 {
			prev = &out[len(out)-1]
		}
		switch {
		case reRule.MatchString(trimmed):
			gap()
		case reHeading.MatchString(trimmed) && indent < 4:
			m := reHeading.FindStringSubmatch(trimmed)
			if t := inlinePlain(m[2]); t != "" {
				gap()
				out = append(out, noteBlock{kind: noteHeading, text: t})
			}
		case reBullet.MatchString(trimmed):
			m := reBullet.FindStringSubmatch(trimmed)
			out = append(out, noteBlock{kind: noteBullet, marker: "•", text: inlinePlain(m[2]), level: min(indent/2, 2)})
		case reOrdered.MatchString(trimmed):
			m := reOrdered.FindStringSubmatch(trimmed)
			out = append(out, noteBlock{kind: noteBullet, marker: strings.TrimRight(m[1], ".)") + ".", text: inlinePlain(m[2]), level: min(indent/2, 2)})
		case indent >= 4 && (prev == nil || prev.kind == noteGap || prev.kind == noteCode):
			out = append(out, noteBlock{kind: noteCode, text: raw[min(4, len(raw)):]})
		case prev != nil && (prev.kind == noteText || prev.kind == noteBullet):
			// a soft line break: the paragraph or list item goes on
			prev.text += " " + inlinePlain(trimmed)
		default:
			text := strings.TrimLeft(trimmed, "> ") // a quote reads as plain text
			out = append(out, noteBlock{kind: noteText, text: inlinePlain(text)})
		}
	}
	for len(out) > 0 && out[len(out)-1].kind == noteGap {
		out = out[:len(out)-1]
	}
	return out
}

// indentWidth counts leading spaces, a tab as four.
func indentWidth(s string) int {
	n := 0
	for _, r := range s {
		switch r {
		case ' ':
			n++
		case '\t':
			n += 4
		default:
			return n
		}
	}
	return n
}

// inlinePlain drops the inline Markdown marks: images and links keep their
// text, emphasis and code marks go, simple HTML tags too.
func inlinePlain(s string) string {
	s = reImage.ReplaceAllString(s, "$1")
	s = reLink.ReplaceAllString(s, "$1")
	s = reRefLink.ReplaceAllString(s, "$1")
	s = reTag.ReplaceAllString(s, " ")
	s = strings.NewReplacer("**", "", "__", "", "`", "", "~~", "", `\*`, "*", `\_`, "_").Replace(s)
	return strings.Join(strings.Fields(s), " ")
}

// noteRelease is one release's notes, ready to draw.
type noteRelease struct {
	heading string // "v1.2.0 · 2026-10-03"
	blocks  []noteBlock
}

// noteReleases reads up to max releases' notes.
func noteReleases(rels []release.Info, max int) []noteRelease {
	out := make([]noteRelease, 0, min(len(rels), max))
	for i := range rels {
		if i == max {
			break
		}
		r := &rels[i]
		h := r.TagName
		if !r.PublishedAt.IsZero() {
			h += " · " + r.PublishedAt.Local().Format("2006-01-02")
		}
		out = append(out, noteRelease{heading: h, blocks: noteBlocks(r.Notes())})
	}
	return out
}

// noteMono is the face of code lines.
const noteMono = "Go Mono"

// layoutNotes draws one release's blocks; empty is the line for a release
// without notes.
func layoutNotes(gtx C, th *material.Theme, r noteRelease, empty string) D {
	children := []layout.FlexChild{
		layout.Rigid(label(th, textL, r.heading, colText, font.Bold).Layout),
		vspace(gapXS),
	}
	if len(r.blocks) == 0 {
		children = append(children, layout.Rigid(label(th, textM, empty, colSub, font.Normal).Layout))
	}
	for i, b := range r.blocks {
		if i > 0 && b.kind != noteGap && r.blocks[i-1].kind != noteGap {
			children = append(children, vspace(3))
		}
		children = append(children, layout.Rigid(noteBlockWidget(th, b)))
	}
	return column(gtx, children...)
}

func noteBlockWidget(th *material.Theme, b noteBlock) layout.Widget {
	switch b.kind {
	case noteGap:
		return func(gtx C) D { return D{Size: image.Pt(0, gtx.Dp(8))} }
	case noteHeading:
		return label(th, textM, b.text, colText, font.SemiBold).Layout
	case noteCode:
		l := label(th, textS, b.text, colSub, font.Normal)
		l.Font.Typeface = noteMono
		return l.Layout
	case noteBullet:
		return func(gtx C) D {
			return layout.Inset{Left: unit.Dp(4 + 14*b.level)}.Layout(gtx, func(gtx C) D {
				return layout.Flex{Alignment: layout.Start}.Layout(gtx,
					layout.Rigid(func(gtx C) D {
						gtx.Constraints.Min.X = gtx.Dp(14)
						return label(th, textM, b.marker, colSub, font.Normal).Layout(gtx)
					}),
					hspace(4),
					layout.Flexed(1, label(th, textM, b.text, colText, font.Normal).Layout),
				)
			})
		}
	}
	return label(th, textM, b.text, colText, font.Normal).Layout
}
