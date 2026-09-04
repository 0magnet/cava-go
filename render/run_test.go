package render

import (
	"math"
	"testing"
	"time"

	"github.com/gdamore/tcell/v3"

	"github.com/0magnet/cava-go"
	"github.com/0magnet/cava-go/internal/simscreen"
)

// TestRunDrawsATone is the end-to-end test: a tone goes into a stream, the
// whole pipeline runs, and bars appear on a screen. It is the only test that
// exercises the loop, the plan, the shaper and the renderer together, which is
// where the wiring mistakes live — a channel count passed where a bar count
// belongs draws nothing, and every unit test still passes.
func TestRunDrawsATone(t *testing.T) {
	const rate = 44100

	cfg := cava.DefaultConfig()
	cfg.Framerate = 200
	cfg.DrawAndQuit = 400
	cfg.SampleRate = rate
	cfg.InputChannels = 1
	cfg.Channels = "mono"
	cfg.Bars = 10
	cfg.BarWidth = 2
	cfg.BarSpacing = 1
	cfg.CenterAlign = false
	cfg.ShowIdleBarHeads = false
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}

	stream := cava.NewStream(16384)
	stop := make(chan struct{})
	defer close(stop)

	// Keep the stream fed at roughly real time, as a fifo would.
	go func() {
		buf := make([]float64, 512)
		phase := 0
		for {
			select {
			case <-stop:
				return
			default:
			}
			for n := range buf {
				buf[n] = math.Sin(2*math.Pi*440/rate*float64(phase+n)) * 20000
			}
			phase += len(buf)
			stream.Write(buf)
			time.Sleep(2 * time.Millisecond)
		}
	}()

	screen := simscreen.NewScreen()
	if err := screen.Init(); err != nil {
		t.Fatal(err)
	}
	defer screen.Fini()
	screen.SetSize(40, 10)

	done := make(chan error, 1)
	go func() { done <- Run(screen, cfg, stream, stop) }()

	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("Run did not stop after its frame count")
	}

	// Something must have been drawn, and it must be a block glyph rather than
	// stray text.
	blocks := 0
	for y := 0; y < 10; y++ {
		for x := 0; x < 40; x++ {
			str, _, _ := screen.Get(x, y)
			for _, b := range bottomBlocks {
				if str == b {
					blocks++
				}
			}
		}
	}
	if blocks == 0 {
		t.Error("a 440 Hz tone drew no bars at all")
	}
}

// TestRunStopsOnRequest: the loop must come back when it is told to, or
// quitting hangs.
func TestRunStopsOnRequest(t *testing.T) {
	cfg := cava.DefaultConfig()
	cfg.InputChannels = 1
	cfg.Channels = "mono"
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}

	screen := simscreen.NewScreen()
	if err := screen.Init(); err != nil {
		t.Fatal(err)
	}
	defer screen.Fini()
	screen.SetSize(40, 10)

	stop := make(chan struct{})
	done := make(chan error, 1)
	go func() { done <- Run(screen, cfg, cava.NewStream(16384), stop) }()

	time.Sleep(50 * time.Millisecond)
	close(stop)

	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run ignored the stop channel")
	}
}

// TestRunQuitsOnQ covers the key handling, which is the only reason the loop
// reads events at all.
func TestRunQuitsOnQ(t *testing.T) {
	cfg := cava.DefaultConfig()
	cfg.InputChannels = 1
	cfg.Channels = "mono"
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}

	screen := simscreen.NewScreen()
	if err := screen.Init(); err != nil {
		t.Fatal(err)
	}
	defer screen.Fini()
	screen.SetSize(40, 10)

	stop := make(chan struct{})
	defer close(stop)
	done := make(chan error, 1)
	go func() { done <- Run(screen, cfg, cava.NewStream(16384), stop) }()

	time.Sleep(50 * time.Millisecond)
	screen.EventQ() <- tcell.NewEventKey(tcell.KeyRune, "q", tcell.ModNone)

	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not quit on 'q'")
	}
}

// TestRunRejectsAnImpossibleLayout: a fixed bar count that will not fit has to
// be an error from the loop, not a screen full of nothing.
func TestRunRejectsAnImpossibleLayout(t *testing.T) {
	cfg := cava.DefaultConfig()
	cfg.InputChannels = 1
	cfg.Channels = "mono"
	cfg.Bars = 500
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}

	screen := simscreen.NewScreen()
	if err := screen.Init(); err != nil {
		t.Fatal(err)
	}
	defer screen.Fini()
	screen.SetSize(20, 10)

	stop := make(chan struct{})
	defer close(stop)
	if err := Run(screen, cfg, cava.NewStream(16384), stop); err == nil {
		t.Error("500 bars in 20 columns was accepted")
	}
}
