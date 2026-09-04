package cava

import (
	"io"
	"time"
)

// RunRaw is cava's `raw` output method: bar heights written to a stream for
// something else to draw, rather than a picture on a terminal.
//
// It lives here rather than in the command because it is the whole pipeline —
// stream, engine, shaper, output — and a pipeline that cannot be called from a
// test is a pipeline nobody tests. There is no screen, so the bar count is
// fixed rather than derived from a width; cava hard-codes 512 per channel and
// so does this.
//
// It returns when stop closes, when the configured frame count is reached, or
// when the reader filling stream reports an error.
func RunRaw(w io.Writer, cfg *Config, stream *Stream, stop <-chan struct{}) error {
	outputChannels := cfg.OutputChannels()
	audioChannels := cfg.InputChannels
	if audioChannels == 1 {
		// One channel cannot be drawn as two mirrored halves.
		outputChannels = 1
	}

	numberOfBars := cfg.Bars
	if numberOfBars <= 0 {
		numberOfBars = 512 * outputChannels
	}
	if outputChannels == 2 && numberOfBars%2 != 0 {
		numberOfBars--
	}

	out := NewRawWriter(w, cfg.DataFormat == "binary", cfg.BitFormat,
		cfg.AsciiMaxRange, cfg.BarDelimiter, cfg.FrameDelimiter)

	plan, err := Init(numberOfBars/outputChannels, cfg.SampleRate, audioChannels,
		cfg.Autosens, cfg.NoiseReduction, cfg.LowerCutOff, cfg.HigherCutOff, cfg.Scaling)
	if err != nil {
		return err
	}

	shaper := NewShaper(numberOfBars, plan.Bars(), audioChannels, outputChannels)
	shaper.Height = float64(out.FullScale())
	shaper.FilterHeight = out.FullScale()
	shaper.Sensitivity = cfg.Sensitivity / 100
	shaper.Monstercat = cfg.Monstercat
	shaper.Waves = cfg.Waves
	shaper.UserEQ = cfg.UserEQ
	shaper.MonoOption = cfg.MonoOption
	shaper.Reverse = cfg.Reverse
	// Idle heads are a drawing trick; a consumer of raw values wants a zero to
	// mean zero.
	shaper.ShowIdleBarHeads = false

	cavaOut := make([]float64, plan.Bars()*audioChannels)
	heights := make([]int, numberOfBars)
	take := make([]float64, plan.InputBufferSize())

	readerFrame := 512 * audioChannels
	samplesPerFrame := (cfg.SampleRate / cfg.Framerate) * audioChannels

	ticker := time.NewTicker(time.Second / time.Duration(cfg.Framerate))
	defer ticker.Stop()

	frames := 0
	for {
		select {
		case <-stop:
			return nil
		case <-ticker.C:
		}

		n, err := stream.Take(take, FrameBudget(stream.Available(), samplesPerFrame, readerFrame))
		if err != nil {
			return err
		}
		plan.Execute(take[:n], n, cavaOut)
		shaper.Shape(cavaOut, heights)
		if err := out.WriteFrame(heights); err != nil {
			return err
		}

		frames++
		if cfg.DrawAndQuit > 0 && frames >= cfg.DrawAndQuit {
			return nil
		}
	}
}
