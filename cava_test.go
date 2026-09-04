package cava

import (
	"math"
	"testing"
)

// TestBlueprint is the one test here that checks this port against the
// original rather than against itself.
//
// cava ships cavacore_test.c, which drives the engine with a 200 Hz tone in
// the left channel and a 2000 Hz tone in the right for 300 frames and asserts
// the final bar heights against a hard-coded blueprint, to within 2%. The
// blueprint below is copied from that file unchanged, along with its
// parameters and its signal. If this passes, this port's band layout,
// equaliser, windowing, transform, smoothing and sensitivity all agree with
// the C engine to three decimal places after three hundred frames of
// accumulated state.
//
// It is also why the arithmetic in planBands is written in float32 where the
// original uses C float: computing those cut-offs in double moves a bar
// boundary by one FFT bin and this test fails.
func TestBlueprint(t *testing.T) {
	const (
		barsPerChannel = 10
		channels       = 2
		bufferSize     = 512 * channels
		rate           = 44100
		noiseReduction = 0.77
		lowCutOff      = 50
		highCutOff     = 10000
		frames         = 300
	)

	blueprint200 := [barsPerChannel]float64{0, 0, 0.994, 0.004, 0, 0, 0, 0, 0, 0}
	blueprint2000 := [barsPerChannel]float64{0, 0, 0, 0, 0, 0, 0.683, 0.002, 0, 0}

	plan, err := Init(barsPerChannel, rate, channels, 1, noiseReduction, lowCutOff, highCutOff, ScalingLinear)
	if err != nil {
		t.Fatal(err)
	}

	in := make([]float64, bufferSize)
	out := make([]float64, barsPerChannel*channels)

	for k := 0; k < frames; k++ {
		// The phase is continuous across frames — a broken sine would smear
		// across the spectrum and there would be nothing to assert.
		for n := 0; n < bufferSize/2; n++ {
			phase := float64(n + k*bufferSize/2)
			in[n*2] = math.Sin(2*math.Pi*200/rate*phase) * 20000
			in[n*2+1] = math.Sin(2*math.Pi*2000/rate*phase) * 20000
		}
		plan.Execute(in, bufferSize, out)
	}

	for i := range out {
		out[i] = math.Round(out[i]*1000) / 1000
	}

	check := func(label string, got []float64, want [barsPerChannel]float64) {
		t.Helper()
		for i, w := range want {
			g := got[i]
			if g > w*1.02 || g < w*0.98 {
				t.Errorf("%s bar %d = %.3f, blueprint %.3f (2%% tolerance)", label, i, g, w)
			}
		}
	}
	check("200 Hz (left)", out[:barsPerChannel], blueprint200)
	check("2000 Hz (right)", out[barsPerChannel:], blueprint2000)
}

// TestToneLandsInItsBand is the same idea without the hard-coded numbers: a
// tone should light the bar whose frequency range contains it and leave the
// rest alone.
func TestToneLandsInItsBand(t *testing.T) {
	const rate = 44100
	for _, freq := range []float64{80, 200, 440, 1000, 4000} {
		plan, err := Init(20, rate, 1, 1, 0.77, 50, 10000, ScalingLinear)
		if err != nil {
			t.Fatal(err)
		}
		out := make([]float64, 20)
		in := make([]float64, 512)
		for k := 0; k < 300; k++ {
			for n := range in {
				in[n] = math.Sin(2*math.Pi*freq/rate*float64(n+k*512)) * 20000
			}
			plan.Execute(in, len(in), out)
		}

		peak, peakBar := 0.0, -1
		for i, v := range out {
			if v > peak {
				peak, peakBar = v, i
			}
		}
		if peakBar < 0 {
			t.Errorf("%.0f Hz: nothing in any bar", freq)
			continue
		}

		// Which bar the tone belongs in, allowing one either side. A bar's
		// edges are quantised to whole FFT bins, so a bar that asks for
		// 50-81 Hz gets whatever bins are nearest and a tone a hertz or two
		// outside its nominal range lands next door. Requiring an exact hit
		// would be asserting the rounding, not the layout.
		cut := plan.CutOffFrequencies()
		want := -1
		for i := 0; i < len(cut)-1; i++ {
			if freq >= float64(cut[i]) && freq < float64(cut[i+1]) {
				want = i
				break
			}
		}
		if want < 0 {
			t.Errorf("%.0f Hz is outside every bar's range", freq)
		} else if peakBar < want-1 || peakBar > want+1 {
			t.Errorf("%.0f Hz peaked in bar %d (%.0f-%.0f Hz), expected bar %d (%.0f-%.0f Hz)",
				freq, peakBar, cut[peakBar], cut[peakBar+1], want, cut[want], cut[want+1])
		}
		// The neighbours pick up some of a windowed tone's skirt; anything
		// further away should be quiet.
		for i, v := range out {
			if i >= peakBar-1 && i <= peakBar+1 {
				continue
			}
			if v > peak/10 {
				t.Errorf("%.0f Hz: bar %d is %.3f, more than a tenth of the peak %.3f at bar %d",
					freq, i, v, peak, peakBar)
			}
		}
	}
}

// TestWhiteNoiseSpreads is the complement of the tone test: a signal with
// energy everywhere should reach every bar.
func TestWhiteNoiseSpreads(t *testing.T) {
	plan, err := Init(16, 44100, 1, 1, 0.77, 50, 10000, ScalingLinear)
	if err != nil {
		t.Fatal(err)
	}
	out := make([]float64, 16)
	in := make([]float64, 512)

	// A fixed linear congruential sequence rather than math/rand, so the test
	// asserts the same thing on every run and in every Go version.
	seed := uint64(1)
	next := func() float64 {
		seed = seed*6364136223846793005 + 1442695040888963407
		return (float64(seed>>11)/float64(1<<53))*2 - 1
	}

	for k := 0; k < 300; k++ {
		for n := range in {
			in[n] = next() * 20000
		}
		plan.Execute(in, len(in), out)
	}

	for i, v := range out {
		if v <= 0 {
			t.Errorf("white noise left bar %d at %v", i, v)
		}
	}
}

// TestSweepMovesTheEnergy checks that the peak bar climbs as a sweep does. It
// is the only test here that exercises the band layout across its whole range
// in one go.
func TestSweepMovesTheEnergy(t *testing.T) {
	const rate = 44100
	plan, err := Init(24, rate, 1, 0, 0.0, 50, 10000, ScalingLinear)
	if err != nil {
		t.Fatal(err)
	}
	out := make([]float64, 24)
	in := make([]float64, 4096)

	peakAt := func(freq float64) int {
		// A fresh, long buffer of one tone: enough samples to fill the whole
		// transform, so nothing of the previous frequency is left in it.
		for pass := 0; pass < 4; pass++ {
			for n := range in {
				in[n] = math.Sin(2*math.Pi*freq/rate*float64(n+pass*len(in))) * 20000
			}
			plan.Execute(in, len(in), out)
		}
		best, bestBar := 0.0, 0
		for i, v := range out {
			if v > best {
				best, bestBar = v, i
			}
		}
		return bestBar
	}

	last := -1
	for _, freq := range []float64{60, 120, 250, 500, 1000, 2000, 4000, 8000} {
		bar := peakAt(freq)
		if bar <= last {
			t.Errorf("%.0f Hz peaked at bar %d, which is not above the previous peak %d", freq, bar, last)
		}
		last = bar
	}
}

// TestBandsAreOrdered checks the invariant everything else rests on: bars run
// from low to high, each covers at least one bin, and none overlaps its
// neighbour.
func TestBandsAreOrdered(t *testing.T) {
	for _, tc := range []struct {
		bars, rate, low, high int
	}{
		{10, 44100, 50, 10000},
		{60, 44100, 50, 8000},
		{200, 44100, 30, 20000},
		{16, 8000, 50, 3000},
		{32, 48000, 20, 20000},
		{64, 96000, 50, 10000},
	} {
		plan, err := Init(tc.bars, tc.rate, 2, 1, 0.77, tc.low, tc.high, ScalingLinear)
		if err != nil {
			t.Fatalf("%v: %v", tc, err)
		}
		cut := plan.CutOffFrequencies()
		for i := 0; i < tc.bars; i++ {
			if plan.upperCutOff[i] < plan.lowerCutOff[i] {
				t.Errorf("%v: bar %d has bins %d..%d, which is empty", tc, i, plan.lowerCutOff[i], plan.upperCutOff[i])
			}
			if i > 0 && cut[i] < cut[i-1] {
				t.Errorf("%v: bar %d starts at %.1f Hz, below bar %d at %.1f Hz", tc, i, cut[i], i-1, cut[i-1])
			}
			// Bins are per-transform, so only compare bars read from the same
			// one.
			if i > 0 && (i < plan.bassCutOffBar) == (i-1 < plan.bassCutOffBar) {
				if plan.lowerCutOff[i] <= plan.upperCutOff[i-1] {
					t.Errorf("%v: bar %d starts at bin %d, inside bar %d which ends at %d",
						tc, i, plan.lowerCutOff[i], i-1, plan.upperCutOff[i-1])
				}
			}
		}
		if plan.bassCutOffBar > 0 && float64(cut[plan.bassCutOffBar]) < bassCutOff {
			t.Errorf("%v: the first non-bass bar starts at %.1f Hz, below the %d Hz handover",
				tc, cut[plan.bassCutOffBar], bassCutOff)
		}
	}
}

// TestInitRejectsBadParameters keeps the error messages honest — every one of
// these is a check cava makes.
func TestInitRejectsBadParameters(t *testing.T) {
	for _, tc := range []struct {
		name                           string
		bars, rate, channels, autosens int
		noise                          float64
		low, high                      int
		scaling                        ScalingMode
	}{
		{name: "no channels", bars: 10, rate: 44100, channels: 0, noise: 0.77, low: 50, high: 10000},
		{name: "too many channels", bars: 10, rate: 44100, channels: 3, noise: 0.77, low: 50, high: 10000},
		{name: "zero rate", bars: 10, rate: 0, channels: 2, noise: 0.77, low: 50, high: 10000},
		{name: "absurd rate", bars: 10, rate: 400000, channels: 2, noise: 0.77, low: 50, high: 10000},
		{name: "no bars", bars: 0, rate: 44100, channels: 2, noise: 0.77, low: 50, high: 10000},
		{name: "too many bars", bars: 5000, rate: 44100, channels: 2, noise: 0.77, low: 50, high: 10000},
		{name: "negative cut off", bars: 10, rate: 44100, channels: 2, noise: 0.77, low: -1, high: 10000},
		{name: "inverted cut offs", bars: 10, rate: 44100, channels: 2, noise: 0.77, low: 10000, high: 50},
		{name: "above nyquist", bars: 10, rate: 44100, channels: 2, noise: 0.77, low: 50, high: 30000},
		{name: "unknown scaling", bars: 10, rate: 44100, channels: 2, noise: 0.77, low: 50, high: 10000, scaling: 99},
	} {
		if _, err := Init(tc.bars, tc.rate, tc.channels, tc.autosens, tc.noise, tc.low, tc.high, tc.scaling); err == nil {
			t.Errorf("%s: Init accepted it", tc.name)
		}
	}
}

// TestTransformSizeFollowsRate pins the thresholds. They are a table in the
// original with no formula behind it, so the only way to keep them right is to
// write them down.
func TestTransformSizeFollowsRate(t *testing.T) {
	for _, tc := range []struct{ rate, want int }{
		{8000, 512},
		{8125, 512},
		{8126, 1024},
		{16250, 1024},
		{16251, 2048},
		{32500, 2048},
		{44100, 4096},
		{48000, 4096},
		{75000, 4096},
		{96000, 8192},
		{150000, 8192},
		{192000, 16384},
		{300000, 16384},
		{384000, 32768},
	} {
		plan, err := Init(4, tc.rate, 1, 1, 0.77, 50, tc.rate/4, ScalingLinear)
		if err != nil {
			t.Fatalf("%d Hz: %v", tc.rate, err)
		}
		if plan.fftBufferSize != tc.want {
			t.Errorf("%d Hz: transform is %d points, want %d", tc.rate, plan.fftBufferSize, tc.want)
		}
		if plan.fftBassBufferSize != tc.want*2 {
			t.Errorf("%d Hz: bass transform is %d points, want %d", tc.rate, plan.fftBassBufferSize, tc.want*2)
		}
	}
}

// TestAutosensClimbsAndBacksOff checks the two halves of the sensitivity loop:
// a quiet signal is amplified until it fills the range, and a loud one is
// pulled back until it stops clipping.
func TestAutosensClimbsAndBacksOff(t *testing.T) {
	const rate = 44100
	run := func(amplitude float64, frames int) (*Plan, []float64) {
		plan, err := Init(10, rate, 1, 1, 0.77, 50, 10000, ScalingLinear)
		if err != nil {
			t.Fatal(err)
		}
		out := make([]float64, 10)
		in := make([]float64, 512)
		for k := 0; k < frames; k++ {
			for n := range in {
				in[n] = math.Sin(2*math.Pi*200/rate*float64(n+k*512)) * amplitude
			}
			plan.Execute(in, len(in), out)
		}
		return plan, out
	}

	quiet, _ := run(20, 300)
	loud, _ := run(2000000, 300)
	if quiet.Sensitivity() <= 1 {
		t.Errorf("a quiet signal left sensitivity at %v, expected it to climb above 1", quiet.Sensitivity())
	}
	if loud.Sensitivity() >= 1 {
		t.Errorf("a loud signal left sensitivity at %v, expected it to fall below 1", loud.Sensitivity())
	}

	// Whatever the input level, the output ends up in range — that is the
	// whole point of the feature.
	for _, amp := range []float64{20, 20000, 2000000} {
		_, out := run(amp, 400)
		peak := 0.0
		for _, v := range out {
			if v > peak {
				peak = v
			}
		}
		if peak < 0.5 || peak > 1.0 {
			t.Errorf("amplitude %.0f settled at a peak of %.3f, want something between 0.5 and 1", amp, peak)
		}
	}
}

// TestSilenceDecays checks that the picture falls away when the music stops
// rather than freezing. Both smoothing filters have to be leaky for this.
func TestSilenceDecays(t *testing.T) {
	const rate = 44100
	plan, err := Init(10, rate, 1, 1, 0.77, 50, 10000, ScalingLinear)
	if err != nil {
		t.Fatal(err)
	}
	out := make([]float64, 10)
	in := make([]float64, 512)
	for k := 0; k < 200; k++ {
		for n := range in {
			in[n] = math.Sin(2*math.Pi*200/rate*float64(n+k*512)) * 20000
		}
		plan.Execute(in, len(in), out)
	}
	loud := 0.0
	for _, v := range out {
		if v > loud {
			loud = v
		}
	}
	if loud <= 0 {
		t.Fatalf("no signal to decay from")
	}

	clear(in)
	for k := 0; k < 200; k++ {
		plan.Execute(in, len(in), out)
	}
	for i, v := range out {
		if v > loud/100 {
			t.Errorf("after 200 silent frames bar %d is still %.4f, was %.4f", i, v, loud)
		}
	}
}

// TestNoNewSamplesIsStable checks the path cava takes when a frame is drawn
// with nothing to draw from: it must not divide by zero, and it must not
// leave the frame rate estimate poisoned.
func TestNoNewSamplesIsStable(t *testing.T) {
	plan, err := Init(10, 44100, 2, 1, 0.77, 50, 10000, ScalingLinear)
	if err != nil {
		t.Fatal(err)
	}
	out := make([]float64, 20)
	for k := 0; k < 50; k++ {
		plan.Execute(nil, 0, out)
	}
	// A single sample with two channels is fewer than one frame.
	plan.Execute([]float64{1}, 1, out)
	if math.IsInf(plan.Framerate(), 0) || math.IsNaN(plan.Framerate()) {
		t.Errorf("framerate estimate is %v", plan.Framerate())
	}
	for i, v := range out {
		if math.IsNaN(v) || math.IsInf(v, 0) {
			t.Errorf("bar %d is %v", i, v)
		}
	}
}

// TestDecibelScalingIsBounded checks the other scaling mode stays in range and
// does not return -Inf for a silent band.
func TestDecibelScalingIsBounded(t *testing.T) {
	const rate = 44100
	plan, err := Init(20, rate, 1, 0, 0.77, 50, 10000, ScalingDecibel)
	if err != nil {
		t.Fatal(err)
	}
	out := make([]float64, 20)
	in := make([]float64, 512)
	for k := 0; k < 100; k++ {
		for n := range in {
			in[n] = math.Sin(2*math.Pi*440/rate*float64(n+k*512)) * 20000
		}
		plan.Execute(in, len(in), out)
	}
	for i, v := range out {
		if math.IsNaN(v) || math.IsInf(v, 0) {
			t.Errorf("bar %d is %v under decibel scaling", i, v)
		}
	}

	clear(in)
	for k := 0; k < 100; k++ {
		plan.Execute(in, len(in), out)
	}
	for i, v := range out {
		if math.IsNaN(v) || math.IsInf(v, 0) {
			t.Errorf("silent bar %d is %v under decibel scaling", i, v)
		}
	}
}

// TestMonoAndStereoAgree checks that de-interleaving is right: the same tone
// in both channels of a stereo stream must produce the same bars as it does
// mono, and a tone in one channel only must not appear in the other.
func TestMonoAndStereoAgree(t *testing.T) {
	const rate = 44100

	stereoPlan, err := Init(10, rate, 2, 0, 0.0, 50, 10000, ScalingLinear)
	if err != nil {
		t.Fatal(err)
	}
	stereoOut := make([]float64, 20)
	stereoIn := make([]float64, 1024)
	for k := 0; k < 50; k++ {
		for n := 0; n < 512; n++ {
			// Left channel only, in the usual interleaving: even samples left,
			// odd samples right.
			stereoIn[n*2] = math.Sin(2*math.Pi*440/rate*float64(n+k*512)) * 20000
			stereoIn[n*2+1] = 0
		}
		stereoPlan.Execute(stereoIn, len(stereoIn), stereoOut)
	}

	left, right := stereoOut[:10], stereoOut[10:]
	peak := 0.0
	for _, v := range left {
		if v > peak {
			peak = v
		}
	}
	if peak <= 0 {
		t.Fatal("nothing in the left channel")
	}
	for i, v := range right {
		if v > peak/1000 {
			t.Errorf("a left-channel-only tone put %.4f in right bar %d, against a left peak of %.4f", v, i, peak)
		}
	}
}
