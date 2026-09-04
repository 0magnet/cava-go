package render

import (
	"encoding/binary"
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/gdamore/tcell/v3"

	"github.com/0magnet/cava-go"
	"github.com/0magnet/cava-go/internal/simscreen"
)

// writeTone writes a sine tone as interleaved signed 16-bit little endian PCM
// — the format every example in the README produces — and returns the path.
func writeTone(t *testing.T, freq float64, rate, channels int, seconds float64) string { //nolint:unparam // the frequency is part of what the helper says
	t.Helper()
	frames := int(float64(rate) * seconds)
	buf := make([]byte, frames*channels*2)
	for i := 0; i < frames; i++ {
		v := uint16(int16(math.Sin(2*math.Pi*freq/float64(rate)*float64(i)) * 20000)) //nolint:gosec // deliberate
		for c := 0; c < channels; c++ {
			binary.LittleEndian.PutUint16(buf[(i*channels+c)*2:], v)
		}
	}
	path := filepath.Join(t.TempDir(), "tone.raw")
	if err := os.WriteFile(path, buf, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestRunDrawsAToneFromARealFile is the terminal half of the regression test
// in the root package: PCM in a file, through the decoder and the reader and
// the stream and the engine, and out as block glyphs on a screen.
//
// The version of this test that came before it wrote samples straight into the
// stream, and so passed throughout the period when the binary drew nothing at
// all for a real file. A test that skips the input path cannot see a fault in
// the input path.
func TestRunDrawsAToneFromARealFile(t *testing.T) {
	const (
		rate     = 44100
		channels = 2
	)
	path := writeTone(t, 440, rate, channels, 0.5)

	cfg := cava.DefaultConfig()
	cfg.SampleRate = rate
	cfg.InputChannels = channels
	cfg.Source = path
	cfg.DrawAndQuit = 45
	cfg.Bars = 10
	cfg.BarWidth = 2
	cfg.BarSpacing = 1
	cfg.CenterAlign = false
	// Idle heads would draw a row of stubs whether or not any audio arrived,
	// which is exactly the thing this test must not mistake for success.
	cfg.ShowIdleBarHeads = false
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}

	stream := cava.NewStream(16384)
	stop := make(chan struct{})
	defer close(stop)
	go cava.PumpSource(stream, path, cava.Input{
		Format:       cfg.Format(),
		Rate:         rate,
		Channels:     channels,
		FrameSamples: 512 * channels,
	}, stop)

	screen := simscreen.NewScreen()
	if err := screen.Init(); err != nil {
		t.Fatal(err)
	}
	defer screen.Fini()
	screen.SetSize(40, 10)

	// Watch every frame rather than only the last one. The bars fall away once
	// the tone ends, so the final frame of a short clip can legitimately be
	// empty; what must not happen is that no frame ever had anything in it.
	drawn := make(chan int, 1)
	watching := make(chan struct{})
	go func() {
		best := 0
		for {
			select {
			case <-watching:
				drawn <- best
				return
			case <-time.After(5 * time.Millisecond):
			}
			n := 0
			for y := 0; y < 10; y++ {
				for x := 0; x < 40; x++ {
					str, _, _ := screen.Get(x, y)
					for _, b := range bottomBlocks {
						if str == b {
							n++
						}
					}
				}
			}
			if n > best {
				best = n
			}
		}
	}()

	done := make(chan error, 1)
	go func() { done <- Run(screen, cfg, stream, stop) }()

	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("Run did not stop after its frame count")
	}
	close(watching)

	if best := <-drawn; best == 0 {
		t.Error("a 440 Hz tone read from a file drew no bars at all")
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
