package cava

import (
	"bytes"
	"encoding/binary"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// writeTone writes seconds of a sine tone as interleaved signed 16-bit little
// endian PCM — the format every example in the README produces — and returns
// the path.
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

// TestRawOutputFromARealFile is the regression test for the defect that made
// this port emit frames of nothing for perfectly good audio.
//
// It is deliberately not a core-level test. Every unit test in this package
// passed while the binary drew silence, because each one handed the engine
// samples directly. This one starts where a user starts — PCM in a file — and
// goes through the decoder, the reader, the stream and the whole raw pipeline,
// which is where the two faults were:
//
//   - the reader consumed the entire file in a few milliseconds, so the audio
//     was over before the first frame was drawn;
//   - and on reaching the end it filled the buffer with silence, overwriting
//     the samples that had arrived and not yet been drawn.
//
// Either one alone gives a run of frames with every bar at zero, which is
// exactly what this asserts against.
func TestRawOutputFromARealFile(t *testing.T) {
	const (
		rate     = 44100
		channels = 2
		bars     = 16
	)
	path := writeTone(t, 440, rate, channels, 0.5)

	cfg := DefaultConfig()
	cfg.InputMethod = "fifo"
	cfg.Source = path
	cfg.SampleRate = rate
	cfg.InputChannels = channels
	cfg.Bars = bars
	cfg.OutputMethod = "raw"
	cfg.DataFormat = "ascii"
	cfg.DrawAndQuit = 45
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}

	stream := NewStream(16384)
	stop := make(chan struct{})
	defer close(stop)
	go PumpSource(stream, path, Input{
		Format:       cfg.Format(),
		Rate:         rate,
		Channels:     channels,
		FrameSamples: 512 * channels,
	}, stop)

	var out bytes.Buffer
	if err := RunRaw(&out, cfg, stream, stop); err != nil {
		t.Fatal(err)
	}

	frames := strings.Split(strings.TrimSuffix(out.String(), "\n"), "\n")
	if len(frames) < 10 {
		t.Fatalf("only %d frames were written", len(frames))
	}

	live, peak, peakBar := 0, 0, -1
	for _, frame := range frames {
		for i, field := range strings.Split(strings.TrimSuffix(frame, ";"), ";") {
			v, err := strconv.Atoi(field)
			if err != nil {
				t.Fatalf("frame %q is not ascii bar heights: %v", frame, err)
			}
			if v > 0 {
				live++
			}
			if v > peak {
				peak, peakBar = v, i
			}
		}
	}

	if live == 0 {
		t.Fatalf("a 440 Hz tone through the real input path produced %d frames and not one non-zero bar", len(frames))
	}
	// Not just non-zero: the tone has to reach a usable fraction of full
	// scale, or the display is technically alive and visually dead.
	if peak < 100 {
		t.Errorf("the tallest bar over the whole run was %d of %d", peak, cfg.AsciiMaxRange)
	}
	// 440 Hz belongs in the left half, since the left channel is drawn
	// outwards to the left from the middle.
	if peakBar >= bars/2 {
		t.Errorf("the peak landed in bar %d of %d, which is the right channel's half", peakBar, bars)
	}
}

// TestStdinPathFromARealFile is the same journey down the other input path,
// which is the one the bug was reported against.
func TestStdinPathFromARealFile(t *testing.T) {
	const (
		rate     = 44100
		channels = 2
	)
	path := writeTone(t, 440, rate, channels, 0.4)
	f, err := os.Open(path) //nolint:gosec // the test's own temp file
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close() //nolint:errcheck

	cfg := DefaultConfig()
	cfg.InputMethod = "stdin"
	cfg.SampleRate = rate
	cfg.InputChannels = channels
	cfg.Bars = 16
	cfg.OutputMethod = "raw"
	cfg.DataFormat = "ascii"
	cfg.DrawAndQuit = 36
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}

	stream := NewStream(16384)
	stop := make(chan struct{})
	defer close(stop)
	// Pump rather than PumpSource: standard input is a stream that is opened
	// once and cannot be reopened, which is what the command does with it.
	go func() {
		_ = Pump(stream, f, Input{ //nolint:errcheck // the file simply ends
			Format:       cfg.Format(),
			Rate:         rate,
			Channels:     channels,
			FrameSamples: 512 * channels,
		}, stop)
		stream.Silence()
	}()

	var out bytes.Buffer
	if err := RunRaw(&out, cfg, stream, stop); err != nil {
		t.Fatal(err)
	}

	if !strings.ContainsFunc(out.String(), func(r rune) bool { return r >= '1' && r <= '9' }) {
		t.Fatal("every bar of every frame was zero on the stdin path")
	}
}

// TestReaderPacesToTheSampleRate is the property that makes the tests above
// pass, stated on its own so that a regression in it is reported as what it is
// rather than as a display that has gone dark.
//
// A file hands over as fast as the disk allows. Everything downstream — the
// engine's frame rate estimate, the smoothing constants derived from it, and a
// drawing loop that takes whatever has accumulated — assumes audio arrives at
// the speed it is played, so the reader is what has to make that true.
func TestReaderPacesToTheSampleRate(t *testing.T) {
	const (
		rate     = 44100
		channels = 2
		seconds  = 0.4
	)
	path := writeTone(t, 440, rate, channels, seconds)
	f, err := os.Open(path) //nolint:gosec // the test's own temp file
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close() //nolint:errcheck

	// A stream big enough for the whole tone, so that nothing is dropped and
	// the only thing being measured is how long delivery took.
	stream := NewStream(int(rate*seconds)*channels + 4096)

	start := time.Now()
	err = Pump(stream, f, Input{
		Format:       Format16,
		Rate:         rate,
		Channels:     channels,
		FrameSamples: 512 * channels,
	}, nil)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("Pump did not report the end of the file")
	}
	want := time.Duration(seconds * float64(time.Second))
	if elapsed < want*8/10 {
		t.Errorf("%v of audio was delivered in %v; the reader is not pacing to the sample rate", want, elapsed)
	}
	if elapsed > want*2 {
		t.Errorf("%v of audio took %v to deliver, which is far slower than it plays", want, elapsed)
	}
}

// TestUnpacedIsOptedInto: pacing is what a real source needs and a nuisance in
// a test that only wants to push samples through.
func TestUnpacedIsOptedInto(t *testing.T) {
	path := writeTone(t, 440, 44100, 2, 0.4)
	f, err := os.Open(path) //nolint:gosec // the test's own temp file
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close() //nolint:errcheck

	stream := NewStream(44100*2 + 4096)
	start := time.Now()
	_ = Pump(stream, f, Input{ //nolint:errcheck // the file simply ends
		Format:       Format16,
		Rate:         44100,
		Channels:     2,
		FrameSamples: 1024,
		Unpaced:      true,
	}, nil)
	if elapsed := time.Since(start); elapsed > 100*time.Millisecond {
		t.Errorf("an unpaced read of 0.4s of audio took %v", elapsed)
	}
}

// TestSilenceKeepsUndrawnAudio is the second fault on its own. A source that
// delivers faster than the frame rate always has samples in hand when it
// reaches its end, and zeroing the buffer at that moment threw them away — the
// difference between a track that fades out and a track that was never seen.
func TestSilenceKeepsUndrawnAudio(t *testing.T) {
	s := NewStream(8)
	s.Write([]float64{1, 2, 3})
	s.Silence()

	dst := make([]float64, 8)
	n, err := s.Take(dst, 0)
	if err != nil {
		t.Fatal(err)
	}
	if n != 8 {
		t.Fatalf("Silence left %d samples, want a full buffer", n)
	}
	for i, want := range []float64{1, 2, 3, 0, 0, 0, 0, 0} {
		if dst[i] != want {
			t.Fatalf("after Silence the buffer is %v, want the audio kept and the rest zeroed", dst[:n])
		}
	}
}

// TestFrameBudget covers the rationing both drawing loops share.
func TestFrameBudget(t *testing.T) {
	for _, tc := range []struct {
		name                                    string
		available, samplesPerFrame, readerFrame int
		want                                    int
	}{
		// 60 fps at 44100 stereo: a frame wants more than the reader delivers
		// at once, so there is nothing to ration.
		{"slower than the reader", 2048, 1470, 1024, 2048},
		{"slower than the reader, empty", 0, 1470, 1024, 0},
		// 200 fps: a frame wants less than one read, so it takes its share.
		{"faster than the reader, overrun", 2048, 440, 1024, 1024},
		{"faster, nothing waiting", 0, 440, 1024, 0},
		{"faster, underrun", 200, 440, 1024, 200},
		{"faster, steady", 1200, 440, 1024, 440},
	} {
		if got := FrameBudget(tc.available, tc.samplesPerFrame, tc.readerFrame); got != tc.want {
			t.Errorf("%s: FrameBudget(%d, %d, %d) = %d, want %d",
				tc.name, tc.available, tc.samplesPerFrame, tc.readerFrame, got, tc.want)
		}
	}
}

// TestDisplayFallsAwayWhenTheSourceEnds is the other half of the same claim.
// Drawing nothing for real audio was the reported fault; drawing the last
// fragment of a finished track for ever is its mirror image, and one fix can
// easily produce the other.
//
// With no new samples the engine has nothing to shift into its history, so it
// transforms the same buffer over and over and the bars sit exactly where they
// were. The reader is what has to keep feeding it silence once the source has
// gone.
func TestDisplayFallsAwayWhenTheSourceEnds(t *testing.T) {
	const (
		rate     = 44100
		channels = 2
	)
	path := writeTone(t, 440, rate, channels, 0.3)
	f, err := os.Open(path) //nolint:gosec // the test's own temp file
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close() //nolint:errcheck

	cfg := DefaultConfig()
	cfg.SampleRate = rate
	cfg.InputChannels = channels
	cfg.Bars = 16
	cfg.OutputMethod = "raw"
	cfg.DataFormat = "ascii"
	cfg.DrawAndQuit = 150
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}

	stream := NewStream(16384)
	stop := make(chan struct{})
	defer close(stop)
	// The stdin arrangement: read once, then keep the picture fading.
	go func() {
		_ = Pump(stream, f, Input{ //nolint:errcheck // the file simply ends
			Format:       cfg.Format(),
			Rate:         rate,
			Channels:     channels,
			FrameSamples: 512 * channels,
		}, stop)
		quiet(stream, stop)
	}()

	var out bytes.Buffer
	if err := RunRaw(&out, cfg, stream, stop); err != nil {
		t.Fatal(err)
	}

	frames := strings.Split(strings.TrimSuffix(out.String(), "\n"), "\n")
	if len(frames) < 50 {
		t.Fatalf("only %d frames were written", len(frames))
	}

	// It has to have drawn something first, or this would pass on the very
	// bug the tests above exist for.
	if !strings.ContainsFunc(strings.Join(frames[:40], ""), func(r rune) bool { return r >= '1' && r <= '9' }) {
		t.Fatal("the tone never drew anything, so there is nothing to fall away from")
	}

	last := frames[len(frames)-1]
	for _, field := range strings.Split(strings.TrimSuffix(last, ";"), ";") {
		v, err := strconv.Atoi(field)
		if err != nil {
			t.Fatalf("frame %q is not ascii bar heights: %v", last, err)
		}
		if v != 0 {
			t.Errorf("a second after the audio ended the bars are still at %q", last)
			break
		}
	}
}
