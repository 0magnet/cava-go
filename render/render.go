// Package render draws cava's bars into a tcell screen.
//
// Nothing here creates, initialises or finalises a screen: a [Renderer] is
// handed one and paints into it. That is the same discipline the animations in
// termanim keep, and for the same reason — it is what lets identical drawing
// code run in a terminal and inside a browser pane where something else owns
// the screen.
//
// Bars arrive in eighths of a cell, which is the unit cava works in from the
// point the FFT output is scaled to the terminal height. A cell shows the
// remainder as one of the eight partial block glyphs, so a 40-row terminal has
// 320 distinct bar heights rather than 40.
package render

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/gdamore/tcell/v3"
	"github.com/gdamore/tcell/v3/color"
)

// bottomBlocks are the glyphs for a bar growing up from the bottom of the
// screen: index 0 is a full cell and 1 to 7 are the partial blocks, one eighth
// to seven eighths.
var bottomBlocks = [8]string{"█", "▁", "▂", "▃", "▄", "▅", "▆", "▇"}

// topBlocks are the same fractions hanging from the top of the cell. Five of
// them are Symbols for Legacy Computing, added in Unicode 13 — a font without
// them will show boxes, which is why cava warns that orientations other than
// 'bottom' depend on the font.
var topBlocks = [8]string{"█", "▔", "\U0001FB82", "\U0001FB83", "▀", "\U0001FB84", "\U0001FB85", "\U0001FB86"}

// Orientation is which edge the bars grow from.
type Orientation int

const (
	// Bottom grows bars upwards from the bottom row.
	Bottom Orientation = iota
	// Top hangs them from the top row.
	Top
)

// Style is the colouring, in the same terms as cava's [color] section.
//
// Colors are either one of cava's eight names, "default" to leave the
// terminal's own, or "#rrggbb".
type Style struct {
	Foreground string
	Background string

	// Gradient colours the screen from the bottom of the bars to the top,
	// interpolating between two to eight stops.
	Gradient bool
	// GradientColors are the stops, bottom first.
	GradientColors []string

	// HorizontalGradient colours by bar rather than by row, so each bar is one
	// colour and the row of bars runs through the stops.
	HorizontalGradient bool
	// HorizontalColors are those stops, leftmost first.
	HorizontalColors []string

	// BlendDirection decides how the two are mixed when both are on: "up",
	// "down", "left" or "right". "up" means the horizontal gradient owns the
	// bottom of the screen and the vertical one the top.
	BlendDirection string
}

// Layout is where the bars go on a screen of a given size.
type Layout struct {
	// Cols and Rows are the drawing area in cells. Rows excludes the x-axis
	// label row if there is one.
	Cols, Rows int
	// Bars is how many bars fit, which is what the plan must be built for.
	Bars int
	// BarWidth and BarSpacing are in cells.
	BarWidth, BarSpacing int
	// Offset is the left margin that centres the bars in the leftover cells.
	Offset int
}

// ComputeLayout works out how many bars fit and where they start.
//
// fixedBars of zero fills the width. stereo forces an even count, because the
// two channels are drawn as mirror images of each other and an odd bar would
// have nowhere to be. A width too narrow for a fixed bar count is an error
// rather than a silent truncation, as it is in cava.
func ComputeLayout(cols, rows, fixedBars, barWidth, barSpacing int, stereo, centerAlign bool) (Layout, error) {
	if barWidth < 1 {
		barWidth = 1
	}
	if barSpacing < 0 {
		barSpacing = 0
	}
	outputChannels := 1
	if stereo {
		outputChannels = 2
	}

	var bars int
	if fixedBars > 0 {
		bars = fixedBars
		if bars*barWidth+bars*barSpacing-barSpacing > cols {
			return Layout{}, fmt.Errorf("window is too narrow for number of bars set, maximum is %d",
				(cols-barSpacing)/(barWidth+barSpacing))
		}
		if bars < outputChannels {
			return Layout{}, fmt.Errorf("fixed number of bars must be at least 1 with mono output and 2 with stereo output")
		}
		if outputChannels == 2 && bars%2 != 0 {
			return Layout{}, fmt.Errorf("must have even number of bars with stereo output")
		}
	} else {
		bars = (cols + barSpacing) / (barWidth + barSpacing)
		if bars/outputChannels > 512 {
			bars = 512 * outputChannels
		}
		if bars <= 1 {
			bars = outputChannels
		}
		if outputChannels == 2 && bars%2 != 0 {
			bars--
		}
	}

	offset := 0
	if centerAlign {
		offset = (cols - bars*barWidth - bars*barSpacing + barSpacing) / 2
		if offset < 0 {
			offset = 0
		}
	}

	return Layout{
		Cols: cols, Rows: rows, Bars: bars,
		BarWidth: barWidth, BarSpacing: barSpacing, Offset: offset,
	}, nil
}

// Renderer paints bars. Build one with [New] whenever the layout or the style
// changes; it caches the gradient, which is the only expensive part.
type Renderer struct {
	layout      Layout
	orientation Orientation

	base tcell.Style

	// vertical is one colour per row, bottom first; horizontal is one per bar.
	// Either may be nil.
	vertical   []color.Color
	horizontal []color.Color
	// blended is rows*bars when both gradients are on.
	blended []color.Color

	// glyphs holds each of the eight block characters repeated to the bar
	// width, and blank and gap the same for an empty cell and the space
	// between bars. Building them once turns the inner loop into one call per
	// bar per row.
	glyphs [8]string
	blank  string
	gap    string
}

// New returns a renderer for a layout and style.
func New(layout Layout, orientation Orientation, style Style) (*Renderer, error) {
	fg, err := parseColor(style.Foreground)
	if err != nil {
		return nil, fmt.Errorf("foreground: %w", err)
	}
	bg, err := parseColor(style.Background)
	if err != nil {
		return nil, fmt.Errorf("background: %w", err)
	}

	r := &Renderer{
		layout:      layout,
		orientation: orientation,
		base:        tcell.StyleDefault.Foreground(fg).Background(bg),
	}

	if style.Gradient {
		r.vertical, err = interpolate(style.GradientColors, layout.Rows)
		if err != nil {
			return nil, fmt.Errorf("gradient: %w", err)
		}
	}
	if style.HorizontalGradient {
		r.horizontal, err = interpolate(style.HorizontalColors, layout.Bars)
		if err != nil {
			return nil, fmt.Errorf("horizontal_gradient: %w", err)
		}
	}
	if r.vertical != nil && r.horizontal != nil {
		r.blended = blend(r.vertical, r.horizontal, style.BlendDirection)
	}

	set := bottomBlocks
	if orientation == Top {
		set = topBlocks
	}
	for i, g := range set {
		r.glyphs[i] = strings.Repeat(g, layout.BarWidth)
	}
	r.blank = strings.Repeat(" ", layout.BarWidth)
	r.gap = strings.Repeat(" ", layout.BarSpacing)

	return r, nil
}

// Layout returns the layout the renderer was built for.
func (r *Renderer) Layout() Layout { return r.layout }

// Draw paints one frame. bars holds one height per bar in eighths of a cell,
// and must be at least Layout().Bars long.
//
// It clears and rewrites the whole drawing area every frame. cava tracks the
// previous frame and emits cursor moves for the cells that changed, which is
// what its "noncurses" output method is; tcell already does exactly that
// underneath, so doing it again here would only make the diff worse.
func (r *Renderer) Draw(screen tcell.Screen, bars []int) {
	l := r.layout
	n := l.Bars
	if len(bars) < n {
		n = len(bars)
	}

	for row := 0; row < l.Rows; row++ {
		// row counts from the base of the bars: 0 is the bottom row when they
		// grow up and the top row when they hang down. The gradient is
		// indexed the same way, so the first gradient stop is always at the
		// root of the bar.
		screenRow := row
		if r.orientation == Bottom {
			screenRow = l.Rows - row - 1
		}
		for i := 0; i < n; i++ {
			cell := bars[i] - row*8 //nolint:gosec // i is bounded by n, which is bounded by len(bars)
			var glyph string
			switch {
			case cell < 1:
				glyph = r.blank
			case cell > 7:
				glyph = r.glyphs[0]
			default:
				glyph = r.glyphs[cell]
			}
			x := l.Offset + i*(l.BarWidth+l.BarSpacing)
			screen.PutStrStyled(x, screenRow, glyph, r.style(row, i))
			if l.BarSpacing > 0 && i < n-1 {
				screen.PutStrStyled(x+l.BarWidth, screenRow, r.gap, r.base)
			}
		}
	}
}

// style returns the style for one cell, row counted from the base of the bars.
func (r *Renderer) style(row, bar int) tcell.Style {
	switch {
	case r.blended != nil:
		return r.base.Foreground(r.blended[row*r.layout.Bars+bar])
	case r.vertical != nil:
		return r.base.Foreground(r.vertical[row])
	case r.horizontal != nil:
		return r.base.Foreground(r.horizontal[bar])
	}
	return r.base
}

// DrawXAxis writes the lower cut-off frequency of every few bars along the
// row below (or above) the bars, which is cava's `xaxis = frequency`.
//
// Labels are placed only where there is room for the whole number plus a
// space, so a narrow terminal gets fewer of them rather than a smear.
func (r *Renderer) DrawXAxis(screen tcell.Screen, row int, cutoffs []float32) {
	l := r.layout
	next := 0
	for i := 0; i < l.Bars && i < len(cutoffs); i++ {
		x := l.Offset + i*(l.BarWidth+l.BarSpacing)
		if x < next {
			continue
		}
		label := formatFrequency(cutoffs[i])
		if x+len(label) > l.Cols {
			break
		}
		screen.PutStrStyled(x, row, label, r.base)
		next = x + len(label) + 1
	}
}

// formatFrequency renders a cut-off the way cava does: hertz below a
// kilohertz, and kilohertz with one decimal above it.
func formatFrequency(hz float32) string {
	if hz < 1000 {
		return strconv.Itoa(int(hz))
	}
	s := strconv.FormatFloat(float64(hz)/1000, 'f', 1, 32)
	return strings.TrimSuffix(s, ".0") + "k"
}

// Clear blanks the drawing area in the configured background colour. Call it
// on a resize; a normal frame does not need it because every cell is written.
func (r *Renderer) Clear(screen tcell.Screen) {
	line := strings.Repeat(" ", r.layout.Cols)
	for y := 0; y < r.layout.Rows+1; y++ {
		screen.PutStrStyled(0, y, line, r.base)
	}
}

// namedColors are cava's eight, in ANSI order.
var namedColors = map[string]color.Color{
	"default": color.Default,
	"black":   color.Black,
	"red":     color.Maroon,
	"green":   color.Green,
	"yellow":  color.Olive,
	"blue":    color.Navy,
	"magenta": color.Purple,
	"cyan":    color.Teal,
	"white":   color.Silver,
}

// parseColor accepts a cava colour name or a '#rrggbb' code.
//
// The names map to the eight ANSI colours rather than to their web
// equivalents, because that is what cava sets and it is the point of naming
// them: they follow whatever palette the terminal is themed with.
func parseColor(s string) (color.Color, error) {
	if s == "" {
		return color.Default, nil
	}
	if c, ok := namedColors[s]; ok {
		return c, nil
	}
	if len(s) == 7 && s[0] == '#' {
		v, err := strconv.ParseUint(s[1:], 16, 32)
		if err != nil {
			return color.Default, fmt.Errorf("invalid color %q", s)
		}
		return color.NewHexColor(int32(v)), nil //nolint:gosec // 24 bits, parsed as such
	}
	return color.Default, fmt.Errorf("invalid color %q: use a name or '#rrggbb'", s)
}

// interpolate spreads two to eight colour stops over size steps.
//
// The division is the original's and is worth keeping rather than tidying: the
// stops are given whole steps each, and the fractional remainder is carried
// along and spent one step at a time, so a gradient of three colours over ten
// rows does not put its middle stop half a row off where it belongs. The last
// step is the last stop exactly, whatever the arithmetic produced.
func interpolate(stops []string, size int) ([]color.Color, error) {
	if len(stops) < 2 {
		return nil, fmt.Errorf("need at least two colors, got %d", len(stops))
	}
	if len(stops) > 8 {
		return nil, fmt.Errorf("at most eight colors, got %d", len(stops))
	}
	if size < 1 {
		return nil, fmt.Errorf("cannot spread a gradient over %d steps", size)
	}

	defs := make([][3]int32, len(stops))
	for i, s := range stops {
		c, err := parseColor(s)
		if err != nil {
			return nil, err
		}
		// A gradient has to be interpolated, so its stops must be actual
		// values. A palette name is whatever the terminal's theme says it is,
		// and mixing two of those produces a colour that is in neither.
		if len(s) == 0 || s[0] != '#' {
			return nil, fmt.Errorf("gradient colors must be given as '#rrggbb', got %q", s)
		}
		rr, gg, bb := c.RGB()
		defs[i] = [3]int32{rr, gg, bb}
	}

	out := make([]color.Color, size)
	segments := len(stops) - 1
	rest := float64(size)/float64(segments) - float64(size/segments)
	restTotal := 0.0
	idx := 0
	for i := 0; i < segments; i++ {
		individual := size / segments
		if restTotal > 1.0 {
			individual++
			restTotal--
		}
		for n := 0; n < individual && idx < size; n++ {
			var rgb [3]int32
			for c := 0; c < 3; c++ {
				step := float64(defs[i+1][c]-defs[i][c]) * (float64(n) / float64(individual))
				rgb[c] = defs[i][c] + int32(step)
			}
			out[idx] = color.NewRGBColor(rgb[0], rgb[1], rgb[2])
			idx++
		}
		restTotal += rest
	}
	// Anything the rounding left unfilled, plus the final step, is the last
	// stop.
	last := color.NewRGBColor(defs[len(defs)-1][0], defs[len(defs)-1][1], defs[len(defs)-1][2])
	for ; idx < size; idx++ {
		out[idx] = last
	}
	out[size-1] = last
	return out, nil
}

// blend mixes a per-row gradient with a per-bar one into a full grid, weighted
// by position in the direction given.
func blend(vertical, horizontal []color.Color, direction string) []color.Color {
	rows, bars := len(vertical), len(horizontal)
	out := make([]color.Color, rows*bars)
	for i := 0; i < rows; i++ {
		height := float64(i) / float64(rows)
		for n := 0; n < bars; n++ {
			width := float64(n) / float64(bars)
			var w float64
			switch direction {
			case "down":
				w = 1 - height
			case "left":
				w = width
			case "right":
				w = 1 - width
			default: // "up"
				w = height
			}
			vr, vg, vb := vertical[i].RGB()
			hr, hg, hb := horizontal[n].RGB()
			out[i*bars+n] = color.NewRGBColor(
				int32(float64(vr)*w+float64(hr)*(1-w)),
				int32(float64(vg)*w+float64(hg)*(1-w)),
				int32(float64(vb)*w+float64(hb)*(1-w)),
			)
		}
	}
	return out
}
