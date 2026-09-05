package render

import (
	"strings"
	"testing"

	"github.com/gdamore/tcell/v3"
	"github.com/gdamore/tcell/v3/color"

	"github.com/0magnet/cava-go/internal/simscreen"
)

// newTestScreen returns a screen of the given size, ready to draw into.
func newTestScreen(t *testing.T, w, h int) tcell.Screen {
	t.Helper()
	s := simscreen.NewScreen()
	if err := s.Init(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Fini)
	s.SetSize(w, h)
	return s
}

// column reads one column of the screen from the top down.
func column(s tcell.Screen, x, rows int) []string {
	out := make([]string, rows)
	for y := 0; y < rows; y++ {
		str, _, _ := s.Get(x, y)
		out[y] = str
	}
	return out
}

// TestDrawPartialBlocks is the reason bars are counted in eighths: a cell can
// show a fraction of itself, so a four-row terminal has thirty-two distinct
// heights rather than four.
func TestDrawPartialBlocks(t *testing.T) {
	screen := newTestScreen(t, 4, 4)
	layout := Layout{Cols: 4, Rows: 4, Bars: 1, BarWidth: 1, BarSpacing: 0}
	r, err := New(layout, Bottom, Style{Foreground: "default", Background: "default"})
	if err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		height int
		want   []string // top row first
	}{
		{0, []string{" ", " ", " ", " "}},
		{1, []string{" ", " ", " ", "▁"}},
		{4, []string{" ", " ", " ", "▄"}},
		{7, []string{" ", " ", " ", "▇"}},
		{8, []string{" ", " ", " ", "█"}},
		{9, []string{" ", " ", "▁", "█"}},
		{12, []string{" ", " ", "▄", "█"}},
		{32, []string{"█", "█", "█", "█"}},
		{99, []string{"█", "█", "█", "█"}},
	} {
		r.Draw(screen, []int{tc.height})
		screen.Show()
		got := column(screen, 0, 4)
		for i := range tc.want {
			if got[i] != tc.want[i] {
				t.Errorf("height %d drew %q, want %q", tc.height, got, tc.want)
				break
			}
		}
	}
}

// TestDrawTopOrientation hangs the bars from the other edge, with the glyph
// set that fills a cell from the top.
func TestDrawTopOrientation(t *testing.T) {
	screen := newTestScreen(t, 4, 4)
	layout := Layout{Cols: 4, Rows: 4, Bars: 1, BarWidth: 1, BarSpacing: 0}
	r, err := New(layout, Top, Style{Foreground: "default", Background: "default"})
	if err != nil {
		t.Fatal(err)
	}

	r.Draw(screen, []int{12})
	screen.Show()
	got := column(screen, 0, 4)
	want := []string{"█", "▀", " ", " "}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("top orientation drew %q, want %q", got, want)
		}
	}
}

// TestDrawBarWidthAndSpacing checks the geometry: every cell of a wide bar
// carries the same glyph, and the gap between bars stays empty.
func TestDrawBarWidthAndSpacing(t *testing.T) {
	screen := newTestScreen(t, 12, 2)
	layout := Layout{Cols: 12, Rows: 2, Bars: 3, BarWidth: 3, BarSpacing: 1, Offset: 0}
	r, err := New(layout, Bottom, Style{Foreground: "default", Background: "default"})
	if err != nil {
		t.Fatal(err)
	}
	r.Draw(screen, []int{8, 0, 8})
	screen.Show()

	bottom := make([]string, 12)
	for x := 0; x < 12; x++ {
		bottom[x], _, _ = screen.Get(x, 1)
	}
	got := strings.Join(bottom, "")
	// Three cells of bar, one of gap, three of nothing, one of gap, three of
	// bar, and one cell left over.
	want := "███" + " " + "   " + " " + "███" + " "
	if got != want {
		t.Errorf("bottom row is %q, want %q", got, want)
	}
}

// TestDrawOffsetCenters checks that the left margin is honored, which is what
// center_align does.
func TestDrawOffsetCenters(t *testing.T) {
	screen := newTestScreen(t, 10, 1)
	layout := Layout{Cols: 10, Rows: 1, Bars: 2, BarWidth: 2, BarSpacing: 1, Offset: 3}
	r, err := New(layout, Bottom, Style{Foreground: "default", Background: "default"})
	if err != nil {
		t.Fatal(err)
	}
	r.Draw(screen, []int{8, 8})
	screen.Show()
	for x := 0; x < 3; x++ {
		if str, _, _ := screen.Get(x, 0); str != " " && str != "" {
			t.Errorf("column %d is %q, expected the left margin to be blank", x, str)
		}
	}
	if str, _, _ := screen.Get(3, 0); str != "█" {
		t.Errorf("the first bar starts at column %d, not 3", 3)
	}
}

// TestComputeLayout covers the bar-count arithmetic, which is the one piece of
// this that is easy to get subtly wrong and hard to see.
func TestComputeLayout(t *testing.T) {
	for _, tc := range []struct {
		name                    string
		cols, fixed, width, gap int
		stereo, center          bool
		wantBars, wantOffset    int
	}{
		{name: "fills the width", cols: 80, width: 2, gap: 1, wantBars: 27, wantOffset: 0},
		{name: "stereo rounds down to even", cols: 80, width: 2, gap: 1, stereo: true, wantBars: 26, wantOffset: 0},
		{name: "centered leaves a margin", cols: 80, width: 2, gap: 1, stereo: true, center: true, wantBars: 26, wantOffset: 1},
		{name: "one cell each", cols: 10, width: 1, gap: 0, wantBars: 10, wantOffset: 0},
		{name: "fixed count", cols: 80, fixed: 10, width: 2, gap: 1, wantBars: 10, wantOffset: 0},
		{name: "a single column", cols: 1, width: 1, gap: 0, wantBars: 1, wantOffset: 0},
		{name: "a single column in stereo", cols: 1, width: 1, gap: 0, stereo: true, wantBars: 2, wantOffset: 0},
	} {
		l, err := ComputeLayout(tc.cols, 24, tc.fixed, tc.width, tc.gap, tc.stereo, tc.center)
		if err != nil {
			t.Errorf("%s: %v", tc.name, err)
			continue
		}
		if l.Bars != tc.wantBars {
			t.Errorf("%s: %d bars, want %d", tc.name, l.Bars, tc.wantBars)
		}
		if l.Offset != tc.wantOffset {
			t.Errorf("%s: offset %d, want %d", tc.name, l.Offset, tc.wantOffset)
		}
	}
}

// TestComputeLayoutRejectsTheImpossible: cava refuses rather than drawing
// fewer bars than were asked for, and says what would fit.
func TestComputeLayoutRejectsTheImpossible(t *testing.T) {
	if _, err := ComputeLayout(10, 24, 100, 2, 1, false, false); err == nil {
		t.Error("100 bars were accepted in 10 columns")
	}
	if _, err := ComputeLayout(80, 24, 11, 2, 1, true, false); err == nil {
		t.Error("an odd bar count was accepted for stereo")
	}
	if _, err := ComputeLayout(80, 24, 1, 2, 1, true, false); err == nil {
		t.Error("a single bar was accepted for stereo")
	}
}

// TestGradientInterpolates checks the color ramp: it starts on the first
// stop, ends exactly on the last, and moves monotonically between them.
func TestGradientInterpolates(t *testing.T) {
	colors, err := interpolate([]string{"#000000", "#ffffff"}, 8)
	if err != nil {
		t.Fatal(err)
	}
	if colors[0] != color.NewRGBColor(0, 0, 0) {
		r, g, b := colors[0].RGB()
		t.Errorf("the first step is %d,%d,%d, want black", r, g, b)
	}
	if colors[7] != color.NewRGBColor(255, 255, 255) {
		r, g, b := colors[7].RGB()
		t.Errorf("the last step is %d,%d,%d, want white", r, g, b)
	}
	last := int32(-1)
	for i, c := range colors {
		r, _, _ := c.RGB()
		if r < last {
			t.Errorf("step %d is darker than the one before it", i)
		}
		last = r
	}
}

func TestGradientRejectsBadInput(t *testing.T) {
	if _, err := interpolate([]string{"#000000"}, 8); err == nil {
		t.Error("a one-color gradient was accepted")
	}
	if _, err := interpolate([]string{"#1", "#2", "#3", "#4", "#5", "#6", "#7", "#8", "#9"}, 8); err == nil {
		t.Error("a nine-color gradient was accepted")
	}
	if _, err := interpolate([]string{"red", "blue"}, 8); err == nil {
		t.Error("named colors were accepted in a gradient, which cannot be interpolated")
	}
	if _, err := interpolate([]string{"#000000", "#ffffff"}, 0); err == nil {
		t.Error("a gradient over zero steps was accepted")
	}
}

// TestGradientIsAppliedByRow: the whole point of the vertical gradient is that
// the color depends on how high up the bar a cell is, not on which bar it is.
func TestGradientIsAppliedByRow(t *testing.T) {
	screen := newTestScreen(t, 2, 4)
	layout := Layout{Cols: 2, Rows: 4, Bars: 2, BarWidth: 1, BarSpacing: 0}
	r, err := New(layout, Bottom, Style{
		Foreground:     "default",
		Background:     "default",
		Gradient:       true,
		GradientColors: []string{"#000000", "#ff0000"},
	})
	if err != nil {
		t.Fatal(err)
	}
	r.Draw(screen, []int{32, 32})
	screen.Show()

	var reds []int32
	for y := 3; y >= 0; y-- {
		_, style, _ := screen.Get(0, y)
		red, _, _ := style.GetForeground().RGB()
		reds = append(reds, red)

		// The second bar in the same row must be the same color.
		_, other, _ := screen.Get(1, y)
		if other.GetForeground() != style.GetForeground() {
			t.Errorf("row %d: the two bars are different colors", y)
		}
	}
	if reds[0] >= reds[len(reds)-1] {
		t.Errorf("the gradient runs %v from the bottom up; it should get redder", reds)
	}
}

// TestHorizontalGradientIsAppliedByBar is the mirror of the test above.
func TestHorizontalGradientIsAppliedByBar(t *testing.T) {
	screen := newTestScreen(t, 4, 2)
	layout := Layout{Cols: 4, Rows: 2, Bars: 4, BarWidth: 1, BarSpacing: 0}
	r, err := New(layout, Bottom, Style{
		Foreground:         "default",
		Background:         "default",
		HorizontalGradient: true,
		HorizontalColors:   []string{"#000000", "#ff0000"},
	})
	if err != nil {
		t.Fatal(err)
	}
	r.Draw(screen, []int{16, 16, 16, 16})
	screen.Show()

	var reds []int32
	for x := 0; x < 4; x++ {
		_, style, _ := screen.Get(x, 0)
		red, _, _ := style.GetForeground().RGB()
		reds = append(reds, red)

		_, other, _ := screen.Get(x, 1)
		if other.GetForeground() != style.GetForeground() {
			t.Errorf("bar %d is a different color in its two rows", x)
		}
	}
	if reds[0] >= reds[3] {
		t.Errorf("the gradient runs %v left to right; it should get redder", reds)
	}
}

func TestParseColor(t *testing.T) {
	if c, err := parseColor("default"); err != nil || c != color.Default {
		t.Errorf("default = %v, %v", c, err)
	}
	if c, err := parseColor("#010203"); err != nil || c != color.NewRGBColor(1, 2, 3) {
		t.Errorf("#010203 = %v, %v", c, err)
	}
	for _, s := range []string{"#fff", "burgundy", "#gggggg"} {
		if _, err := parseColor(s); err == nil {
			t.Errorf("%q was accepted", s)
		}
	}
}

func TestFormatFrequency(t *testing.T) {
	for _, tc := range []struct {
		hz   float32
		want string
	}{
		{50, "50"},
		{999, "999"},
		{1000, "1k"},
		{1500, "1.5k"},
		{16000, "16k"},
	} {
		if got := formatFrequency(tc.hz); got != tc.want {
			t.Errorf("%v Hz formatted as %q, want %q", tc.hz, got, tc.want)
		}
	}
}

// TestDrawXAxis writes labels where they fit and nowhere else.
func TestDrawXAxis(t *testing.T) {
	screen := newTestScreen(t, 20, 2)
	layout := Layout{Cols: 20, Rows: 1, Bars: 10, BarWidth: 1, BarSpacing: 1}
	r, err := New(layout, Bottom, Style{Foreground: "default", Background: "default"})
	if err != nil {
		t.Fatal(err)
	}
	cutoffs := []float32{50, 100, 200, 400, 800, 1600, 3200, 6400, 8000, 10000, 12000}
	r.DrawXAxis(screen, 1, cutoffs)
	screen.Show()

	row := make([]string, 20)
	for x := 0; x < 20; x++ {
		row[x], _, _ = screen.Get(x, 1)
	}
	got := strings.Join(row, "")
	if !strings.HasPrefix(got, "50") {
		t.Errorf("the axis starts with %q, want the first cut-off", got)
	}
	if strings.Contains(got, "5050") {
		t.Errorf("labels ran into each other: %q", got)
	}
}

// TestRendererDoesNotOwnTheScreen is the discipline that lets the same drawing
// code run inside a host that owns the screen already: nothing here may call
// Init or Fini, and a screen that has been finalized elsewhere is not this
// package's business.
func TestRendererDoesNotOwnTheScreen(t *testing.T) {
	screen := simscreen.NewScreen()
	if err := screen.Init(); err != nil {
		t.Fatal(err)
	}
	screen.SetSize(8, 4)

	layout := Layout{Cols: 8, Rows: 4, Bars: 4, BarWidth: 1, BarSpacing: 1}
	r, err := New(layout, Bottom, Style{Foreground: "default", Background: "default"})
	if err != nil {
		t.Fatal(err)
	}
	r.Clear(screen)
	r.Draw(screen, []int{8, 16, 24, 32})
	screen.Show()

	// The caller finalizes it, because the caller made it.
	screen.Fini()
}
