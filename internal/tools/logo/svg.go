package main

// A rasteriser for the small SVG subset the logo masters use: <rect> with
// rx, <circle>, and <path> made of M and A commands (circular arcs) that
// is filled or stroked with round caps, each with an optional opacity.
// Coverage is supersampled, so the output does not depend on any SVG
// library being installed.

import (
	"encoding/xml"
	"errors"
	"fmt"
	"image"
	"image/color"
	"io"
	"math"
	"os"
	"strconv"
	"strings"
)

// shape is one element: it reports whether a point (in viewBox units) is
// inside it.
type shape struct {
	inside func(x, y float64) bool
	col    color.NRGBA // straight alpha, opacity folded in
}

type drawing struct {
	w, h   float64 // viewBox size
	shapes []shape
}

func loadSVG(path string) (*drawing, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return parseSVG(f)
}

func parseSVG(r io.Reader) (*drawing, error) {
	dec := xml.NewDecoder(r)
	d := &drawing{}
	for {
		tok, err := dec.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}
		el, ok := tok.(xml.StartElement)
		if !ok {
			continue
		}
		a := map[string]string{}
		for _, at := range el.Attr {
			a[at.Name.Local] = at.Value
		}
		num := func(k string) float64 {
			v, _ := strconv.ParseFloat(a[k], 64)
			return v
		}
		switch el.Name.Local {
		case "svg":
			vb := strings.Fields(a["viewBox"])
			if len(vb) != 4 {
				return nil, fmt.Errorf("svg: viewBox %q", a["viewBox"])
			}
			d.w, _ = strconv.ParseFloat(vb[2], 64)
			d.h, _ = strconv.ParseFloat(vb[3], 64)
		case "rect":
			x, y, w, h, rx := num("x"), num("y"), num("width"), num("height"), num("rx")
			d.add(a, a["fill"], func(px, py float64) bool { return inRoundRect(px, py, x, y, w, h, rx) })
		case "circle":
			cx, cy, rr := num("cx"), num("cy"), num("r")
			d.add(a, a["fill"], func(px, py float64) bool { return math.Hypot(px-cx, py-cy) <= rr })
		case "path":
			segs, err := parsePath(a["d"])
			if err != nil {
				return nil, err
			}
			if a["stroke"] == "" || a["stroke"] == "none" {
				return nil, fmt.Errorf("svg: only stroked paths are supported")
			}
			if a["stroke-linecap"] != "round" {
				return nil, fmt.Errorf("svg: only round line caps are supported")
			}
			half := num("stroke-width") / 2
			d.add(a, a["stroke"], func(px, py float64) bool {
				for _, s := range segs {
					if s.dist(px, py) <= half {
						return true
					}
				}
				return false
			})
		}
	}
	if d.w == 0 || d.h == 0 {
		return nil, fmt.Errorf("svg: no viewBox")
	}
	return d, nil
}

func (d *drawing) add(a map[string]string, paint string, inside func(x, y float64) bool) {
	c, err := parseColor(paint)
	if err != nil {
		return // fill="none"
	}
	if o, err := strconv.ParseFloat(a["opacity"], 64); err == nil {
		c.A = uint8(math.Round(float64(c.A) * o))
	}
	d.shapes = append(d.shapes, shape{inside: inside, col: c})
}

func parseColor(s string) (color.NRGBA, error) {
	s = strings.TrimPrefix(strings.TrimSpace(s), "#")
	if len(s) == 3 {
		s = string([]byte{s[0], s[0], s[1], s[1], s[2], s[2]})
	}
	if len(s) != 6 {
		return color.NRGBA{}, fmt.Errorf("color %q", s)
	}
	v, err := strconv.ParseUint(s, 16, 32)
	if err != nil {
		return color.NRGBA{}, err
	}
	return color.NRGBA{uint8(v >> 16), uint8(v >> 8), uint8(v), 255}, nil
}

func inRoundRect(px, py, x, y, w, h, r float64) bool {
	if px < x || py < y || px > x+w || py > y+h {
		return false
	}
	cx := math.Max(x+r, math.Min(px, x+w-r))
	cy := math.Max(y+r, math.Min(py, y+h-r))
	return math.Hypot(px-cx, py-cy) <= r
}

// arc is a circular arc from angle a0 sweeping da (radians, y down).
type arc struct {
	cx, cy, r, a0, da float64
}

func (s arc) point(a float64) (float64, float64) {
	return s.cx + s.r*math.Cos(a), s.cy + s.r*math.Sin(a)
}

// dist is the distance from p to the arc.
func (s arc) dist(px, py float64) float64 {
	phi := math.Atan2(py-s.cy, px-s.cx)
	var t float64
	if s.da >= 0 {
		t = math.Mod(phi-s.a0+4*math.Pi, 2*math.Pi)
	} else {
		t = math.Mod(s.a0-phi+4*math.Pi, 2*math.Pi)
	}
	if t <= math.Abs(s.da) {
		return math.Abs(math.Hypot(px-s.cx, py-s.cy) - s.r)
	}
	x0, y0 := s.point(s.a0)
	x1, y1 := s.point(s.a0 + s.da)
	return math.Min(math.Hypot(px-x0, py-y0), math.Hypot(px-x1, py-y1))
}

// parsePath reads "M x y" followed by "a"/"A" circular arcs.
func parsePath(d string) ([]arc, error) {
	tok := tokenize(d)
	var out []arc
	var x, y float64
	cmd := byte(0)
	nums := func(n int) ([]float64, error) {
		if len(tok) < n {
			return nil, fmt.Errorf("path %q: short", d)
		}
		v := make([]float64, n)
		for i := range v {
			f, err := strconv.ParseFloat(tok[i], 64)
			if err != nil {
				return nil, fmt.Errorf("path %q: %w", d, err)
			}
			v[i] = f
		}
		tok = tok[n:]
		return v, nil
	}
	for len(tok) > 0 {
		if c := tok[0][0]; len(tok[0]) == 1 && strings.IndexByte("MmAa", c) >= 0 {
			cmd = c
			tok = tok[1:]
		}
		switch cmd {
		case 'M', 'm':
			v, err := nums(2)
			if err != nil {
				return nil, err
			}
			if cmd == 'm' {
				x, y = x+v[0], y+v[1]
			} else {
				x, y = v[0], v[1]
			}
		case 'A', 'a':
			v, err := nums(7)
			if err != nil {
				return nil, err
			}
			if v[0] != v[1] || v[2] != 0 {
				return nil, fmt.Errorf("path %q: only circular arcs", d)
			}
			x2, y2 := v[5], v[6]
			if cmd == 'a' {
				x2, y2 = x+x2, y+y2
			}
			out = append(out, endpointArc(x, y, x2, y2, v[0], v[3] != 0, v[4] != 0))
			x, y = x2, y2
		default:
			return nil, fmt.Errorf("path %q: unsupported command", d)
		}
	}
	return out, nil
}

func tokenize(d string) []string {
	var out []string
	var cur strings.Builder
	flush := func() {
		if cur.Len() > 0 {
			out = append(out, cur.String())
			cur.Reset()
		}
	}
	for i := 0; i < len(d); i++ {
		c := d[i]
		switch {
		case c == ' ' || c == ',' || c == '\n' || c == '\t' || c == '\r':
			flush()
		case (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z'):
			flush()
			out = append(out, string(c))
		case c == '-' && cur.Len() > 0:
			flush()
			cur.WriteByte(c)
		default:
			cur.WriteByte(c)
		}
	}
	flush()
	return out
}

// endpointArc converts SVG's endpoint parameterisation (SVG 1.1 F.6.5)
// for a circle of radius r.
func endpointArc(x1, y1, x2, y2, r float64, large, sweep bool) arc {
	hx, hy := (x1-x2)/2, (y1-y2)/2
	d2 := hx*hx + hy*hy
	if d2 > r*r {
		r = math.Sqrt(d2)
	}
	k := math.Sqrt(math.Max(0, (r*r-d2)/d2))
	if large == sweep {
		k = -k
	}
	cxp, cyp := k*hy, -k*hx
	cx, cy := cxp+(x1+x2)/2, cyp+(y1+y2)/2
	a0 := math.Atan2((hy-cyp)/r, (hx-cxp)/r)
	a1 := math.Atan2((-hy-cyp)/r, (-hx-cxp)/r)
	da := a1 - a0
	if !sweep && da > 0 {
		da -= 2 * math.Pi
	} else if sweep && da < 0 {
		da += 2 * math.Pi
	}
	return arc{cx: cx, cy: cy, r: r, a0: a0, da: da}
}

// render rasterises d at n×n pixels with ss×ss samples per pixel.
func (d *drawing) render(n, ss int) *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, n, n))
	sx, sy := d.w/float64(n), d.h/float64(n)
	cov := make([]float64, len(d.shapes))
	for py := 0; py < n; py++ {
		for px := 0; px < n; px++ {
			for i := range cov {
				cov[i] = 0
			}
			for j := 0; j < ss; j++ {
				for i := 0; i < ss; i++ {
					x := (float64(px) + (float64(i)+0.5)/float64(ss)) * sx
					y := (float64(py) + (float64(j)+0.5)/float64(ss)) * sy
					for k, s := range d.shapes {
						if s.inside(x, y) {
							cov[k]++
						}
					}
				}
			}
			// Composite premultiplied, source over, in shape order.
			var r, g, b, a float64
			for k, s := range d.shapes {
				sa := float64(s.col.A) / 255 * cov[k] / float64(ss*ss)
				r = float64(s.col.R)/255*sa + r*(1-sa)
				g = float64(s.col.G)/255*sa + g*(1-sa)
				b = float64(s.col.B)/255*sa + b*(1-sa)
				a = sa + a*(1-sa)
			}
			if a > 0 {
				img.SetNRGBA(px, py, color.NRGBA{
					R: uint8(math.Round(r / a * 255)),
					G: uint8(math.Round(g / a * 255)),
					B: uint8(math.Round(b / a * 255)),
					A: uint8(math.Round(a * 255)),
				})
			}
		}
	}
	return img
}
