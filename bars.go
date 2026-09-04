package cava

// Shaper turns the engine's output into the integer bar heights that get
// drawn.
//
// Everything between [Plan.Execute] and the screen lives here: manual
// sensitivity, the scale to the height of the output, the user equaliser, the
// monstercat filter, and the arrangement of two channels into one row of bars.
// It is separate from the drawing so that a terminal, a raw stream and a
// browser canvas can share it.
type Shaper struct {
	// Bars is the number of bars drawn, counting both channels.
	Bars int
	// PlanBars is the number of bars per channel, which is what the plan was
	// built for.
	PlanBars int
	// AudioChannels is 1 or 2, matching the plan.
	AudioChannels int
	// OutputChannels is 2 when the channels are drawn separately.
	OutputChannels int

	// Height is full scale for a bar, in the units the output wants: eighths
	// of a cell for a terminal, or the format's full range for raw output.
	Height float64
	// FilterHeight is the height handed to the monstercat filter, which uses
	// it only to decide whether the surface is a terminal or a pixel display.
	FilterHeight int

	// Sensitivity is the manual gain, applied on top of whatever the plan's
	// autosens is doing. 1.0 is unchanged.
	Sensitivity float64

	// Monstercat and Waves configure [MonstercatFilter]. Monstercat of 0
	// disables both.
	Monstercat float64
	Waves      int

	// UserEQ is an arbitrary number of gain stops spread over the bars. Empty
	// disables it.
	UserEQ []float64

	// MonoOption is "left", "right" or "average" and decides what a single row
	// of bars shows when the input is stereo.
	MonoOption string
	// Reverse draws the spectrum the other way round.
	Reverse bool
	// ShowIdleBarHeads keeps a one-eighth stub visible under a silent bar, so
	// the display reads as idle rather than as broken.
	ShowIdleBarHeads bool

	left  []float64
	right []float64
	raw   []float64
}

// NewShaper returns a shaper sized for a layout. bars is the total drawn,
// planBars the number per channel.
func NewShaper(bars, planBars, audioChannels, outputChannels int) *Shaper {
	s := &Shaper{
		Bars:           bars,
		PlanBars:       planBars,
		AudioChannels:  audioChannels,
		OutputChannels: outputChannels,
		Sensitivity:    1,
		MonoOption:     "average",
	}
	s.left = make([]float64, planBars)
	s.right = make([]float64, planBars)
	s.raw = make([]float64, bars)
	return s
}

// RawBars is how many values [Shaper.Shape] reads from the engine's output.
func (s *Shaper) RawBars() int { return s.PlanBars * s.AudioChannels }

// Shape converts one frame of engine output into bar heights, writing
// s.Bars values into dst. out is modified in place.
func (s *Shaper) Shape(out []float64, dst []int) {
	n := s.RawBars()
	if n > len(out) {
		n = len(out)
	}

	// Manual sensitivity, then clamp, then scale to the output's height. The
	// clamp comes before the scale so that a bar can never be drawn off the
	// end of the screen, however far autosens has drifted.
	for i := 0; i < n; i++ {
		out[i] *= s.Sensitivity
		if out[i] > 1.0 {
			out[i] = 1.0
		} else if out[i] < 0.0 {
			out[i] = 0.0
		}
		out[i] *= s.Height
	}

	// The user equaliser. Its keys are spread evenly over the bars whatever
	// their number, so three keys over sixty bars is a three-band tone
	// control and sixty keys is a per-bar one.
	eqAt := func(i int) float64 {
		if len(s.UserEQ) == 0 {
			return 1
		}
		idx := i * len(s.UserEQ) / s.PlanBars
		if idx >= len(s.UserEQ) {
			idx = len(s.UserEQ) - 1
		}
		return s.UserEQ[idx]
	}

	if s.AudioChannels == 2 {
		for i := 0; i < s.PlanBars; i++ {
			s.left[i] = out[i] * eqAt(i)
			s.right[i] = out[i+s.PlanBars] * eqAt(i)
		}
		if s.Monstercat > 0 || s.Waves > 0 {
			MonstercatFilter(s.left, s.Waves, s.Monstercat, s.FilterHeight)
			MonstercatFilter(s.right, s.Waves, s.Monstercat, s.FilterHeight)
		}
		s.combine()
	} else {
		for i := 0; i < s.Bars && i < s.PlanBars; i++ {
			s.raw[i] = out[i] * eqAt(i)
		}
		if s.Monstercat > 0 || s.Waves > 0 {
			MonstercatFilter(s.raw, s.Waves, s.Monstercat, s.FilterHeight)
		}
		if s.Reverse {
			for i, j := 0, len(s.raw)-1; i < j; i, j = i+1, j-1 {
				s.raw[i], s.raw[j] = s.raw[j], s.raw[i]
			}
		}
	}

	for i := 0; i < s.Bars && i < len(dst); i++ {
		v := int(s.raw[i])
		// A bar that has fallen to nothing is drawn as a sliver rather than as
		// an empty column, which is what stops a quiet passage from looking
		// like a crash.
		if s.ShowIdleBarHeads && v < 1 {
			v = 1
		}
		dst[i] = v
	}
}

// combine lays two channels of bars into one row.
//
// In stereo the left channel occupies the left half reversed and the right
// channel the right half forwards, so the lowest frequencies of both meet in
// the middle and the display is symmetric about it. In mono the two are mixed
// down by MonoOption.
func (s *Shaper) combine() {
	half := s.Bars / 2
	if s.OutputChannels == 2 {
		for n := 0; n < s.Bars; n++ {
			if n < half {
				if s.Reverse {
					s.raw[n] = s.left[n]
				} else {
					s.raw[n] = s.left[half-n-1]
				}
			} else {
				if s.Reverse {
					s.raw[n] = s.right[s.Bars-n-1]
				} else {
					s.raw[n] = s.right[n-half]
				}
			}
		}
		return
	}

	for n := 0; n < s.Bars && n < s.PlanBars; n++ {
		var v float64
		switch s.MonoOption {
		case "left":
			v = s.left[n]
		case "right":
			v = s.right[n]
		default:
			v = (s.left[n] + s.right[n]) / 2
		}
		if s.Reverse {
			s.raw[s.Bars-n-1] = v
		} else {
			s.raw[n] = v
		}
	}
}

// FrameBudget decides how many samples one frame should consume, given how
// many are waiting.
//
// The two cases are cava's. When the drawing loop is slower than the reader's
// own frame — the usual arrangement, 60 fps against 512-sample reads — it
// simply takes everything that has arrived, and the reader's pacing is what
// keeps that from being the whole track at once. When the loop is faster, it
// has to ration: take a frame's worth, take less if that is all there is, and
// take the excess in one go if the reader has got far enough ahead that
// draining it a frame at a time would never catch up.
//
// available, samplesPerFrame and readerFrame are all counted in samples across
// every channel.
func FrameBudget(available, samplesPerFrame, readerFrame int) int {
	if samplesPerFrame >= readerFrame {
		// Slower than the reader: no rationing needed.
		return available
	}
	use := samplesPerFrame
	if available < use {
		// Underrun. Use what there is.
		use = available
	}
	if available > readerFrame+use {
		// Overrun. The reader got ahead, so take the excess in one go rather
		// than falling further behind every frame.
		use = available - readerFrame
	}
	return use
}
