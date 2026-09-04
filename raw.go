package cava

import (
	"bufio"
	"encoding/binary"
	"io"
	"strconv"
)

// RawWriter is cava's `raw` output method: bar heights written to a stream for
// something else to draw. It is what makes cava usable as a source rather than
// as a display — a status bar, a LED strip, a shader.
//
// The units differ from the terminal output. There the bars are in eighths of
// a cell; here they run from 0 to the full range of the chosen format, so the
// consumer does not have to know how tall a terminal was.
type RawWriter struct {
	w *bufio.Writer

	binary     bool
	bitFormat  int // 8 or 16, binary only
	asciiRange int
	barDelim   byte
	frameDelim byte

	buf []byte
}

// NewRawWriter returns a raw writer.
//
// binaryFormat picks between the binary and ascii forms. bitFormat is 8 or 16
// and only applies to binary; asciiRange is the full-scale value for ascii.
// The delimiters are the byte after each bar and after each frame, as cava's
// bar_delimiter and frame_delimiter.
func NewRawWriter(w io.Writer, binaryFormat bool, bitFormat, asciiRange int, barDelim, frameDelim byte) *RawWriter {
	return &RawWriter{
		w:          bufio.NewWriter(w),
		binary:     binaryFormat,
		bitFormat:  bitFormat,
		asciiRange: asciiRange,
		barDelim:   barDelim,
		frameDelim: frameDelim,
	}
}

// FullScale is the value a bar of maximum height carries in this format.
func (rw *RawWriter) FullScale() int {
	if !rw.binary {
		return rw.asciiRange
	}
	if rw.bitFormat == 8 {
		return 255
	}
	return 65535
}

// WriteFrame writes one frame of bar heights and flushes it.
//
// Binary 16-bit output is two bytes per bar, little endian. cava writes the
// machine's own byte order, which on everything it is built for is little
// endian; making that explicit is the one place this differs, and it differs
// only on hardware cava does not run on.
func (rw *RawWriter) WriteFrame(bars []int) error {
	limit := rw.FullScale()
	if rw.binary {
		rw.buf = rw.buf[:0]
		for _, v := range bars {
			if v > limit {
				v = limit
			}
			if v < 0 {
				v = 0
			}
			if rw.bitFormat == 8 {
				rw.buf = append(rw.buf, byte(v))
			} else {
				rw.buf = binary.LittleEndian.AppendUint16(rw.buf, uint16(v)) //nolint:gosec // clamped above
			}
		}
		if _, err := rw.w.Write(rw.buf); err != nil {
			return err
		}
		return rw.w.Flush()
	}

	for _, v := range bars {
		if v > limit {
			v = limit
		}
		if v < 0 {
			v = 0
		}
		if _, err := rw.w.WriteString(strconv.Itoa(v)); err != nil {
			return err
		}
		if err := rw.w.WriteByte(rw.barDelim); err != nil {
			return err
		}
	}
	if err := rw.w.WriteByte(rw.frameDelim); err != nil {
		return err
	}
	return rw.w.Flush()
}
