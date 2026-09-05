package render

import (
	"time"

	"github.com/gdamore/tcell/v3"

	"github.com/0magnet/cava-go"
)

// Run draws a visualization on screen until the user quits, stop closes, or
// the configured frame count is reached.
//
// It does not call Init or Fini and it does not create the screen: the screen
// belongs to the caller. That is what lets the same loop run in a terminal and
// inside a browser pane where something else owns the screen, and it is the
// discipline termanim keeps for the same reason.
//
// stream is filled by whatever is reading audio — see [cava.PumpSource]. Run
// never blocks on it; a frame with no new samples is still drawn, which is
// what makes the bars fall away when the music stops rather than freeze.
func Run(screen tcell.Screen, cfg *cava.Config, stream *cava.Stream, stop <-chan struct{}) error {
	audioChannels := cfg.InputChannels
	outputChannels := cfg.OutputChannels()
	if audioChannels == 1 {
		// One channel cannot be drawn as two mirrored halves. cava reads past
		// the end of its own buffer here; refusing instead is the only honest
		// option.
		outputChannels = 1
	}

	orientation := Bottom
	if cfg.Orientation == "top" {
		orientation = Top
	}
	xaxis := cfg.XAxis == "frequency"

	// A frame's worth of samples at the configured rate, against the reader's
	// own frame. Which of the two is larger decides whether this loop has to
	// ration what it takes; see [cava.FrameBudget].
	readerFrame := 512 * audioChannels
	samplesPerFrame := (cfg.SampleRate / cfg.Framerate) * audioChannels

	ticker := time.NewTicker(time.Second / time.Duration(cfg.Framerate))
	defer ticker.Stop()

	events := screen.EventQ()

	var (
		plan     *cava.Plan
		shaper   *cava.Shaper
		renderer *Renderer
		out      []float64
		bars     []int
		take     []float64
		cols     int
		rows     int
	)

	// build lays out the screen and plans the engine for it. It runs before
	// the first frame and again on every resize, which is the only time any of
	// this is allocated.
	build := func() error {
		w, h := screen.Size()
		drawRows := h
		if xaxis {
			drawRows--
		}
		if drawRows < 1 {
			drawRows = 1
		}

		layout, err := ComputeLayout(w, drawRows, cfg.Bars, cfg.BarWidth, cfg.BarSpacing,
			outputChannels == 2, cfg.CenterAlign)
		if err != nil {
			return err
		}

		plan, err = cava.Init(layout.Bars/outputChannels, cfg.SampleRate, audioChannels,
			cfg.Autosens, cfg.NoiseReduction, cfg.LowerCutOff, cfg.HigherCutOff, cfg.Scaling)
		if err != nil {
			return err
		}

		renderer, err = New(layout, orientation, Style{
			Foreground:         cfg.Foreground,
			Background:         cfg.Background,
			Gradient:           cfg.Gradient,
			GradientColors:     cfg.GradientColors,
			HorizontalGradient: cfg.HorizontalGradient,
			HorizontalColors:   cfg.HorizontalColors,
			BlendDirection:     cfg.BlendDirection,
		})
		if err != nil {
			return err
		}

		shaper = cava.NewShaper(layout.Bars, plan.Bars(), audioChannels, outputChannels)
		// A cell is eight units tall, and max_height gives back a fraction of
		// the screen.
		shaper.Height = float64(layout.Rows) * 8 * cfg.MaxHeight
		shaper.FilterHeight = layout.Rows
		shaper.Sensitivity = cfg.Sensitivity / 100
		shaper.Monstercat = cfg.Monstercat
		shaper.Waves = cfg.Waves
		shaper.UserEQ = cfg.UserEQ
		shaper.MonoOption = cfg.MonoOption
		shaper.Reverse = cfg.Reverse
		shaper.ShowIdleBarHeads = cfg.ShowIdleBarHeads

		out = make([]float64, plan.Bars()*audioChannels)
		bars = make([]int, layout.Bars)
		take = make([]float64, plan.InputBufferSize())
		cols, rows = w, h

		screen.Clear()
		renderer.Clear(screen)
		return nil
	}

	if err := build(); err != nil {
		return err
	}

	frames := 0
	silentFrames := 0

	for {
		select {
		case <-stop:
			return nil
		case ev := <-events:
			switch ev := ev.(type) {
			case *tcell.EventResize:
				if err := build(); err != nil {
					return err
				}
			case *tcell.EventKey:
				switch ev.Key() {
				case tcell.KeyEscape, tcell.KeyCtrlC:
					return nil
				case tcell.KeyUp:
					// The same keys cava binds: a nudge either way on top of
					// whatever autosens has settled on.
					shaper.Sensitivity *= 1.05
				case tcell.KeyDown:
					shaper.Sensitivity *= 0.95
				case tcell.KeyRight:
					cfg.BarWidth++
					if err := build(); err != nil {
						return err
					}
				case tcell.KeyLeft:
					if cfg.BarWidth > 1 {
						cfg.BarWidth--
						if err := build(); err != nil {
							return err
						}
					}
				case tcell.KeyRune:
					switch ev.Str() {
					case "q", "Q":
						return nil
					case "r", "R":
						if err := build(); err != nil {
							return err
						}
					}
				}
			}
			continue

		case <-ticker.C:
		}

		// A resize can also arrive as a size change with no event, which is
		// what happens when the host owns the screen.
		if w, h := screen.Size(); w != cols || h != rows {
			if err := build(); err != nil {
				return err
			}
		}

		n, err := stream.Take(take, cava.FrameBudget(stream.Available(), samplesPerFrame, readerFrame))
		if err != nil {
			return err
		}

		plan.Execute(take[:n], n, out)
		shaper.Shape(out, bars)

		renderer.Draw(screen, bars)
		if xaxis {
			row := rows - 1
			if orientation == Top {
				row = 0
			}
			renderer.DrawXAxis(screen, row, plan.CutOffFrequencies())
		}
		screen.Show()

		frames++
		if cfg.DrawAndQuit > 0 && frames >= cfg.DrawAndQuit {
			return nil
		}

		// The sleep timer. cava stops transforming after a configured run of
		// silence and only wakes on input; here the loop keeps running but at
		// one frame a second, which costs the same and needs no second path
		// through the code.
		if cfg.SleepTimer > 0 {
			if silent(bars, cfg.ShowIdleBarHeads) {
				silentFrames++
			} else {
				silentFrames = 0
			}
			if silentFrames > cfg.SleepTimer*cfg.Framerate {
				select {
				case <-stop:
					return nil
				case <-time.After(time.Second):
				}
			}
		}
	}
}

// silent reports whether every bar is at rest. With idle bar heads on, at rest
// is one eighth rather than nothing.
func silent(bars []int, idleHeads bool) bool {
	floor := 0
	if idleHeads {
		floor = 1
	}
	for _, v := range bars {
		if v > floor {
			return false
		}
	}
	return true
}
