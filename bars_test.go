package cava

import "testing"

// TestShapeScalesAndClamps: whatever the engine produces, a bar must land
// between nothing and the height of the display. Autosens drifts, and a bar
// drawn past the top of the screen is a crash in some outputs and a mess in
// the rest.
func TestShapeScalesAndClamps(t *testing.T) {
	s := NewShaper(4, 4, 1, 1)
	s.Height = 80 // ten rows of eight
	dst := make([]int, 4)

	s.Shape([]float64{0, 0.5, 1.0, 3.0}, dst)
	want := []int{0, 40, 80, 80}
	for i := range want {
		if dst[i] != want[i] {
			t.Errorf("bar %d = %d, want %d", i, dst[i], want[i])
		}
	}

	s.Shape([]float64{-1, -0.5, 0, 0}, dst)
	for i, v := range dst {
		if v != 0 {
			t.Errorf("a negative bar %d came out at %d", i, v)
		}
	}
}

// TestShapeIdleBarHeads: with the option on, a silent bar is a sliver rather
// than an empty column.
func TestShapeIdleBarHeads(t *testing.T) {
	s := NewShaper(3, 3, 1, 1)
	s.Height = 80
	s.ShowIdleBarHeads = true
	dst := make([]int, 3)
	s.Shape([]float64{0, 0, 0}, dst)
	for i, v := range dst {
		if v != 1 {
			t.Errorf("idle bar %d = %d, want 1", i, v)
		}
	}

	s.ShowIdleBarHeads = false
	s.Shape([]float64{0, 0, 0}, dst)
	for i, v := range dst {
		if v != 0 {
			t.Errorf("with idle heads off bar %d = %d, want 0", i, v)
		}
	}
}

// TestShapeStereoMirrors is the arrangement that makes cava look like cava:
// the left channel runs outwards to the left and the right channel outwards to
// the right, so the bass of both meets in the middle.
func TestShapeStereoMirrors(t *testing.T) {
	s := NewShaper(8, 4, 2, 2)
	s.Height = 100
	dst := make([]int, 8)

	// Left channel 1..4 by frequency, right channel 5..8.
	out := []float64{0.01, 0.02, 0.03, 0.04, 0.05, 0.06, 0.07, 0.08}
	s.Shape(out, dst)

	want := []int{4, 3, 2, 1, 5, 6, 7, 8}
	for i := range want {
		if dst[i] != want[i] {
			t.Fatalf("stereo bars = %v, want %v", dst, want)
		}
	}

	// Reversed, the two halves swap the direction they run in.
	s.Reverse = true
	s.Shape([]float64{0.01, 0.02, 0.03, 0.04, 0.05, 0.06, 0.07, 0.08}, dst)
	wantRev := []int{1, 2, 3, 4, 8, 7, 6, 5}
	for i := range wantRev {
		if dst[i] != wantRev[i] {
			t.Fatalf("reversed stereo bars = %v, want %v", dst, wantRev)
		}
	}
}

// TestShapeMonoOptions covers the three ways two channels become one row.
func TestShapeMonoOptions(t *testing.T) {
	for _, tc := range []struct {
		option string
		want   []int
	}{
		{"average", []int{30, 40}},
		{"left", []int{10, 20}},
		{"right", []int{50, 60}},
	} {
		s := NewShaper(2, 2, 2, 1)
		s.Height = 100
		s.MonoOption = tc.option
		dst := make([]int, 2)
		s.Shape([]float64{0.1, 0.2, 0.5, 0.6}, dst)
		for i := range tc.want {
			if dst[i] != tc.want[i] {
				t.Errorf("mono_option %s gave %v, want %v", tc.option, dst, tc.want)
				break
			}
		}
	}
}

// TestShapeUserEQ spreads a handful of gain stops over however many bars there
// are, which is what makes an eq section written for one terminal width work
// at another.
func TestShapeUserEQ(t *testing.T) {
	s := NewShaper(6, 6, 1, 1)
	s.Height = 100
	s.UserEQ = []float64{0, 1, 2}
	dst := make([]int, 6)
	s.Shape([]float64{0.5, 0.5, 0.5, 0.5, 0.5, 0.5}, dst)

	want := []int{0, 0, 50, 50, 100, 100}
	for i := range want {
		if dst[i] != want[i] {
			t.Fatalf("with a three-stop eq over six bars: %v, want %v", dst, want)
		}
	}
}

// TestShapeAppliesTheFilter checks that the monstercat filter is wired in and
// runs per channel, not across the join between them — spreading energy from
// the left channel's treble into the right channel's would be a bug that only
// showed up as a smear in the middle of the screen.
func TestShapeAppliesTheFilter(t *testing.T) {
	s := NewShaper(8, 4, 2, 2)
	s.Height = 1000
	s.Monstercat = 1.5
	dst := make([]int, 8)

	// One tall bar at the top of the left channel's range, nothing anywhere
	// else. After mirroring it sits at index 0.
	s.Shape([]float64{0, 0, 0, 1.0, 0, 0, 0, 0}, dst)

	if dst[0] != 1000 {
		t.Fatalf("the peak is %d, want 1000; bars = %v", dst[0], dst)
	}
	if dst[1] == 0 {
		t.Errorf("the filter did not spread into the next bar: %v", dst)
	}
	// The right half is a different channel and must be untouched.
	for i := 4; i < 8; i++ {
		if dst[i] != 0 {
			t.Errorf("the filter reached across into the right channel: %v", dst)
			break
		}
	}
}

// TestShapeSensitivity is the manual gain the arrow keys drive.
func TestShapeSensitivity(t *testing.T) {
	s := NewShaper(1, 1, 1, 1)
	s.Height = 100
	s.Sensitivity = 2
	dst := make([]int, 1)
	s.Shape([]float64{0.25}, dst)
	if dst[0] != 50 {
		t.Errorf("with sensitivity 2 a bar of 0.25 came out at %d, want 50", dst[0])
	}
}
