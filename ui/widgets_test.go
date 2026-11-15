package ui

import (
	"image"
	"testing"
	"time"

	"gioui.org/io/input"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/unit"
	"gioui.org/widget"
)

// Every component lays out at the narrowest window in both palettes and
// takes room (no GPU needed: only the layout runs).
func TestComponentsLayout(t *testing.T) {
	th := newTheme()
	var ops op.Ops
	var router input.Router
	var clk widget.Clickable
	var b widget.Bool
	var ed widget.Editor
	var sec secretField
	var chips chipGroup
	var exp expander
	var bar saveBarState
	for _, dark := range []bool{false, true} {
		usePalette(dark)
		comps := map[string]layout.Widget{
			"primary":   primaryButton(th, &clk, "저장").Layout,
			"secondary": secondaryButton(th, &clk, "추가").Layout,
			"danger":    dangerButton(th, &clk, "종료").Layout,
			"small":     smallButton(th, &clk, "더 보기", colAccent).Layout,
			"chips":     func(gtx C) D { return chips.Layout(gtx, th, []string{"30분", "1시간", "끌 때까지"}, 1) },
			"field":     field(th, &ed, "5001"),
			"secret":    func(gtx C) D { return sec.Layout(gtx, th, "시크릿") },
			"toggle":    toggle(th, &b, "x").Layout,
			"toggleRow": func(gtx C) D { return toggleRow(gtx, th, &b, "자동 시작", "로그인할 때") },
			"listRow":   func(gtx C) D { return listRow(gtx, th, &clk, "서비스 설정", "", "포트 5001") },
			"expander":  func(gtx C) D { return exp.Layout(gtx, th, "더 보기", fixed(10)) },
			"dot":       func(gtx C) D { return statusDot(gtx, colOK, 8) },
			"usage":     func(gtx C) D { return usageBar(gtx, 0.4, colAccent) },
			"pill":      pill(th, "배터리 80%", colSub, colCard),
			"tile":      func(gtx C) D { return tile(gtx, th, &clk, "shutdown", "종료", colDanger, colDangerDim) },
			"topBar":    func(gtx C) D { return topBar(gtx, th, &clk, "SmartThings") },
			"saveBar": func(gtx C) D {
				return saveBar(gtx, th, &bar, "저장되지 않은 변경 사항", "되돌리기", "저장")
			},
			"card": func(gtx C) D { return card(gtx, fixed(10)) },
		}
		for name, w := range comps {
			ops.Reset()
			gtx := layout.Context{Ops: &ops, Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1}, Now: time.Now(),
				Constraints: layout.Constraints{Max: image.Pt(winMinW, 2000)}, Source: router.Source()}
			if d := w(gtx); d.Size.X <= 0 || d.Size.Y <= 0 || d.Size.X > winMinW {
				t.Errorf("%s (dark %v): size %v", name, dark, d.Size)
			}
		}
	}
	usePalette(false)
}
