package cava

import (
	"bytes"
	"testing"
)

// TestRawAscii is the format anything scripted will read: decimal numbers, one
// per bar, with the delimiters the config names.
func TestRawAscii(t *testing.T) {
	var buf bytes.Buffer
	w := NewRawWriter(&buf, false, 16, 1000, ';', '\n')
	if err := w.WriteFrame([]int{0, 500, 1000, 2000, -5}); err != nil {
		t.Fatal(err)
	}
	if got, want := buf.String(), "0;500;1000;1000;0;\n"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestRawAsciiCustomDelimiters(t *testing.T) {
	var buf bytes.Buffer
	w := NewRawWriter(&buf, false, 16, 100, ' ', '|')
	if err := w.WriteFrame([]int{1, 2}); err != nil {
		t.Fatal(err)
	}
	if got, want := buf.String(), "1 2 |"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// TestRawBinary8 pins one byte per bar, clamped to 255.
func TestRawBinary8(t *testing.T) {
	var buf bytes.Buffer
	w := NewRawWriter(&buf, true, 8, 1000, ';', '\n')
	if w.FullScale() != 255 {
		t.Errorf("full scale = %d, want 255", w.FullScale())
	}
	if err := w.WriteFrame([]int{0, 128, 255, 999}); err != nil {
		t.Fatal(err)
	}
	if got, want := buf.Bytes(), []byte{0, 128, 255, 255}; !bytes.Equal(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

// TestRawBinary16 pins two bytes per bar, little endian.
func TestRawBinary16(t *testing.T) {
	var buf bytes.Buffer
	w := NewRawWriter(&buf, true, 16, 1000, ';', '\n')
	if w.FullScale() != 65535 {
		t.Errorf("full scale = %d, want 65535", w.FullScale())
	}
	if err := w.WriteFrame([]int{0, 258, 65535, 70000}); err != nil {
		t.Fatal(err)
	}
	want := []byte{0, 0, 2, 1, 0xff, 0xff, 0xff, 0xff}
	if got := buf.Bytes(); !bytes.Equal(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

// TestRawFramesAreWholeAndFlushed: a consumer reading a fixed number of bytes
// per frame must never see half of one, so every frame is flushed as it is
// written.
func TestRawFramesAreWholeAndFlushed(t *testing.T) {
	var buf bytes.Buffer
	w := NewRawWriter(&buf, true, 16, 1000, ';', '\n')
	for i := 0; i < 3; i++ {
		if err := w.WriteFrame(make([]int, 10)); err != nil {
			t.Fatal(err)
		}
		if got, want := buf.Len(), (i+1)*20; got != want {
			t.Fatalf("after %d frames the stream holds %d bytes, want %d", i+1, got, want)
		}
	}
}
