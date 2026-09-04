package cava

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"sync"
	"time"
)

// Format describes the PCM the input carries.
//
// The engine wants samples in the range of a signed 16-bit sample, roughly
// -32768..32768, and every other width is converted into that range rather
// than into -1..1. That is the original's convention and the equaliser in
// [Plan] is calibrated for it, so a 32-bit float input is multiplied up by
// 65535 rather than normalised down.
type Format struct {
	// Bits is 8, 16, 24 or 32.
	Bits int
	// Float says the samples are IEEE 754 binary32. Only meaningful at 32
	// bits, and ignored otherwise.
	Float bool
	// BigEndian selects the byte order. Every format cava reads is little
	// endian, which is the zero value; the field exists because a fifo is fed by
	// hand often enough that getting it wrong is worth being able to fix.
	BigEndian bool
}

// Format16 is signed 16-bit little endian, cava's default and what
// `mpd`'s fifo output produces.
var Format16 = Format{Bits: 16}

// BytesPerSample is the stride of one sample of one channel.
func (f Format) BytesPerSample() int { return f.Bits / 8 }

// Validate reports whether the format is one the decoder handles.
func (f Format) Validate() error {
	switch f.Bits {
	case 8, 16, 24, 32:
	default:
		return fmt.Errorf("cava: unsupported sample_bits %d, want 8, 16, 24 or 32", f.Bits)
	}
	if f.Float && f.Bits != 32 {
		return fmt.Errorf("cava: float samples must be 32 bit, got %d", f.Bits)
	}
	return nil
}

func (f Format) String() string {
	if f.Float {
		return "32-bit float"
	}
	return fmt.Sprintf("%d-bit signed", f.Bits)
}

// Decode converts len(dst) samples from buf, which must hold at least
// len(dst)*BytesPerSample bytes. It returns the number of samples written.
//
// 24-bit input deserves a word. The original reads a full 32-bit integer at a
// 3-byte stride and divides it down, which means each sample carries one byte
// of the next one in its low bits — audible as nothing, since the value is
// divided by 65535 anyway, but not what a 24-bit decoder would normally do.
// This does the honest thing instead: sign-extend the three bytes and scale
// them into the 16-bit range.
func (f Format) Decode(dst []float64, buf []byte) int {
	bps := f.BytesPerSample()
	n := len(buf) / bps
	if n > len(dst) {
		n = len(dst)
	}
	order := binary.ByteOrder(binary.LittleEndian)
	if f.BigEndian {
		order = binary.BigEndian
	}
	for i := 0; i < n; i++ {
		b := buf[i*bps:]
		switch {
		case f.Bits == 8:
			// A signed byte scaled by 255 to reach the same range as a 16-bit
			// sample.
			dst[i] = float64(int8(b[0])) * 255 //nolint:gosec // a byte reinterpreted as signed is the point
		case f.Bits == 16:
			dst[i] = float64(int16(order.Uint16(b))) //nolint:gosec // reinterpreting the bits as signed is the point
		case f.Bits == 24:
			var raw int32
			if f.BigEndian {
				raw = int32(b[0])<<16 | int32(b[1])<<8 | int32(b[2])
			} else {
				raw = int32(b[2])<<16 | int32(b[1])<<8 | int32(b[0])
			}
			// Sign-extend from 24 bits.
			if raw&0x800000 != 0 {
				raw -= 1 << 24
			}
			dst[i] = float64(raw) / 256
		case f.Float:
			dst[i] = float64(math.Float32frombits(order.Uint32(b))) * 65535
		default:
			dst[i] = float64(int32(order.Uint32(b))) / 65535 //nolint:gosec // reinterpreting the bits as signed is the point
		}
	}
	return n
}

// Stream is the handover between whatever is reading audio and the loop that
// draws it. It is the same arrangement cava uses: a reader fills it as fast as
// the source delivers, and the drawing loop takes whatever has arrived since
// the last frame. Neither waits for the other.
type Stream struct {
	mu  sync.Mutex
	buf []float64
	n   int
	err error
}

// NewStream returns a stream holding up to size samples. cava uses 16384,
// which at 44100 Hz stereo is about 185 ms of slack — enough that a stalled
// drawing loop does not lose audio, and short enough that the picture does not
// lag the sound.
func NewStream(size int) *Stream {
	return &Stream{buf: make([]float64, size)}
}

// Write appends samples. On overflow the whole buffer is discarded and filling
// starts again, which is what the original does: a backlog is worse than a
// gap, because every sample in it is drawn late.
func (s *Stream) Write(samples []float64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.n+len(samples) > len(s.buf) {
		clear(s.buf)
		s.n = 0
	}
	if len(samples) > len(s.buf) {
		samples = samples[len(samples)-len(s.buf):]
	}
	s.n += copy(s.buf[s.n:], samples)
}

// Silence fills the stream with zeros, so that the display falls away rather
// than freezing on the last frame. The reader calls it when the source goes
// quiet or disappears.
func (s *Stream) Silence() {
	s.mu.Lock()
	defer s.mu.Unlock()
	clear(s.buf)
	s.n = len(s.buf)
}

// Fail records a terminal error for [Stream.Take] to report.
func (s *Stream) Fail(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err == nil {
		s.err = err
	}
}

// Available reports how many samples are waiting.
func (s *Stream) Available() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.n
}

// Take copies up to max samples into dst, removes them from the stream and
// returns how many it moved, along with any error the reader has reported.
// Passing max <= 0 takes everything available.
func (s *Stream) Take(dst []float64, max int) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := s.n
	if max > 0 && n > max {
		n = max
	}
	if n > len(dst) {
		n = len(dst)
	}
	copy(dst[:n], s.buf[:n])
	// Shift the remainder down. The buffer is small and this only runs once a
	// frame, so a ring buffer would be complexity for nothing.
	copy(s.buf, s.buf[n:s.n])
	s.n -= n
	return n, s.err
}

// idleTimeout is how long an input may deliver nothing before the display is
// let go. cava counts ten sleeps of ten milliseconds.
const idleTimeout = 100 * time.Millisecond

// Pump reads PCM from r and feeds it to s until r ends or ctx-like stop
// channel closes. frameSamples is how many samples to read per turn, counting
// every channel: cava reads 512 per channel.
//
// It is the caller's job to run this in its own goroutine. A short read is
// held onto rather than decoded, so a source that delivers a sample at a time
// still produces whole frames.
func Pump(s *Stream, r io.Reader, format Format, frameSamples int, stop <-chan struct{}) error {
	if err := format.Validate(); err != nil {
		return err
	}
	buf := make([]byte, frameSamples*format.BytesPerSample())
	samples := make([]float64, frameSamples)
	for {
		select {
		case <-stop:
			return nil
		default:
		}
		n, err := io.ReadFull(r, buf)
		if n > 0 {
			got := format.Decode(samples, buf[:n])
			s.Write(samples[:got])
		}
		if err != nil {
			if errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, io.EOF) {
				return io.EOF
			}
			return err
		}
	}
}

// OpenSource opens an input by cava's naming: a path is a fifo (or any file),
// and "-" or "/dev/stdin" is standard input.
//
// A fifo is opened blocking, so this does not return until something opens the
// other end. That is the behaviour to want — there is nothing to draw before
// then — but it does mean a wrong path leaves the program waiting rather than
// failing.
func OpenSource(path string) (io.ReadCloser, error) {
	if path == "-" || path == "/dev/stdin" {
		return io.NopCloser(os.Stdin), nil
	}
	f, err := os.Open(path) //nolint:gosec // the path is the user's own config
	if err != nil {
		return nil, err
	}
	return f, nil
}

// PumpSource reads path into s forever, reopening it whenever it ends.
//
// A fifo ends every time its writer closes — a track finishing, a player being
// restarted — and cava's answer is to zero the display and open it again. The
// same loop covers a plain file, which simply repeats.
func PumpSource(s *Stream, path string, format Format, frameSamples int, stop <-chan struct{}) {
	for {
		select {
		case <-stop:
			return
		default:
		}
		r, err := OpenSource(path)
		if err != nil {
			s.Fail(err)
			return
		}
		err = Pump(s, r, format, frameSamples, stop)
		_ = r.Close() //nolint:errcheck // the source is being replaced either way
		if err != nil && !errors.Is(err, io.EOF) {
			s.Fail(err)
			return
		}
		s.Silence()
		if path == "-" || path == "/dev/stdin" {
			// Standard input cannot be reopened; when it ends it is over.
			return
		}
		select {
		case <-stop:
			return
		case <-time.After(idleTimeout):
		}
	}
}
