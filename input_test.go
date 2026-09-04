package cava

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"math"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// TestDecodeFormats pins each width to the range the engine expects, which is
// that of a 16-bit sample rather than -1..1. Getting this wrong does not crash
// anything; it makes the bars a thousand times too tall or too short, and
// autosens hides it until someone wonders why the display is dead.
func TestDecodeFormats(t *testing.T) {
	for _, tc := range []struct {
		name   string
		format Format
		bytes  []byte
		want   []float64
	}{
		{
			name:   "8-bit signed, scaled up by 255",
			format: Format{Bits: 8},
			bytes:  []byte{0x00, 0x7f, 0x80, 0xff},
			want:   []float64{0, 127 * 255, -128 * 255, -1 * 255},
		},
		{
			name:   "16-bit signed little endian, unchanged",
			format: Format{Bits: 16},
			bytes:  []byte{0x00, 0x00, 0xff, 0x7f, 0x00, 0x80},
			want:   []float64{0, 32767, -32768},
		},
		{
			name:   "16-bit signed big endian",
			format: Format{Bits: 16, BigEndian: true},
			bytes:  []byte{0x7f, 0xff, 0x80, 0x00},
			want:   []float64{32767, -32768},
		},
		{
			name:   "24-bit signed, scaled down by 256",
			format: Format{Bits: 24},
			bytes:  []byte{0x00, 0x00, 0x00, 0xff, 0xff, 0x7f, 0x00, 0x00, 0x80},
			want:   []float64{0, 8388607.0 / 256, -8388608.0 / 256},
		},
		{
			name:   "32-bit signed, scaled down by 65535",
			format: Format{Bits: 32},
			bytes:  []byte{0x00, 0x00, 0x00, 0x00, 0xff, 0xff, 0xff, 0x7f},
			want:   []float64{0, 2147483647.0 / 65535},
		},
	} {
		got := make([]float64, len(tc.want))
		n := tc.format.Decode(got, tc.bytes)
		if n != len(tc.want) {
			t.Errorf("%s: decoded %d samples, want %d", tc.name, n, len(tc.want))
			continue
		}
		for i := range got {
			if math.Abs(got[i]-tc.want[i]) > 1e-9 {
				t.Errorf("%s: sample %d = %v, want %v", tc.name, i, got[i], tc.want[i])
			}
		}
	}
}

// TestDecodeFloat is separate because the bytes have to be built rather than
// written out.
func TestDecodeFloat(t *testing.T) {
	f := Format{Bits: 32, Float: true}
	buf := make([]byte, 12)
	for i, v := range []float32{0, 1, -0.5} {
		binary.LittleEndian.PutUint32(buf[i*4:], math.Float32bits(v))
	}
	got := make([]float64, 3)
	f.Decode(got, buf)
	want := []float64{0, 65535, -32767.5}
	for i := range want {
		if math.Abs(got[i]-want[i]) > 1e-6 {
			t.Errorf("sample %d = %v, want %v", i, got[i], want[i])
		}
	}
}

// TestDecodeStopsAtTheShorterSlice: a partial read must not run off the end of
// either buffer.
func TestDecodeStopsAtTheShorterSlice(t *testing.T) {
	f := Format{Bits: 16}
	got := make([]float64, 10)
	if n := f.Decode(got, []byte{1, 0, 2, 0}); n != 2 {
		t.Errorf("decoded %d samples from four bytes, want 2", n)
	}
	small := make([]float64, 1)
	if n := f.Decode(small, []byte{1, 0, 2, 0, 3, 0}); n != 1 {
		t.Errorf("decoded %d samples into a one-sample slice, want 1", n)
	}
}

func TestFormatValidate(t *testing.T) {
	for _, f := range []Format{{Bits: 8}, {Bits: 16}, {Bits: 24}, {Bits: 32}, {Bits: 32, Float: true}} {
		if err := f.Validate(); err != nil {
			t.Errorf("%v rejected: %v", f, err)
		}
	}
	for _, f := range []Format{{Bits: 0}, {Bits: 12}, {Bits: 64}, {Bits: 16, Float: true}} {
		if err := f.Validate(); err == nil {
			t.Errorf("%v accepted", f)
		}
	}
}

// TestStreamTakeAndShift checks the handover buffer: what goes in comes out in
// order, and what is left behind stays in order too.
func TestStreamTakeAndShift(t *testing.T) {
	s := NewStream(8)
	s.Write([]float64{1, 2, 3, 4, 5})
	if got := s.Available(); got != 5 {
		t.Fatalf("available = %d, want 5", got)
	}

	dst := make([]float64, 8)
	n, err := s.Take(dst, 2)
	if err != nil || n != 2 || dst[0] != 1 || dst[1] != 2 {
		t.Fatalf("first take: n=%d err=%v dst=%v", n, err, dst[:n])
	}
	n, _ = s.Take(dst, 0) //nolint:errcheck // no reader has failed
	if n != 3 || dst[0] != 3 || dst[1] != 4 || dst[2] != 5 {
		t.Fatalf("second take: n=%d dst=%v", n, dst[:n])
	}
	if n, _ := s.Take(dst, 0); n != 0 { //nolint:errcheck // no reader has failed
		t.Errorf("a drained stream gave %d samples", n)
	}
}

// TestStreamOverflowDiscards pins the choice the original makes: when the
// drawing loop cannot keep up, the backlog is thrown away rather than drawn
// late.
func TestStreamOverflowDiscards(t *testing.T) {
	s := NewStream(4)
	s.Write([]float64{1, 2, 3})
	s.Write([]float64{4, 5, 6})
	dst := make([]float64, 8)
	n, _ := s.Take(dst, 0) //nolint:errcheck // no reader has failed
	if n != 3 || dst[0] != 4 {
		t.Errorf("after overflow the stream holds %v, want the newer samples", dst[:n])
	}
}

// TestStreamSilenceFills is what makes the picture fall away when the source
// disappears instead of freezing on the last frame.
func TestStreamSilenceFills(t *testing.T) {
	s := NewStream(4)
	s.Write([]float64{1, 2})
	s.Silence()
	dst := make([]float64, 4)
	n, _ := s.Take(dst, 0) //nolint:errcheck // no reader has failed
	if n != 4 {
		t.Fatalf("Silence left %d samples, want 4", n)
	}
	for i, v := range dst {
		if v != 0 {
			t.Errorf("sample %d = %v, want silence", i, v)
		}
	}
}

func TestStreamFailIsReported(t *testing.T) {
	s := NewStream(4)
	want := errors.New("the fifo went away")
	s.Fail(want)
	if _, err := s.Take(make([]float64, 4), 0); !errors.Is(err, want) {
		t.Errorf("Take returned %v, want %v", err, want)
	}
}

// TestPumpReadsWholeFrames drives the reader end to end from a byte slice,
// which is the same path a fifo takes.
func TestPumpReadsWholeFrames(t *testing.T) {
	// Four frames of 512 stereo samples, counting up so order is checkable.
	const frame = 512 * 2
	raw := make([]byte, frame*4*2)
	for i := 0; i < frame*4; i++ {
		binary.LittleEndian.PutUint16(raw[i*2:], uint16(int16(i%1000))) //nolint:gosec // deliberate wrap
	}

	s := NewStream(frame * 8)
	err := Pump(s, bytes.NewReader(raw), Format16, frame, nil)
	if !errors.Is(err, io.EOF) {
		t.Fatalf("Pump returned %v, want io.EOF at the end of the data", err)
	}
	if got := s.Available(); got != frame*4 {
		t.Fatalf("stream holds %d samples, want %d", got, frame*4)
	}
	dst := make([]float64, frame*4)
	s.Take(dst, 0) //nolint:errcheck,gosec // no reader has failed
	for i := 0; i < 10; i++ {
		if dst[i] != float64(i) {
			t.Errorf("sample %d = %v, want %v", i, dst[i], float64(i))
		}
	}
}

// TestPumpStopsOnRequest: the reader must not outlive the program.
func TestPumpStopsOnRequest(t *testing.T) {
	stop := make(chan struct{})
	close(stop)
	s := NewStream(1024)
	if err := Pump(s, bytes.NewReader(make([]byte, 4096)), Format16, 512, stop); err != nil {
		t.Errorf("Pump returned %v after being stopped", err)
	}
}

func TestPumpRejectsABadFormat(t *testing.T) {
	if err := Pump(NewStream(16), bytes.NewReader(nil), Format{Bits: 12}, 8, nil); err == nil {
		t.Error("Pump accepted a 12-bit format")
	}
}

// TestPumpSourceReadsAFifo is the real thing: a named pipe, a writer that
// closes, and a reader that keeps the display alive afterwards. It is the
// input path this port actually ships, so it is worth exercising rather than
// stubbing.
func TestPumpSourceReadsAFifo(t *testing.T) {
	if _, err := os.Stat("/dev/null"); err != nil {
		t.Skip("not a unix-like system")
	}
	path := filepath.Join(t.TempDir(), "cava.fifo")
	if err := syscall.Mkfifo(path, 0o600); err != nil {
		t.Skipf("cannot create a fifo here: %v", err)
	}

	s := NewStream(8192)
	stop := make(chan struct{})
	defer close(stop)
	go PumpSource(s, path, Format16, 512*2, stop)

	// Opening for writing unblocks the reader's open.
	w, err := os.OpenFile(path, os.O_WRONLY, 0) //nolint:gosec // the path is the test's own temp dir
	if err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 512*2*2)
	for i := 0; i < 512*2; i++ {
		binary.LittleEndian.PutUint16(buf[i*2:], uint16(int16(1000))) //nolint:gosec // constant
	}
	if _, err := w.Write(buf); err != nil {
		t.Fatal(err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for s.Available() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if s.Available() == 0 {
		t.Fatal("nothing arrived from the fifo")
	}
	dst := make([]float64, 8192)
	n, err := s.Take(dst, 0)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < n; i++ {
		if dst[i] != 1000 {
			t.Fatalf("sample %d = %v, want 1000", i, dst[i])
		}
	}
	w.Close() //nolint:errcheck,gosec // the fifo is being closed deliberately

	// The writer going away must leave the reader alive and the display
	// falling away, not the program dead.
	deadline = time.Now().Add(2 * time.Second)
	for s.Available() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if s.Available() == 0 {
		t.Error("after the writer closed, the stream was not filled with silence")
	}
}

func TestOpenSourceStdin(t *testing.T) {
	for _, name := range []string{"-", "/dev/stdin"} {
		r, err := OpenSource(name)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if err := r.Close(); err != nil {
			t.Errorf("%s: close: %v", name, err)
		}
	}
}
