package cava

import (
	"math"
	"testing"
)

// TestMonstercatStepResponse drives the filter with a single tall bar among
// zeros — the step response of a spatial filter — and checks the skirt it
// leaves against the closed form.
//
// For the exponential shape the closed form is exact on both sides: a bar d
// away from the peak comes out at peak / (monstercat*1.5)^d. The upward pass
// cannot raise it any further, because spreading from an already-divided
// neighbour reproduces exactly the same number.
func TestMonstercatStepResponse(t *testing.T) {
	const (
		n          = 11
		spike      = 8
		monstercat = 1.5
	)
	bars := make([]float64, n)
	bars[5] = spike

	MonstercatFilter(bars, 0, monstercat, 40)

	if bars[5] != spike {
		t.Errorf("the peak moved: %v, want %v", bars[5], spike)
	}
	for i := 0; i < n; i++ {
		d := math.Abs(float64(i - 5))
		want := spike / math.Pow(monstercat*1.5, d)
		if math.Abs(bars[i]-want) > 1e-12 {
			t.Errorf("bar %d = %v, want %v", i, bars[i], want)
		}
	}
}

// TestMonstercatIsWiderWhenWeaker pins the direction of the parameter, which
// is the opposite of what the name suggests: a larger monstercat divides by
// more per bar and so spreads less far.
func TestMonstercatIsWiderWhenWeaker(t *testing.T) {
	width := func(m float64) float64 {
		bars := make([]float64, 11)
		bars[5] = 100
		MonstercatFilter(bars, 0, m, 40)
		return bars[8]
	}
	if width(1.0) <= width(3.0) {
		t.Errorf("monstercat 1.0 reached %v three bars out and 3.0 reached %v; the smaller value should spread further",
			width(1.0), width(3.0))
	}
}

// TestWavesStepResponse checks the parabolic shape. The peak is cut to 80% of
// itself first, and the bars below it — which the pass reaches before it has
// touched them — fall off as the square of the distance.
func TestWavesStepResponse(t *testing.T) {
	const (
		n     = 11
		spike = 100.0
	)
	bars := make([]float64, n)
	bars[5] = spike

	MonstercatFilter(bars, 1, 0, 40)

	if math.Abs(bars[5]-spike/1.25) > 1e-12 {
		t.Errorf("peak = %v, want %v", bars[5], spike/1.25)
	}
	for d := 1; d <= 5; d++ {
		want := math.Max(spike/1.25-float64(d*d), 0)
		if got := bars[5-d]; math.Abs(got-want) > 1e-12 {
			t.Errorf("bar %d below the peak = %v, want %v", 5-d, got, want)
		}
	}
	// The bars above the peak are cut again as the pass walks over them, so
	// they sit below the parabola rather than on it. That asymmetry is in the
	// original, and this pins it rather than pretending it is not there.
	for d := 1; d <= 5; d++ {
		parabola := math.Max(spike/1.25-float64(d*d), 0)
		if got := bars[5+d]; got > parabola+1e-12 {
			t.Errorf("bar %d above the peak = %v, which is above the parabola %v", 5+d, got, parabola)
		}
	}
}

// TestWavesWidensOnALargeSurface checks the height normaliser: on a pixel
// display the parabola has to be much wider in absolute terms to look the same
// as it does in a terminal.
func TestWavesWidensOnALargeSurface(t *testing.T) {
	run := func(height int) []float64 {
		bars := make([]float64, 21)
		bars[10] = 5000
		MonstercatFilter(bars, 1, 0, height)
		return bars
	}
	terminal := run(40)
	pixels := run(2000)
	// Two bars out, the pixel surface has lost more, because its parabola is
	// scaled by height/912.76 rather than by 1.
	if pixels[8] >= terminal[8] {
		t.Errorf("on a 2000-unit surface bar 8 is %v and on a 40-unit one it is %v; the large surface should fall off faster in absolute terms",
			pixels[8], terminal[8])
	}
}

// TestFilterOffIsANoOp checks that leaving both settings at zero — cava's
// default — changes nothing at all.
func TestFilterOffIsANoOp(t *testing.T) {
	bars := []float64{1, 5, 2, 9, 0, 3}
	want := append([]float64(nil), bars...)
	MonstercatFilter(bars, 0, 0, 40)
	for i := range bars {
		if bars[i] != want[i] {
			t.Errorf("bar %d changed from %v to %v with the filter off", i, want[i], bars[i])
		}
	}
}

// TestWavesWinsOverMonstercat pins the precedence: with both set, cava runs
// waves and ignores the other.
func TestWavesWinsOverMonstercat(t *testing.T) {
	both := make([]float64, 9)
	both[4] = 100
	MonstercatFilter(both, 1, 1.5, 40)

	waves := make([]float64, 9)
	waves[4] = 100
	MonstercatFilter(waves, 1, 0, 40)

	for i := range both {
		if both[i] != waves[i] {
			t.Fatalf("with both set bar %d is %v; with waves alone it is %v", i, both[i], waves[i])
		}
	}
}

// TestFilterNeverLowersABar is the property the filter is for: it fills in
// around peaks and must not eat into anything that was already there. The one
// exception is the deliberate cut waves makes to the bar it is spreading from.
func TestFilterNeverLowersABar(t *testing.T) {
	in := []float64{3, 1, 7, 2, 0, 9, 4, 1}
	got := append([]float64(nil), in...)
	MonstercatFilter(got, 0, 2, 40)
	for i := range in {
		if got[i] < in[i]-1e-12 {
			t.Errorf("bar %d fell from %v to %v", i, in[i], got[i])
		}
	}
}
