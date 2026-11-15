package ui

// Components. Each takes its state (a widget.Clickable, widget.Bool,
// widget.Editor …) from the caller and only draws; the screens decide what
// a click does. Read a click before laying the widget out
// (`if clk.Clicked(gtx) {…}` at the top of body), as Gio wants.
//
// How a screen (ui/pages_<id>.go) uses them:
//
//	Page frame        pages.go: heroCard (status at the top of a connection
//	                  page), section (small grey header over a card),
//	                  sections (stacks them), infoRow (read-only fact).
//	Rows              toggleRow / toggleRowIcon (setting + switch),
//	                  settingRow (title left, any control right),
//	                  fieldRow (title left, narrow field right: a port),
//	                  listRow (tappable row with a chevron → sub-page),
//	                  connRow (icon tile, title, summary, status pill,
//	                  chevron: the home 연결 card's rows),
//	                  actionRow (icon tile, title, grey line, chevron: does
//	                  something rather than open a page), pickRow (a name
//	                  with an add mark: pick-from lists).
//	Inputs            field (one line), textArea (several lines),
//	                  secretField (masked, with an eye), searchField
//	                  (magnifier and clear button), chipGroup (≤6 choices;
//	                  wraps), checkChip (one of a multiple choice),
//	                  choiceList (one of several rows that each need a
//	                  name, a line and a pill), numberGrid (1…n in equal
//	                  cells: preset slots), timeStepper (a value between
//	                  ‹ and ›), fieldButton (a button as tall as a field),
//	                  toggle (the switch itself). setText refills an editor
//	                  without moving the caret.
//	Lists             listEditor: rows the user adds and deletes (hubs,
//	                  chat IDs, watched programs). Call Deleted/Added
//	                  before Layout each frame.
//	Buttons           primaryButton (the one strong action), secondaryButton,
//	                  greyButton, dangerButton, smallButton (quiet pill),
//	                  iconButton (round), copyButton (copies, then says so),
//	                  liveButton (live/paused pill), floatingPill (accent
//	                  pill laid over content).
//	State             tone (toneOK/Off/Warn/Bad/Accent) colours statusPill,
//	                  iconTile and heroCard alike; banner shows a coloured
//	                  line (u.setNotice puts one at the top of a page: save
//	                  errors, warnings); statusDot, usageBar, pill.
//	Layout            natural (own width inside a column), dim (fades a
//	                  row that only matters while another setting is on),
//	                  wrap, grid, fill.
//	Questions         u.ask(askSpec{…}) opens a sheet with confirm/cancel
//	                  (quit.go); u.toast.show / showAction for a passing
//	                  message, optionally with one action.
//	Shell helpers     shellopen_windows.go: openURL, openPath, revealPath,
//	                  u.pickFile (native Open dialog, off the window
//	                  goroutine; hand the result back with u.onWindow).
//
// Colours, gaps, radii and text sizes come from style.go only; icons by
// name from iconData below; a screen adds its own from its file with
// func init() { iconData["name"] = icons.X }.

import (
	"image"
	"image/color"
	"io"
	"strconv"
	"strings"
	"time"
	"unicode"

	"gioui.org/font"
	"gioui.org/io/clipboard"
	"gioui.org/io/pointer"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/text"
	"gioui.org/unit"
	"gioui.org/widget"
	"gioui.org/widget/material"
	"golang.org/x/exp/shiny/materialdesign/icons"
)

// --- icons ---

// iconData names the Material icons the screens use; icon() parses each
// once.
var iconData = map[string][]byte{
	"screenoff": icons.HardwareDesktopWindows,
	"sleep":     icons.ImageBrightness3,
	"restart":   icons.ActionAutorenew,
	"shutdown":  icons.ActionPowerSettingsNew,
	"awake":     icons.ImageWBSunny,
	"lock":      icons.ActionLock,
	"more":      icons.NavigationMoreHoriz,
	"chevron":   icons.NavigationChevronRight,
	"back":      icons.NavigationArrowBack,
	"expand":    icons.NavigationExpandMore,
	"collapse":  icons.NavigationExpandLess,
	"prev":      icons.AVSkipPrevious,
	"next":      icons.AVSkipNext,
	"play":      icons.AVPlayArrow,
	"pause":     icons.AVPause,
	"volume":    icons.AVVolumeUp,
	"muted":     icons.AVVolumeOff,
	"add":       icons.ContentAdd,
	"eye":       icons.ActionVisibility,
	"eyeoff":    icons.ActionVisibilityOff,
	"timer":     icons.ImageTimer,
	"hub":       icons.HardwareRouter,
	"telegram":  icons.ContentSend,
	"share":     icons.SocialShare,
	"settings":  icons.ActionSettings,
	"service":   icons.ActionDNS,
	"warn":      icons.AlertWarning,
	"error":     icons.AlertError,
	"ok":        icons.ActionCheckCircle,
	"info":      icons.ActionInfoOutline,
	"copy":      icons.ContentContentCopy,
	"delete":    icons.ActionDelete,
	"folder":    icons.FileFolderOpen,
	"bell":      icons.SocialNotifications,
	"bug":       icons.ActionBugReport,
	"refresh":   icons.NavigationRefresh,
	"open":      icons.ActionOpenInNew,
	"key":       icons.CommunicationVPNKey,
	"update":    icons.NotificationSystemUpdate,
	"logs":      icons.ActionDescription,
	"remote":    icons.ActionSettingsRemote,
	"music":     icons.AVQueueMusic,
	"check":     icons.NavigationCheck,
	"close":     icons.NavigationClose,
	"chevleft":  icons.NavigationChevronLeft,
	"search":    icons.ActionSearch,
	"todown":    icons.NavigationArrowDownward,
}

var iconCache = map[string]*widget.Icon{}

func icon(name string) *widget.Icon {
	if ic, ok := iconCache[name]; ok {
		return ic
	}
	ic, err := widget.NewIcon(iconData[name])
	if err != nil {
		panic("ui: icon " + name + ": " + err.Error())
	}
	iconCache[name] = ic
	return ic
}

// drawIcon paints the named icon size×size in col.
func drawIcon(gtx C, name string, size unit.Dp, col color.NRGBA) D {
	s := gtx.Dp(size)
	gtx.Constraints = layout.Exact(image.Pt(s, s))
	return icon(name).Layout(gtx, col)
}

// --- buttons ---

// primaryButton is the one strong action of a card or page.
func primaryButton(th *material.Theme, clk *widget.Clickable, txt string) material.ButtonStyle {
	b := material.Button(th, clk, txt)
	b.Background = colAccent
	b.Color = colOnAccent
	b.CornerRadius = radiusM
	b.Inset = layout.Inset{Top: 13, Bottom: 13, Left: 16, Right: 16}
	b.Font.Weight = font.SemiBold
	b.TextSize = textL
	return b
}

// secondaryButton is primaryButton's quiet sibling: accent text on the
// accent tint.
func secondaryButton(th *material.Theme, clk *widget.Clickable, txt string) material.ButtonStyle {
	b := primaryButton(th, clk, txt)
	b.Background = colAccentDim
	b.Color = colAccent
	return b
}

// dangerButton confirms something that cannot be taken back.
func dangerButton(th *material.Theme, clk *widget.Clickable, txt string) material.ButtonStyle {
	b := primaryButton(th, clk, txt)
	b.Background = colDanger
	return b
}

// greyButton is a full-size neutral button (취소 next to a confirm).
func greyButton(th *material.Theme, clk *widget.Clickable, txt string) material.ButtonStyle {
	b := primaryButton(th, clk, txt)
	b.Background = colGhost
	b.Color = colText
	return b
}

// smallButton is a quiet grey pill for secondary actions.
func smallButton(th *material.Theme, clk *widget.Clickable, txt string, fg color.NRGBA) material.ButtonStyle {
	b := material.Button(th, clk, txt)
	b.Background = colGhost
	b.Color = fg
	b.CornerRadius = radiusS
	b.Inset = layout.Inset{Top: 7, Bottom: 7, Left: 12, Right: 12}
	b.TextSize = textM
	b.Font.Weight = font.Medium
	return b
}

// fill stretches w to the full width (a button under a form).
func fill(w layout.Widget) layout.Widget {
	return func(gtx C) D {
		gtx.Constraints.Min.X = gtx.Constraints.Max.X
		return w(gtx)
	}
}

// --- chips ---

// chip is one choice of a chip group: grey, or accent while selected.
func chip(th *material.Theme, clk *widget.Clickable, txt string, selected bool) layout.Widget {
	return func(gtx C) D {
		b := smallButton(th, clk, txt, colText)
		if selected {
			b.Background, b.Color = colAccent, colOnAccent
			b.Font.Weight = font.SemiBold
		}
		return b.Layout(gtx)
	}
}

// chipGroup is the state of a row of chips; Clicked reports a tap.
type chipGroup struct {
	clks []widget.Clickable
}

// Clicked returns the index of a chip tapped since the last frame, or -1.
func (g *chipGroup) Clicked(gtx C, n int) int {
	g.grow(n)
	hit := -1
	for i := range n {
		if g.clks[i].Clicked(gtx) {
			hit = i
		}
	}
	return hit
}

func (g *chipGroup) grow(n int) {
	if len(g.clks) < n {
		g.clks = append(g.clks, make([]widget.Clickable, n-len(g.clks))...)
	}
}

// Layout draws the chips, wrapping onto more lines when the row is full.
func (g *chipGroup) Layout(gtx C, th *material.Theme, labels []string, selected int) D {
	g.grow(len(labels))
	ws := make([]layout.Widget, len(labels))
	for i, l := range labels {
		ws[i] = chip(th, &g.clks[i], l, i == selected)
	}
	return wrap(gtx, gapXS, gapXS, ws...)
}

// wrap places children left to right and starts a new line when the next
// one does not fit, like CSS flex-wrap.
func wrap(gtx C, hgap, vgap unit.Dp, children ...layout.Widget) D {
	maxW := gtx.Constraints.Max.X
	hg, vg := gtx.Dp(hgap), gtx.Dp(vgap)
	cgtx := gtx
	cgtx.Constraints.Min = image.Point{}
	x, y, lineH, width := 0, 0, 0, 0
	for _, w := range children {
		m := op.Record(gtx.Ops)
		d := w(cgtx)
		call := m.Stop()
		if x > 0 && x+d.Size.X > maxW {
			x, y = 0, y+lineH+vg
			lineH = 0
		}
		t := op.Offset(image.Pt(x, y)).Push(gtx.Ops)
		call.Add(gtx.Ops)
		t.Pop()
		x += d.Size.X + hg
		width = max(width, x-hg)
		lineH = max(lineH, d.Size.Y)
	}
	return D{Size: image.Pt(width, y+lineH)}
}

// --- fields ---

// field is a bordered one-line text box.
func field(th *material.Theme, ed *widget.Editor, hint string) layout.Widget {
	return func(gtx C) D {
		ed.SingleLine = true
		return fieldFrame(gtx, func(gtx C) D {
			e := material.Editor(th, ed, hint)
			e.HintColor = colFaint
			e.TextSize = textL
			return e.Layout(gtx)
		})
	}
}

func fieldFrame(gtx C, w layout.Widget) D {
	gtx.Constraints.Min.X = gtx.Constraints.Max.X
	border := widget.Border{Color: colLine, CornerRadius: radiusM, Width: unit.Dp(1)}
	return border.Layout(gtx, func(gtx C) D {
		return layout.Inset{Top: 12, Bottom: 12, Left: 14, Right: 14}.Layout(gtx, w)
	})
}

// secretField is a password box with an eye button that shows the text.
type secretField struct {
	ed     widget.Editor
	reveal widget.Clickable
	shown  bool
}

func (s *secretField) Layout(gtx C, th *material.Theme, hint string) D {
	if s.reveal.Clicked(gtx) {
		s.shown = !s.shown
	}
	s.ed.SingleLine = true
	if s.shown {
		s.ed.Mask = 0
	} else {
		s.ed.Mask = '•'
	}
	return fieldFrame(gtx, func(gtx C) D {
		return row(gtx,
			layout.Flexed(1, func(gtx C) D {
				e := material.Editor(th, &s.ed, hint)
				e.HintColor = colFaint
				e.TextSize = textL
				return e.Layout(gtx)
			}),
			hspace(8),
			layout.Rigid(func(gtx C) D {
				name := "eye"
				if s.shown {
					name = "eyeoff"
				}
				return s.reveal.Layout(gtx, func(gtx C) D {
					pointer.CursorPointer.Add(gtx.Ops)
					return drawIcon(gtx, name, 20, colSub)
				})
			}),
		)
	})
}

// fieldLabel is the small caption above a field.
func fieldLabel(th *material.Theme, txt string) layout.FlexChild {
	return layout.Rigid(func(gtx C) D {
		return layout.Inset{Bottom: 6}.Layout(gtx, label(th, textM, txt, colSub, font.Medium).Layout)
	})
}

// --- toggles and rows ---

// switchStyle is a Toss-like switch: a pill track, accent while on, with
// the white thumb inside it.
type switchStyle struct {
	b *widget.Bool
}

func toggle(_ *material.Theme, b *widget.Bool, _ string) switchStyle {
	return switchStyle{b: b}
}

func (s switchStyle) Layout(gtx C) D {
	return s.b.Layout(gtx, func(gtx C) D {
		pointer.CursorPointer.Add(gtx.Ops)
		w, h := gtx.Dp(44), gtx.Dp(26)
		track := colFaint
		if s.b.Value {
			track = colAccent
		}
		if s.b.Hovered() {
			track = mix(track, colText, 0.08)
		}
		paint.FillShape(gtx.Ops, track, clip.UniformRRect(image.Rect(0, 0, w, h), h/2).Op(gtx.Ops))
		pad := gtx.Dp(3)
		d := h - 2*pad
		x := pad
		if s.b.Value {
			x = w - pad - d
		}
		paint.FillShape(gtx.Ops, rgb(0xffffff), clip.Ellipse(image.Rect(x, pad, x+d, pad+d)).Op(gtx.Ops))
		return D{Size: image.Pt(w, h)}
	})
}

// titled is a title with an optional grey line under it.
func titled(th *material.Theme, title, sub string) layout.Widget {
	return func(gtx C) D {
		if sub == "" {
			return label(th, textL, title, colText, font.Medium).Layout(gtx)
		}
		return column(gtx,
			layout.Rigid(label(th, textL, title, colText, font.Medium).Layout),
			vspace(3),
			layout.Rigid(label(th, textS, sub, colSub, font.Normal).Layout),
		)
	}
}

// toggleRow is a setting with a switch on the right.
func toggleRow(gtx C, th *material.Theme, b *widget.Bool, title, sub string) D {
	return row(gtx,
		layout.Flexed(1, titled(th, title, sub)),
		hspace(gapS),
		layout.Rigid(toggle(th, b, title).Layout),
	)
}

// listRow is a tappable row: title, grey line, optional value and a
// chevron. It is the way into a sub-page.
func listRow(gtx C, th *material.Theme, clk *widget.Clickable, title, sub, value string) D {
	return clk.Layout(gtx, func(gtx C) D {
		pointer.CursorPointer.Add(gtx.Ops)
		gtx.Constraints.Min.X = gtx.Constraints.Max.X
		return layout.Inset{Top: 4, Bottom: 4}.Layout(gtx, func(gtx C) D {
			return row(gtx,
				layout.Flexed(1, titled(th, title, sub)),
				layout.Rigid(func(gtx C) D {
					if value == "" {
						return D{}
					}
					return layout.Inset{Left: 8}.Layout(gtx, line(th, textM, value, colSub, font.Normal).Layout)
				}),
				hspace(4),
				layout.Rigid(func(gtx C) D { return drawIcon(gtx, "chevron", 20, colFaint) }),
			)
		})
	})
}

// listRowBadge is listRow with a widget (a statusPill) before the
// chevron instead of a grey value.
func listRowBadge(gtx C, th *material.Theme, clk *widget.Clickable, title, sub string, badge layout.Widget) D {
	return clk.Layout(gtx, func(gtx C) D {
		pointer.CursorPointer.Add(gtx.Ops)
		gtx.Constraints.Min.X = gtx.Constraints.Max.X
		return layout.Inset{Top: 4, Bottom: 4}.Layout(gtx, func(gtx C) D {
			return row(gtx,
				layout.Flexed(1, titled(th, title, sub)),
				layout.Rigid(func(gtx C) D { return layout.Inset{Left: 8}.Layout(gtx, badge) }),
				hspace(4),
				layout.Rigid(func(gtx C) D { return drawIcon(gtx, "chevron", 20, colFaint) }),
			)
		})
	})
}

// settingRow puts a title on the left and any control on the right.
func settingRow(gtx C, th *material.Theme, title, sub string, control layout.Widget) D {
	return row(gtx,
		layout.Flexed(1, titled(th, title, sub)),
		hspace(gapS),
		layout.Rigid(control),
	)
}

// expander is a section that opens in place instead of a dialog.
type expander struct {
	open bool
	head widget.Clickable
}

// Layout draws the header (title and a chevron) and, while open, body
// under it.
func (e *expander) Layout(gtx C, th *material.Theme, title string, body layout.Widget) D {
	if e.head.Clicked(gtx) {
		e.open = !e.open
	}
	return column(gtx,
		layout.Rigid(func(gtx C) D {
			return e.head.Layout(gtx, func(gtx C) D {
				pointer.CursorPointer.Add(gtx.Ops)
				gtx.Constraints.Min.X = gtx.Constraints.Max.X
				return layout.Inset{Top: 4, Bottom: 4}.Layout(gtx, func(gtx C) D {
					name := "expand"
					if e.open {
						name = "collapse"
					}
					return row(gtx,
						layout.Flexed(1, label(th, textM, title, colSub, font.Medium).Layout),
						layout.Rigid(func(gtx C) D { return drawIcon(gtx, name, 20, colFaint) }),
					)
				})
			})
		}),
		layout.Rigid(func(gtx C) D {
			if !e.open {
				return D{}
			}
			return layout.Inset{Top: gapS}.Layout(gtx, body)
		}),
	)
}

// cardTitle is a card's heading with an optional control on the right.
func cardTitle(gtx C, th *material.Theme, title string, trailing layout.Widget) D {
	return row(gtx,
		layout.Flexed(1, label(th, titleSize, title, colText, font.Bold).Layout),
		layout.Rigid(func(gtx C) D {
			if trailing == nil {
				return D{}
			}
			return trailing(gtx)
		}),
	)
}

// --- indicators ---

// statusDot is a filled circle (the connection light).
func statusDot(gtx C, col color.NRGBA, size unit.Dp) D {
	s := gtx.Dp(size)
	r := image.Rect(0, 0, s, s)
	paint.FillShape(gtx.Ops, col, clip.Ellipse(r).Op(gtx.Ops))
	return D{Size: r.Max}
}

// usageBar draws a thin rounded meter filled to frac (0…1).
func usageBar(gtx C, frac float64, fill color.NRGBA) D {
	w, h := gtx.Constraints.Max.X, gtx.Dp(6)
	r := h / 2
	paint.FillShape(gtx.Ops, colLine, clip.UniformRRect(image.Rect(0, 0, w, h), r).Op(gtx.Ops))
	if frac > 0 {
		fw := max(int(float64(w)*min(frac, 1)), h) // never thinner than its own rounding
		paint.FillShape(gtx.Ops, fill, clip.UniformRRect(image.Rect(0, 0, fw, h), r).Op(gtx.Ops))
	}
	return D{Size: image.Pt(w, h)}
}

// divider is a hairline across the width.
func divider(gtx C) D {
	w, h := gtx.Constraints.Max.X, max(gtx.Dp(1), 1)
	paint.FillShape(gtx.Ops, colLine, clip.Rect(image.Rect(0, 0, w, h)).Op())
	return D{Size: image.Pt(w, h)}
}

// pill is a small tinted badge ("배터리 80%").
func pill(th *material.Theme, txt string, fg, bg color.NRGBA) layout.Widget {
	return func(gtx C) D {
		return layout.Stack{}.Layout(gtx,
			layout.Expanded(func(gtx C) D {
				r := image.Rectangle{Max: gtx.Constraints.Min}
				paint.FillShape(gtx.Ops, bg, clip.UniformRRect(r, r.Dy()/2).Op(gtx.Ops))
				return D{Size: gtx.Constraints.Min}
			}),
			layout.Stacked(func(gtx C) D {
				return layout.Inset{Top: 4, Bottom: 4, Left: 10, Right: 10}.Layout(gtx,
					line(th, textS, txt, fg, font.Medium).Layout)
			}),
		)
	}
}

// tile is a square-ish action button: an icon in a tinted circle with the
// label under it (the 바로 실행 grid).
func tile(gtx C, th *material.Theme, clk *widget.Clickable, iconName, txt string, fg, bg color.NRGBA) D {
	return clk.Layout(gtx, func(gtx C) D {
		pointer.CursorPointer.Add(gtx.Ops)
		gtx.Constraints.Min.X = gtx.Constraints.Max.X
		return layout.Flex{Axis: layout.Vertical, Alignment: layout.Middle}.Layout(gtx,
			layout.Rigid(func(gtx C) D {
				s := gtx.Dp(48)
				return layout.Stack{Alignment: layout.Center}.Layout(gtx,
					layout.Expanded(func(gtx C) D {
						r := image.Rect(0, 0, s, s)
						bgc := bg
						if clk.Hovered() {
							bgc = mix(bg, fg, 0.12)
						}
						paint.FillShape(gtx.Ops, bgc, clip.UniformRRect(r, gtx.Dp(radiusL)).Op(gtx.Ops))
						return D{Size: r.Max}
					}),
					layout.Stacked(func(gtx C) D {
						gtx.Constraints.Min = image.Pt(s, s)
						return layout.Center.Layout(gtx, func(gtx C) D { return drawIcon(gtx, iconName, 24, fg) })
					}),
				)
			}),
			vspace(8),
			layout.Rigid(func(gtx C) D {
				l := line(th, textM, txt, colText, font.Medium)
				l.Alignment = text.Middle
				return l.Layout(gtx)
			}),
		)
	})
}

// iconButton is a round button holding one icon (the media controls).
func iconButton(gtx C, clk *widget.Clickable, name string, size unit.Dp, fg, bg color.NRGBA) D {
	return clk.Layout(gtx, func(gtx C) D {
		pointer.CursorPointer.Add(gtx.Ops)
		s := gtx.Dp(size)
		r := image.Rect(0, 0, s, s)
		bgc := bg
		if clk.Hovered() {
			bgc = mix(bg, fg, 0.12)
		}
		paint.FillShape(gtx.Ops, bgc, clip.Ellipse(r).Op(gtx.Ops))
		inner := gtx.Dp(size * 0.5)
		off := (s - inner) / 2
		t := op.Offset(image.Pt(off, off)).Push(gtx.Ops)
		gtx.Constraints = layout.Exact(image.Pt(inner, inner))
		icon(name).Layout(gtx, fg)
		t.Pop()
		return D{Size: r.Max}
	})
}

// mix blends b into a by t (0…1).
func mix(a, b color.NRGBA, t float32) color.NRGBA {
	l := func(x, y uint8) uint8 { return uint8(float32(x) + (float32(y)-float32(x))*t) }
	return color.NRGBA{R: l(a.R, b.R), G: l(a.G, b.G), B: l(a.B, b.B), A: 0xff}
}

// grid lays children out in equal columns.
func grid(gtx C, gap unit.Dp, children ...layout.Widget) D {
	fc := make([]layout.FlexChild, 0, 2*len(children))
	for i, w := range children {
		if i > 0 {
			fc = append(fc, hspace(gap))
		}
		fc = append(fc, layout.Flexed(1, w))
	}
	return layout.Flex{}.Layout(gtx, fc...)
}

// textEnd right-aligns a label.
const textEnd = text.End

func imagePt(s int) image.Point { return image.Pt(s, s) }

// iconBadgeBg is the 36dp tinted rounded square behind a row's icon.
func iconBadgeBg(gtx C, bg color.NRGBA) D {
	s := gtx.Dp(36)
	r := image.Rect(0, 0, s, s)
	paint.FillShape(gtx.Ops, bg, clip.UniformRRect(r, gtx.Dp(radiusM)).Op(gtx.Ops))
	return D{Size: r.Max}
}

// iconBadge is a row's leading icon on its tinted square.
func iconBadge(gtx C, name string, fg, bg color.NRGBA) D {
	return layout.Stack{Alignment: layout.Center}.Layout(gtx,
		layout.Stacked(func(gtx C) D { return iconBadgeBg(gtx, bg) }),
		layout.Stacked(func(gtx C) D { return drawIcon(gtx, name, 20, fg) }),
	)
}

// --- tones -------------------------------------------------------------------

// tone is the colour of a state, shared by pills, icon tiles, banners and
// the hero card, so "연결됨" is the same green everywhere.
type tone int

const (
	toneOff    tone = iota // grey: off, not set up, unknown
	toneOK                 // green: connected, on, working
	toneWarn               // amber: needs a look
	toneBad                // red: failed, unreachable
	toneAccent             // blue: neutral emphasis
)

// colors are the tone's foreground and its tinted background.
func (t tone) colors() (fg, bg color.NRGBA) {
	switch t {
	case toneOK:
		return colOK, mix(colCard, colOK, 0.14)
	case toneWarn:
		return colWarn, mix(colCard, colWarn, 0.16)
	case toneBad:
		return colDanger, colDangerDim
	case toneAccent:
		return colAccent, colAccentDim
	}
	return colSub, colGhost
}

// statusPill is a small tinted badge in a tone ("연결됨", "꺼짐").
func statusPill(th *material.Theme, txt string, t tone) layout.Widget {
	fg, bg := t.colors()
	return pill(th, txt, fg, bg)
}

// iconTile is an icon on a rounded square tinted in a tone (size dp),
// the leading mark of a connection row or a toggle.
func iconTile(gtx C, name string, t tone, size unit.Dp) D {
	fg, bg := t.colors()
	s := gtx.Dp(size)
	return layout.Stack{Alignment: layout.Center}.Layout(gtx,
		layout.Expanded(func(gtx C) D {
			r := image.Rect(0, 0, s, s)
			paint.FillShape(gtx.Ops, bg, clip.UniformRRect(r, gtx.Dp(radiusM)).Op(gtx.Ops))
			return D{Size: r.Max}
		}),
		layout.Stacked(func(gtx C) D {
			gtx.Constraints = layout.Exact(imagePt(s))
			return layout.Center.Layout(gtx, func(gtx C) D { return drawIcon(gtx, name, size*0.55, fg) })
		}),
	)
}

// connRow is a tappable row with an icon tile, a title over a one-line
// summary, a status pill and a chevron: the home 연결 card.
func connRow(gtx C, th *material.Theme, clk *widget.Clickable, iconName string, iconTone tone, title, sub, status string, statusTone tone) D {
	return clk.Layout(gtx, func(gtx C) D {
		pointer.CursorPointer.Add(gtx.Ops)
		gtx.Constraints.Min.X = gtx.Constraints.Max.X
		return layout.Inset{Top: 4, Bottom: 4}.Layout(gtx, func(gtx C) D {
			return row(gtx,
				layout.Rigid(func(gtx C) D { return iconTile(gtx, iconName, iconTone, 40) }),
				hspace(gapS),
				layout.Flexed(1, func(gtx C) D {
					return column(gtx,
						layout.Rigid(line(th, textL, title, colText, font.SemiBold).Layout),
						vspace(2),
						layout.Rigid(line(th, textS, sub, colSub, font.Normal).Layout),
					)
				}),
				layout.Rigid(func(gtx C) D {
					if status == "" {
						return D{}
					}
					return layout.Inset{Left: 8}.Layout(gtx, statusPill(th, status, statusTone))
				}),
				hspace(2),
				layout.Rigid(func(gtx C) D { return drawIcon(gtx, "chevron", 20, colFaint) }),
			)
		})
	})
}

// toggleRowIcon is toggleRow with an icon tile in front, for rhythm in a
// card of several switches.
func toggleRowIcon(gtx C, th *material.Theme, b *widget.Bool, iconName string, t tone, title, sub string) D {
	return row(gtx,
		layout.Rigid(func(gtx C) D { return iconTile(gtx, iconName, t, 36) }),
		hspace(gapS),
		layout.Flexed(1, titled(th, title, sub)),
		hspace(gapS),
		layout.Rigid(toggle(th, b, title).Layout),
	)
}

// fieldRow puts a title (and a grey line) on the left and a narrow
// one-line field of width dp on the right: a port, a number.
func fieldRow(gtx C, th *material.Theme, ed *widget.Editor, title, sub, hint string, width unit.Dp) D {
	return row(gtx,
		layout.Flexed(1, titled(th, title, sub)),
		hspace(gapS),
		layout.Rigid(func(gtx C) D {
			w := gtx.Dp(width)
			gtx.Constraints.Min.X, gtx.Constraints.Max.X = w, w
			ed.SingleLine = true
			border := widget.Border{Color: colLine, CornerRadius: radiusM, Width: unit.Dp(1)}
			return border.Layout(gtx, func(gtx C) D {
				return layout.Inset{Top: 9, Bottom: 9, Left: 12, Right: 12}.Layout(gtx, func(gtx C) D {
					gtx.Constraints.Min.X = gtx.Constraints.Max.X
					e := material.Editor(th, ed, hint)
					e.HintColor = colFaint
					e.TextSize = textL
					return e.Layout(gtx)
				})
			})
		}),
	)
}

// textArea is a bordered box of several lines (at least lines tall) that
// grows with its text.
func textArea(th *material.Theme, ed *widget.Editor, hint string, lines int) layout.Widget {
	return func(gtx C) D {
		ed.SingleLine = false
		return fieldFrame(gtx, func(gtx C) D {
			gtx.Constraints.Min.Y = int(float32(lines) * float32(gtx.Sp(textL)) * 1.45)
			e := material.Editor(th, ed, hint)
			e.HintColor = colFaint
			e.TextSize = textL
			return e.Layout(gtx)
		})
	}
}

// --- banners -------------------------------------------------------------------

// bannerIcon names the icon of a banner in tone t.
func bannerIcon(t tone) string {
	switch t {
	case toneOK:
		return "ok"
	case toneWarn:
		return "warn"
	case toneBad:
		return "error"
	}
	return "info"
}

// banner is an inline status line on a tinted panel: an icon and text in
// the tone's colour. For a page-level message use u.setNotice instead,
// which puts one at the top of the page.
func banner(gtx C, th *material.Theme, t tone, txt string) D {
	fg, bg := t.colors()
	return surface(gtx, bg, radiusM, func(gtx C) D {
		return layout.Inset{Top: 12, Bottom: 12, Left: 14, Right: 14}.Layout(gtx, func(gtx C) D {
			return layout.Flex{Alignment: layout.Start}.Layout(gtx,
				layout.Rigid(func(gtx C) D { return drawIcon(gtx, bannerIcon(t), 18, fg) }),
				hspace(10),
				layout.Flexed(1, label(th, textM, txt, colText, font.Medium).Layout),
			)
		})
	})
}

// --- lists -------------------------------------------------------------------

// listEditor is the state of a list the user edits: a delete button per
// row and an add button under them. Each frame, before Layout:
//
//	if i := p.hubs.Deleted(gtx, len(hubs)); i >= 0 { hubs = slices.Delete(hubs, i, i+1) }
//	if p.hubs.Added(gtx) { … }
type listEditor struct {
	del []widget.Clickable
	add widget.Clickable
}

// Deleted returns the row whose delete button was pressed, or -1.
func (l *listEditor) Deleted(gtx C, n int) int {
	l.grow(n)
	hit := -1
	for i := range n {
		if l.del[i].Clicked(gtx) {
			hit = i
		}
	}
	return hit
}

// Added reports a press of the add button.
func (l *listEditor) Added(gtx C) bool { return l.add.Clicked(gtx) }

func (l *listEditor) grow(n int) {
	if len(l.del) < n {
		l.del = append(l.del, make([]widget.Clickable, n-len(l.del))...)
	}
}

// Layout draws n rows (row draws row i's content), each with a delete
// button, hairlines between them, then the add button; empty is the grey
// line shown without rows, addLabel the button's text ("" hides it).
func (l *listEditor) Layout(gtx C, th *material.Theme, n int, empty, addLabel string, row func(gtx C, i int) D) D {
	l.grow(n)
	children := make([]layout.FlexChild, 0, 2*n+2)
	if n == 0 && empty != "" {
		children = append(children, layout.Rigid(label(th, textM, empty, colSub, font.Normal).Layout))
	}
	for i := range n {
		if i > 0 {
			children = append(children, vspace(8), layout.Rigid(divider), vspace(8))
		}
		children = append(children, layout.Rigid(func(gtx C) D {
			return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
				layout.Flexed(1, func(gtx C) D { return row(gtx, i) }),
				hspace(8),
				layout.Rigid(func(gtx C) D { return iconButton(gtx, &l.del[i], "delete", 32, colDanger, colGhost) }),
			)
		}))
	}
	if addLabel != "" {
		children = append(children, vspace(gapS), layout.Rigid(smallButton(th, &l.add, "+ "+addLabel, colAccent).Layout))
	}
	return column(gtx, children...)
}

// --- copy ----------------------------------------------------------------------

// copiedShown is how long a copy button says it copied.
const copiedShown = 1500 * time.Millisecond

// copyButton puts a text on the clipboard and says so for a moment.
type copyButton struct {
	clk   widget.Clickable
	until time.Time
}

// Layout draws the button (txt, then done once pressed) and copies value
// when it is pressed.
func (c *copyButton) Layout(gtx C, th *material.Theme, value, txt, done string) D {
	if c.clk.Clicked(gtx) {
		gtx.Execute(clipboard.WriteCmd{Type: "application/text", Data: io.NopCloser(strings.NewReader(value))})
		c.until = gtx.Now.Add(copiedShown)
	}
	shown := txt
	if gtx.Now.Before(c.until) {
		shown = done
		gtx.Execute(op.InvalidateCmd{At: c.until})
	}
	return smallButton(th, &c.clk, shown, colAccent).Layout(gtx)
}

// --- more inputs -----------------------------------------------------------------

// setText puts s in ed unless it already says so, so the caret stays put
// when a form is refilled with what is typed.
func setText(ed *widget.Editor, s string) {
	if ed.Text() != s {
		ed.SetText(s)
	}
}

// fieldButton is a quiet button as tall as the field beside it ([찾아보기]
// next to a path).
func fieldButton(th *material.Theme, clk *widget.Clickable, txt string) material.ButtonStyle {
	b := smallButton(th, clk, txt, colText)
	b.CornerRadius = radiusM
	b.Inset = layout.Inset{Top: 13, Bottom: 13, Left: 14, Right: 14}
	return b
}

// searchField is a filter box: a magnifier, the editor and, while it
// holds text, a clear button. The caller empties ed on clear.Clicked.
func searchField(gtx C, th *material.Theme, ed *widget.Editor, clear *widget.Clickable, hint string) D {
	return fieldFrame(gtx, func(gtx C) D {
		return row(gtx,
			layout.Rigid(func(gtx C) D { return drawIcon(gtx, "search", 18, colSub) }),
			hspace(8),
			layout.Flexed(1, func(gtx C) D {
				e := material.Editor(th, ed, hint)
				e.HintColor = colFaint
				e.TextSize = textL
				return e.Layout(gtx)
			}),
			layout.Rigid(func(gtx C) D {
				if ed.Len() == 0 {
					return D{}
				}
				return layout.Inset{Left: 8}.Layout(gtx, func(gtx C) D {
					return iconButton(gtx, clear, "close", 20, colSub, colGhost)
				})
			}),
		)
	})
}

// timeStepper is a value ("22:00") between a back and a forward button;
// the caller moves the value on down/up.Clicked.
func timeStepper(th *material.Theme, down, up *widget.Clickable, v string) layout.Widget {
	return func(gtx C) D {
		return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
			layout.Rigid(func(gtx C) D { return iconButton(gtx, down, "chevleft", 32, colText, colGhost) }),
			layout.Rigid(func(gtx C) D {
				gtx.Constraints.Min.X = gtx.Dp(64)
				return layout.Center.Layout(gtx, line(th, textL, v, colText, font.SemiBold).Layout)
			}),
			layout.Rigid(func(gtx C) D { return iconButton(gtx, up, "chevron", 32, colText, colGhost) }),
		)
	}
}

// numberGrid is a row of the numbers 1…n in equal cells: the chosen one in
// accent, the ones taken elsewhere faint (preset slots).
type numberGrid struct {
	clks []widget.Clickable
}

func (g *numberGrid) grow(n int) {
	if len(g.clks) < n {
		g.clks = append(g.clks, make([]widget.Clickable, n-len(g.clks))...)
	}
}

// Clicked returns the number (1 …) tapped since the last frame, or 0.
func (g *numberGrid) Clicked(gtx C, n int) int {
	g.grow(n)
	hit := 0
	for i := range n {
		if g.clks[i].Clicked(gtx) {
			hit = i + 1
		}
	}
	return hit
}

// numberCellH is a numberGrid cell's height.
const numberCellH unit.Dp = 34

// Layout draws 1…n across the width.
func (g *numberGrid) Layout(gtx C, th *material.Theme, n, selected int, taken func(int) bool) D {
	g.grow(n)
	cells := make([]layout.Widget, n)
	for i := range n {
		num := i + 1
		cells[i] = func(gtx C) D {
			fg, bg := colText, colGhost
			switch {
			case num == selected:
				fg, bg = colOnAccent, colAccent
			case taken(num):
				fg = colFaint
			}
			return g.clks[i].Layout(gtx, func(gtx C) D {
				if num != selected && !taken(num) {
					pointer.CursorPointer.Add(gtx.Ops)
					if g.clks[i].Hovered() {
						bg = mix(bg, colText, 0.06)
					}
				}
				w, h := gtx.Constraints.Max.X, gtx.Dp(numberCellH)
				r := clip.UniformRRect(image.Rect(0, 0, w, h), gtx.Dp(radiusS))
				paint.FillShape(gtx.Ops, bg, r.Op(gtx.Ops))
				gtx.Constraints = layout.Exact(image.Pt(w, h))
				return layout.Center.Layout(gtx, func(gtx C) D {
					l := label(th, textM, strconv.Itoa(num), fg, font.SemiBold)
					l.Alignment = text.Middle
					return l.Layout(gtx)
				})
			})
		}
	}
	return grid(gtx, 4, cells...)
}

// checkChip is a chip of a multiple choice: grey while off, accent text
// on the accent tint with a check while on.
func checkChip(th *material.Theme, clk *widget.Clickable, txt string, on bool) layout.Widget {
	return func(gtx C) D {
		return clk.Layout(gtx, func(gtx C) D {
			pointer.CursorPointer.Add(gtx.Ops)
			fg, bg, weight := colText, colGhost, font.Medium
			if on {
				fg, bg, weight = colAccent, colAccentDim, font.SemiBold
			}
			if clk.Hovered() {
				bg = mix(bg, fg, 0.08)
			}
			return layout.Stack{}.Layout(gtx,
				layout.Expanded(func(gtx C) D {
					r := image.Rectangle{Max: gtx.Constraints.Min}
					paint.FillShape(gtx.Ops, bg, clip.UniformRRect(r, gtx.Dp(radiusS)).Op(gtx.Ops))
					return D{Size: gtx.Constraints.Min}
				}),
				layout.Stacked(func(gtx C) D {
					left := unit.Dp(12)
					if on {
						left = 8
					}
					return layout.Inset{Top: 7, Bottom: 7, Left: left, Right: 12}.Layout(gtx, func(gtx C) D {
						children := []layout.FlexChild{}
						if on {
							children = append(children,
								layout.Rigid(func(gtx C) D { return drawIcon(gtx, "check", 16, fg) }),
								hspace(4))
						}
						children = append(children, layout.Rigid(line(th, textM, txt, fg, weight).Layout))
						return layout.Flex{Alignment: layout.Middle}.Layout(gtx, children...)
					})
				}),
			)
		})
	}
}

// --- choice list -----------------------------------------------------------------

// choiceItem is one row of a choiceList: a title, a grey line and an
// optional pill in a tone.
type choiceItem struct {
	title, sub string
	pill       string
	tone       tone
}

// choiceList picks one of several rows that each need more than a chip's
// word (WoL adapters: a name, a MAC, a state). The selected row has a
// tinted panel and a check.
type choiceList struct {
	clks []widget.Clickable
}

// Clicked returns the row tapped since the last frame, or -1.
func (c *choiceList) Clicked(gtx C, n int) int {
	c.grow(n)
	hit := -1
	for i := range n {
		if c.clks[i].Clicked(gtx) {
			hit = i
		}
	}
	return hit
}

func (c *choiceList) grow(n int) {
	if len(c.clks) < n {
		c.clks = append(c.clks, make([]widget.Clickable, n-len(c.clks))...)
	}
}

// Layout draws the rows, selected (-1 for none) highlighted.
func (c *choiceList) Layout(gtx C, th *material.Theme, items []choiceItem, selected int) D {
	c.grow(len(items))
	children := make([]layout.FlexChild, 0, 2*len(items))
	for i, it := range items {
		if i > 0 {
			children = append(children, vspace(4))
		}
		children = append(children, layout.Rigid(func(gtx C) D {
			return choiceRow(gtx, th, &c.clks[i], it, i == selected)
		}))
	}
	return column(gtx, children...)
}

// choiceRow is one row of a choiceList.
func choiceRow(gtx C, th *material.Theme, clk *widget.Clickable, it choiceItem, selected bool) D {
	return clk.Layout(gtx, func(gtx C) D {
		pointer.CursorPointer.Add(gtx.Ops)
		gtx.Constraints.Min.X = gtx.Constraints.Max.X
		bg, edge := colCard, colLine
		if selected {
			bg, edge = colAccentDim, colAccentDim
		}
		border := widget.Border{Color: edge, CornerRadius: radiusM, Width: 1}
		return border.Layout(gtx, func(gtx C) D {
			return surface(gtx, bg, radiusM, func(gtx C) D {
				return layout.Inset{Top: 10, Bottom: 10, Left: 12, Right: 12}.Layout(gtx, func(gtx C) D {
					return row(gtx,
						layout.Rigid(func(gtx C) D {
							if selected {
								return drawIcon(gtx, "check", 20, colAccent)
							}
							return D{Size: imagePt(gtx.Dp(20))}
						}),
						hspace(10),
						layout.Flexed(1, titled(th, it.title, it.sub)),
						layout.Rigid(func(gtx C) D {
							if it.pill == "" {
								return D{}
							}
							return layout.Inset{Left: 8}.Layout(gtx, statusPill(th, it.pill, it.tone))
						}),
					)
				})
			})
		})
	})
}

// --- more rows -------------------------------------------------------------------

// actionRow is a tappable row that does something: an icon tile, a title
// (red for a destructive one) over a grey line, and a chevron.
func actionRow(gtx C, th *material.Theme, clk *widget.Clickable, iconName string, t tone, title, sub string, danger bool) D {
	titleCol := colText
	if danger {
		titleCol = colDanger
	}
	return clk.Layout(gtx, func(gtx C) D {
		pointer.CursorPointer.Add(gtx.Ops)
		gtx.Constraints.Min.X = gtx.Constraints.Max.X
		return layout.Inset{Top: 2, Bottom: 2}.Layout(gtx, func(gtx C) D {
			return row(gtx,
				layout.Rigid(func(gtx C) D { return iconTile(gtx, iconName, t, 36) }),
				hspace(gapS),
				layout.Flexed(1, func(gtx C) D {
					return column(gtx,
						layout.Rigid(label(th, textL, title, titleCol, font.Medium).Layout),
						vspace(3),
						layout.Rigid(label(th, textS, sub, colSub, font.Normal).Layout),
					)
				}),
				hspace(4),
				layout.Rigid(func(gtx C) D { return drawIcon(gtx, "chevron", 20, colFaint) }),
			)
		})
	})
}

// pickRow is one entry of a pick-from list (a running program): its name
// and an add mark; the name turns accent under the pointer.
func pickRow(gtx C, th *material.Theme, clk *widget.Clickable, name string) D {
	return clk.Layout(gtx, func(gtx C) D {
		pointer.CursorPointer.Add(gtx.Ops)
		gtx.Constraints.Min.X = gtx.Constraints.Max.X
		col := colText
		if clk.Hovered() {
			col = colAccent
		}
		return layout.Inset{Top: 2, Bottom: 2, Left: 4}.Layout(gtx, func(gtx C) D {
			return row(gtx,
				layout.Flexed(1, line(th, textL, name, col, font.Normal).Layout),
				hspace(gapS),
				layout.Rigid(func(gtx C) D { return drawIcon(gtx, "add", 20, colAccent) }),
			)
		})
	})
}

// dimOpacity is how much of a dimmed row shows.
const dimOpacity = 0.45

// dim fades what is drawn until the returned func runs, when on is set: a
// row that only matters while another setting is on.
//
//	defer dim(gtx, !p.session.Value)()
func dim(gtx C, on bool) func() {
	if !on {
		return func() {}
	}
	return paint.PushOpacity(gtx.Ops, dimOpacity).Pop
}

// natural lays w out at its own width instead of the card's (a lone
// button in a column).
func natural(w layout.Widget) layout.Widget {
	return func(gtx C) D {
		gtx.Constraints.Min.X = 0
		return w(gtx)
	}
}

// --- live and floating pills ------------------------------------------------------

// liveButton is a pill that shows whether something updates live (a green
// dot and liveTxt) or is paused (a pause mark and pausedTxt); the caller
// switches it on clk.Clicked.
func liveButton(gtx C, th *material.Theme, clk *widget.Clickable, live bool, liveTxt, pausedTxt string) D {
	txt, fg, bg := liveTxt, colOK, mix(colCard, colOK, 0.14)
	if !live {
		txt, fg, bg = pausedTxt, colSub, colGhost
	}
	return clk.Layout(gtx, func(gtx C) D {
		pointer.CursorPointer.Add(gtx.Ops)
		return layout.Stack{}.Layout(gtx,
			layout.Expanded(func(gtx C) D {
				r := image.Rectangle{Max: gtx.Constraints.Min}
				c := bg
				if clk.Hovered() {
					c = mix(bg, fg, 0.12)
				}
				paint.FillShape(gtx.Ops, c, clip.UniformRRect(r, gtx.Dp(radiusS)).Op(gtx.Ops))
				return D{Size: r.Max}
			}),
			layout.Stacked(func(gtx C) D {
				return layout.Inset{Top: 7, Bottom: 7, Left: 10, Right: 12}.Layout(gtx, func(gtx C) D {
					return row(gtx,
						layout.Rigid(func(gtx C) D {
							if !live {
								return drawIcon(gtx, "pause", 16, fg)
							}
							return layout.UniformInset(4).Layout(gtx, func(gtx C) D { return statusDot(gtx, fg, 8) })
						}),
						hspace(4),
						layout.Rigid(line(th, textM, txt, fg, font.SemiBold).Layout),
					)
				})
			}),
		)
	})
}

// floatingPill is an accent pill with an icon and a word, laid over
// content (맨 아래로 over the log).
func floatingPill(gtx C, th *material.Theme, clk *widget.Clickable, iconName, txt string) D {
	return clk.Layout(gtx, func(gtx C) D {
		pointer.CursorPointer.Add(gtx.Ops)
		return layout.Stack{}.Layout(gtx,
			layout.Expanded(func(gtx C) D {
				r := image.Rectangle{Max: gtx.Constraints.Min}
				c := colAccent
				if clk.Hovered() {
					c = mix(colAccent, colOnAccent, 0.12)
				}
				paint.FillShape(gtx.Ops, c, clip.UniformRRect(r, r.Dy()/2).Op(gtx.Ops))
				return D{Size: r.Max}
			}),
			layout.Stacked(func(gtx C) D {
				return layout.Inset{Top: 8, Bottom: 8, Left: 12, Right: 14}.Layout(gtx, func(gtx C) D {
					return row(gtx,
						layout.Rigid(func(gtx C) D { return drawIcon(gtx, iconName, 16, colOnAccent) }),
						hspace(6),
						layout.Rigid(line(th, textM, txt, colOnAccent, font.SemiBold).Layout),
					)
				})
			}),
		)
	})
}

// --- Korean line breaking --------------------------------------------------------

// wordJoiner (U+2060) forbids a line break on either side and draws
// nothing.
const wordJoiner = '\u2060'

// keepWords makes Korean text wrap at spaces only, like CSS
// word-break: keep-all. The line breaker allows a break between any two
// Hangul syllables, so a sentence would otherwise leave one syllable
// ("다") alone on the last line. A word joiner goes between two
// neighbours when either is Hangul and neither is a space; a word too
// long for the line is still broken (Gio falls back to graphemes).
func keepWords(s string) string {
	if !strings.ContainsFunc(s, isHangul) {
		return s
	}
	var b strings.Builder
	b.Grow(len(s) * 2)
	var prev rune
	for i, r := range s {
		if i > 0 && (isHangul(prev) || isHangul(r)) && !unicode.IsSpace(prev) && !unicode.IsSpace(r) {
			b.WriteRune(wordJoiner)
		}
		b.WriteRune(r)
		prev = r
	}
	return b.String()
}

// isHangul reports a Hangul syllable or jamo.
func isHangul(r rune) bool { return unicode.Is(unicode.Hangul, r) }
