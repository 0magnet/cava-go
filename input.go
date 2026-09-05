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
// than into -1..1. That is the original's convention and the equalizer in
// [Plan] is calibrated for it, so a 32-bit float input is multiplied up by
// 65535 rather than normalized down.
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

// Silence tops the stream up with zeros, so that the display falls away
// rather than freezing on the last frame. The reader calls it when the source
// goes quiet or disappears.
//
// It appends rather than overwrites. Zeroing the whole buffer would throw away
// audio that arrived but has not been drawn yet, which is the difference
// between a track fading out and a track that was never seen at all: a source
// that delivers faster than the frame rate always has samples in hand when it
// reaches its end.
func (s *Stream) Silence() {
	s.mu.Lock()
	defer s.mu.Unlock()
	clear(s.buf[s.n:])
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

// Input describes the stream a reader is about to consume.
//
// Rate and Channels are here for one reason only, and it is not decoding: they
// are what lets a reader deliver samples at the speed the audio is meant to
// play. See [Pump].
type Input struct {
	// Format is the byte layout of one sample.
	Format Format
	// Rate is the sample rate in Hz.
	Rate int
	// Channels is 1 or 2, interleaved.
	Channels int
	// FrameSamples is how many samples to read per turn, counting every
	// channel. cava reads 512 per channel.
	FrameSamples int
	// Unpaced delivers samples as fast as the source hands them over, instead
	// of at the rate the audio plays. It is for tests that want to push a
	// fixed amount of audio through without waiting for it.
	Unpaced bool
}

// Validate checks the input description.
func (in Input) Validate() error {
	if err := in.Format.Validate(); err != nil {
		return err
	}
	if in.Rate < 1 {
		return fmt.Errorf("cava: sample rate must be positive, got %d", in.Rate)
	}
	if in.Channels < 1 || in.Channels > 2 {
		return fmt.Errorf("cava: channels must be 1 or 2, got %d", in.Channels)
	}
	if in.FrameSamples < in.Channels {
		return fmt.Errorf("cava: frame must hold at least one sample per channel, got %d for %d channels", in.FrameSamples, in.Channels)
	}
	return nil
}

// maxLag is how far behind the playback clock a reader may fall before it
// gives up on catching back up and re-anchors.
//
// A source that goes quiet — a fifo between tracks — leaves the clock running
// while nothing is consumed. Without a limit the reader would then sprint
// through whatever arrives next to "catch up", which is the very thing the
// pacing exists to prevent.
const maxLag = 250 * time.Millisecond

// Pump reads PCM from r and feeds it to s until r ends or stop closes.
//
// It delivers samples at the rate they are meant to play. That is not padding
// for its own sake: everything downstream assumes a live source. The engine
// derives its frame rate from how many samples arrive per call, the smoothing
// and sensitivity constants are scaled by that estimate, and the drawing loop
// takes whatever has accumulated since the last frame. Hand all of it two
// seconds of audio in five milliseconds — which is what a file, or any pipe
// not fed by a player, does — and the visualizer sees the whole track in a
// frame or two and then draws silence for as long as it is left running.
//
// A genuinely live source is unaffected, because it was already arriving at
// this rate and the wait is always zero. A fifo fed by a player in bursts is
// smoothed back out to the rate it plays at.
//
// It is the caller's job to run this in its own goroutine. A short read is
// held onto rather than decoded, so a source that delivers a sample at a time
// still produces whole frames.
func Pump(s *Stream, r io.Reader, in Input, stop <-chan struct{}) error {
	if err := in.Validate(); err != nil {
		return err
	}
	buf := make([]byte, in.FrameSamples*in.Format.BytesPerSample())
	samples := make([]float64, in.FrameSamples)

	// The playback clock: when the audio delivered so far would have finished
	// playing, had it been played rather than read.
	start := time.Now()
	var delivered int // frames, meaning samples per channel

	for {
		select {
		case <-stop:
			return nil
		default:
		}
		n, err := io.ReadFull(r, buf)
		if n > 0 {
			got := in.Format.Decode(samples, buf[:n])
			s.Write(samples[:got])
			delivered += got / in.Channels

			if !in.Unpaced {
				due := start.Add(time.Duration(float64(delivered) / float64(in.Rate) * float64(time.Second)))
				switch lag := time.Until(due); {
				case lag > 0:
					select {
					case <-stop:
						return nil
					case <-time.After(lag):
					}
				case lag < -maxLag:
					// The source stalled for longer than the buffer is worth.
					// Start the clock again from here rather than racing.
					start = time.Now()
					delivered = 0
				}
			}
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
// other end. That is the behavior to want — there is nothing to draw before
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

// errStopped ends the wait for a source that never arrived.
var errStopped = errors.New("cava: stopped")

// openWaiting opens the source, keeping the stream topped up with silence for
// as long as that takes.
//
// Opening a fifo blocks until something opens the other end, which may be
// never. The display has to keep falling away while that is going on, or a
// player that quits leaves its last frame frozen on the screen for as long as
// the visualizer is left running.
//
// If stop closes first the open is abandoned where it stands. The goroutine
// holding it stays blocked until a writer appears, which is a leak with a
// bounded lifetime: it happens once, on the way out.
func openWaiting(s *Stream, path string, stop <-chan struct{}) (io.ReadCloser, error) {
	type opened struct {
		r   io.ReadCloser
		err error
	}
	ch := make(chan opened, 1)
	go func() {
		r, err := OpenSource(path)
		ch <- opened{r, err}
	}()
	for {
		select {
		case o := <-ch:
			return o.r, o.err
		case <-stop:
			return nil, errStopped
		case <-time.After(idleTimeout):
			s.Silence()
		}
	}
}

// quiet keeps the stream full of silence until stop closes.
//
// This is what a source that has ended for good gets. Without it the engine
// would go on transforming its last buffer for ever — with no new samples it
// has nothing to shift in, so the same input produces the same bars — and the
// display would sit at whatever the final fragment of audio happened to look
// like instead of going quiet.
func quiet(s *Stream, stop <-chan struct{}) {
	for {
		s.Silence()
		select {
		case <-stop:
			return
		case <-time.After(idleTimeout):
		}
	}
}

// PumpSource reads path into s forever, reopening it whenever it ends.
//
// A fifo ends every time its writer closes — a track finishing, a player being
// restarted — and cava's answer is to let the display fall away and open it
// again. The same loop covers a plain file, which simply repeats.
func PumpSource(s *Stream, path string, in Input, stop <-chan struct{}) {
	for {
		select {
		case <-stop:
			return
		default:
		}
		r, err := openWaiting(s, path, stop)
		if err != nil {
			if !errors.Is(err, errStopped) {
				s.Fail(err)
			}
			return
		}
		err = Pump(s, r, in, stop)
		_ = r.Close() //nolint:errcheck // the source is being replaced either way
		if err != nil && !errors.Is(err, io.EOF) {
			s.Fail(err)
			return
		}
		if path == "-" || path == "/dev/stdin" {
			// Standard input cannot be reopened; when it ends it is over, and
			// all that is left to do is let the picture fade.
			quiet(s, stop)
			return
		}
		s.Silence()
		select {
		case <-stop:
			return
		case <-time.After(idleTimeout):
		}
	}
}
