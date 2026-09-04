// Package simscreen builds a tcell screen for tests to draw into and read back.
//
// tcell v2 shipped a SimulationScreen for exactly this and v3 does not. What v3
// has instead is better: a mock terminal in tcell/v3/vt that speaks the real
// protocol, so a screen built on it is an ordinary terminal screen rather than a
// separate implementation that might not behave like one. The same helper is
// in termanim, for the same reason.
//
// The screen's own Get reports the logical contents — what will be shown when
// Show is called — which is what SimulationScreen.GetContents reported, so the
// assertions read the same as they would against a real terminal. Reading the mock
// terminal's cells instead would test the emulator as well as the drawing,
// and would mean converting its colors back out of image/color to compare
// them.
package simscreen

import (
	"github.com/gdamore/tcell/v3"
	"github.com/gdamore/tcell/v3/vt"
)

// Cell is one cell of the screen, shaped like the SimCell these tests used to
// read.
type Cell struct {
	Runes []rune
	Style tcell.Style
}

// NewScreen returns a screen backed by a mock terminal, ready to be Init'd and
// sized by the caller — the way tcell v2's simulation screen was used, so the
// tests read as they would have against one.
//
// It panics rather than returning an error. There is no recovering from not
// being able to make a screen in a test, and the alternative is an error check
// at every call site that only ever reports a broken build.
//
// The mock terminal is told it has truecolor because cava's gradients use it.
// Without that tcell resolves every color down to the sixteen the default
// terminfo advertises, and a test that compares colors reads back grey.
func NewScreen() tcell.Screen {
	mt := vt.NewMockTerm(
		vt.MockOptSize{X: 80, Y: 25},
		vt.MockOptColors(1<<24),
	)
	s, err := tcell.NewTerminfoScreenFromTty(mt)
	if err != nil {
		panic("simscreen: " + err.Error())
	}
	return s
}

// Contents reads the whole screen, row-major, with its dimensions — the shape
// SimulationScreen.GetContents returned.
func Contents(s tcell.Screen) ([]Cell, int, int) {
	w, h := s.Size()
	cells := make([]Cell, 0, w*h)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			str, style, _ := s.Get(x, y)
			cells = append(cells, Cell{Runes: []rune(str), Style: style})
		}
	}
	return cells, w, h
}
