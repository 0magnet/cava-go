package cava

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// Config is cava's configuration, as it appears in the INI file and on the
// command line.
//
// The field set is the subset of cava's that applies to what is implemented
// here — see [Config.Validate] for what happens to the rest. Defaults are the
// original's, so a config written for cava means the same thing.
type Config struct {
	// [general]
	Framerate     int     // frames per second, default 60
	Autosens      int     // 0 off, 1 normal, higher climbs faster
	Sensitivity   float64 // manual gain in percent, only used with autosens off
	Scaling       ScalingMode
	Bars          int // 0 fills the terminal
	BarWidth      int
	BarSpacing    int
	CenterAlign   bool
	MaxHeight     float64 // fraction of the terminal height, 0..1
	LowerCutOff   int     // Hz
	HigherCutOff  int     // Hz
	SleepTimer    int     // seconds of silence before idling, 0 disables
	DrawAndQuit   int     // draw this many frames then exit; cava's test hook
	InputMethod   string  // "fifo" or "stdin"
	Source        string  // fifo path, or "-" for standard input
	SampleRate    int
	SampleBits    int
	SampleFloat   bool
	InputChannels int

	// [output]
	OutputMethod     string // "noncurses" or "raw"
	Orientation      string // "bottom" or "top"
	Channels         string // "stereo" or "mono"
	MonoOption       string // "left", "right" or "average"
	Reverse          bool
	RawTarget        string
	DataFormat       string // "binary" or "ascii"
	BitFormat        int    // 8 or 16
	AsciiMaxRange    int
	BarDelimiter     byte
	FrameDelimiter   byte
	ShowIdleBarHeads bool
	XAxis            string // "none" or "frequency"
	SynchronizedSync bool

	// [color]
	Foreground         string
	Background         string
	Gradient           bool
	GradientColors     []string
	HorizontalGradient bool
	HorizontalColors   []string
	BlendDirection     string // "up", "down", "left" or "right"

	// [smoothing]
	Monstercat     float64
	Waves          int
	NoiseReduction float64 // 0..1 after loading; the file holds 0..100

	// [eq]
	UserEQ []float64

	// Ignored holds keys that were understood by cava but do not apply to this
	// build — the cgo-backed input methods and the graphical outputs. They are
	// kept rather than dropped so the program can say what it disregarded
	// instead of silently doing something else.
	Ignored []string
}

// DefaultConfig returns cava's defaults.
//
// Two of them are worth knowing: the upper cut-off is 8000 Hz, which is what
// cava's own code uses even though its example config file says 10000; and
// noise reduction is stored here as a fraction, where the file holds a
// percentage.
func DefaultConfig() *Config {
	return &Config{
		Framerate:        60,
		Autosens:         1,
		Sensitivity:      100,
		Scaling:          ScalingLinear,
		Bars:             0,
		BarWidth:         2,
		BarSpacing:       1,
		CenterAlign:      true,
		MaxHeight:        1.0,
		LowerCutOff:      50,
		HigherCutOff:     8000,
		InputMethod:      "fifo",
		Source:           "/tmp/mpd.fifo",
		SampleRate:       44100,
		SampleBits:       16,
		InputChannels:    2,
		OutputMethod:     "noncurses",
		Orientation:      "bottom",
		Channels:         "stereo",
		MonoOption:       "average",
		RawTarget:        "/dev/stdout",
		DataFormat:       "binary",
		BitFormat:        16,
		AsciiMaxRange:    1000,
		BarDelimiter:     ';',
		FrameDelimiter:   '\n',
		ShowIdleBarHeads: true,
		XAxis:            "none",
		Foreground:       "default",
		Background:       "default",
		BlendDirection:   "up",
		NoiseReduction:   0.77,
	}
}

// Format returns the sample format the input is configured for.
func (c *Config) Format() Format {
	return Format{Bits: c.SampleBits, Float: c.SampleFloat}
}

// OutputChannels is 2 when the two channels are drawn separately, 1 when they
// are mixed into one row of bars.
func (c *Config) OutputChannels() int {
	if c.Channels == "stereo" {
		return 2
	}
	return 1
}

// unsupportedInput lists the input methods that exist in cava but need a C
// library, and the library each needs. They are rejected by name so the error
// says what is wrong rather than "unknown method".
var unsupportedInput = map[string]string{
	"alsa":      "libasound",
	"pulse":     "libpulse",
	"pipewire":  "libpipewire",
	"portaudio": "libportaudio",
	"jack":      "libjack",
	"sndio":     "libsndio",
	"oss":       "the OSS ioctl interface",
	"coreaudio": "the macOS CoreAudio framework",
	"shmem":     "squeezelite's shared memory segment",
	"winscap":   "the Windows WASAPI loopback interface",
}

// unsupportedOutput is the same for output methods.
var unsupportedOutput = map[string]string{
	"ncurses":  "libncurses",
	"sdl":      "libSDL2",
	"sdl_glsl": "libSDL2 and OpenGL",
	"noritake": "a Noritake VFD panel",
}

// Validate checks the configuration and normalises it, returning an error for
// anything that cannot be honoured.
//
// An input or output method that cava supports through a C library is an error
// here and not a warning: falling back to a different source would draw
// something, and something is worse than nothing when the user asked for their
// speakers.
func (c *Config) Validate() error {
	switch c.InputMethod {
	case "fifo":
	case "stdin":
		c.Source = "-"
	default:
		if lib, ok := unsupportedInput[c.InputMethod]; ok {
			return fmt.Errorf("input method %q needs %s and is not available in this pure-Go port; use 'fifo' or 'stdin'", c.InputMethod, lib)
		}
		return fmt.Errorf("unknown input method %q, supported methods are 'fifo' and 'stdin'", c.InputMethod)
	}

	switch c.OutputMethod {
	case "noncurses", "raw":
	default:
		if lib, ok := unsupportedOutput[c.OutputMethod]; ok {
			return fmt.Errorf("output method %q needs %s and is not available in this pure-Go port; use 'noncurses' or 'raw'", c.OutputMethod, lib)
		}
		return fmt.Errorf("unknown output method %q, supported methods are 'noncurses' and 'raw'", c.OutputMethod)
	}

	switch c.Orientation {
	case "bottom", "top":
	case "left", "right", "horizontal", "vertical":
		return fmt.Errorf("orientation %q is not implemented; use 'bottom' or 'top'", c.Orientation)
	default:
		return fmt.Errorf("unknown orientation %q", c.Orientation)
	}

	switch c.Channels {
	case "stereo", "mono":
	default:
		return fmt.Errorf("output channels must be 'stereo' or 'mono', got %q", c.Channels)
	}
	switch c.MonoOption {
	case "left", "right", "average":
	default:
		return fmt.Errorf("mono_option must be 'left', 'right' or 'average', got %q", c.MonoOption)
	}
	switch c.DataFormat {
	case "binary", "ascii":
	default:
		return fmt.Errorf("data_format must be 'binary' or 'ascii', got %q", c.DataFormat)
	}
	if c.BitFormat != 8 && c.BitFormat != 16 {
		return fmt.Errorf("bit_format must be 8 or 16, got %d", c.BitFormat)
	}
	switch c.XAxis {
	case "none", "frequency":
	case "note":
		return fmt.Errorf("xaxis 'note' is not implemented; use 'none' or 'frequency'")
	default:
		return fmt.Errorf("xaxis must be 'none' or 'frequency', got %q", c.XAxis)
	}
	switch c.BlendDirection {
	case "up", "down", "left", "right":
	default:
		return fmt.Errorf("blend_direction must be 'up', 'down', 'left' or 'right', got %q", c.BlendDirection)
	}

	if c.Framerate < 1 {
		return fmt.Errorf("framerate can't be less than 1")
	}
	if c.BarWidth < 1 {
		c.BarWidth = 1
	}
	if c.BarSpacing < 0 {
		c.BarSpacing = 0
	}
	if c.NoiseReduction < 0 {
		c.NoiseReduction = 0
	} else if c.NoiseReduction > 1 {
		c.NoiseReduction = 1
	}
	if c.MaxHeight > 1.0 {
		return fmt.Errorf("max_height is defined in percent and can't be more than 100")
	}
	if c.MaxHeight < 0.0 {
		return fmt.Errorf("max_height can't be negative")
	}
	if c.InputChannels < 1 || c.InputChannels > 2 {
		return fmt.Errorf("channels must be 1 or 2, got %d", c.InputChannels)
	}
	if err := c.Format().Validate(); err != nil {
		return err
	}
	if c.Gradient {
		if len(c.GradientColors) < 2 {
			return fmt.Errorf("gradient needs at least two colors")
		}
		if len(c.GradientColors) > 8 {
			return fmt.Errorf("gradient can have at most eight colors")
		}
	}
	if c.HorizontalGradient {
		if len(c.HorizontalColors) < 2 {
			return fmt.Errorf("horizontal_gradient needs at least two colors")
		}
		if len(c.HorizontalColors) > 8 {
			return fmt.Errorf("horizontal_gradient can have at most eight colors")
		}
	}
	for _, s := range append(append([]string{c.Foreground, c.Background}, c.GradientColors...), c.HorizontalColors...) {
		if !validColor(s) {
			return fmt.Errorf("invalid color %q: use a name or '#rrggbb'", s)
		}
	}
	return nil
}

// validColor accepts cava's eight names, "default", and a six-digit hex code.
func validColor(s string) bool {
	switch s {
	case "default", "black", "red", "green", "yellow", "blue", "magenta", "cyan", "white":
		return true
	}
	if len(s) != 7 || s[0] != '#' {
		return false
	}
	for _, r := range s[1:] {
		if !strings.ContainsRune("0123456789abcdefABCDEF", r) {
			return false
		}
	}
	return true
}

// ConfigPath returns the path cava reads by default:
// $XDG_CONFIG_HOME/cava/config, or ~/.config/cava/config.
func ConfigPath() string {
	if dir := os.Getenv("XDG_CONFIG_HOME"); dir != "" {
		return filepath.Join(dir, "cava", "config")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".config", "cava", "config")
}

// LoadConfigFile reads an INI file into c. A missing file is not an error —
// cava writes one on first run and works without it — so callers get the
// defaults untouched.
func LoadConfigFile(c *Config, path string) error {
	f, err := os.Open(path) //nolint:gosec // the path is the user's own config
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	defer f.Close() //nolint:errcheck
	return LoadConfig(c, f)
}

// LoadConfig reads INI text into c, overwriting only the keys it finds.
//
// The format is cava's: [sections], key = value, ';' and '#' comments, and
// values optionally wrapped in single quotes. Unknown keys are an error,
// because a typo in a config file is otherwise invisible.
func LoadConfig(c *Config, r io.Reader) error {
	// The eq section has arbitrary key names in file order, so it is collected
	// separately and appended once the whole file has been read.
	type eqEntry struct {
		key   string
		value float64
	}
	var eq []eqEntry

	section := ""
	sc := bufio.NewScanner(r)
	for line := 1; sc.Scan(); line++ {
		text := strings.TrimSpace(sc.Text())
		if text == "" || strings.HasPrefix(text, ";") || strings.HasPrefix(text, "#") {
			continue
		}
		if strings.HasPrefix(text, "[") && strings.HasSuffix(text, "]") {
			section = strings.ToLower(strings.TrimSpace(text[1 : len(text)-1]))
			continue
		}
		key, value, ok := strings.Cut(text, "=")
		if !ok {
			return fmt.Errorf("line %d: expected 'key = value', got %q", line, text)
		}
		key = strings.ToLower(strings.TrimSpace(key))
		value = strings.Trim(strings.TrimSpace(value), "'\"")

		if section == "eq" {
			v, err := strconv.ParseFloat(value, 64)
			if err != nil {
				return fmt.Errorf("line %d: eq key %q: %w", line, key, err)
			}
			eq = append(eq, eqEntry{key, v})
			continue
		}
		if err := c.set(section, key, value); err != nil {
			return fmt.Errorf("line %d: %w", line, err)
		}
	}
	if err := sc.Err(); err != nil {
		return err
	}

	if len(eq) > 0 {
		// cava reads the eq keys in whatever order its INI parser hands them
		// back, which for numeric keys is the order they appear. Sorting by
		// numeric key where possible makes '10' come after '9' rather than
		// after '1', which is what anyone writing the file means.
		sort.SliceStable(eq, func(i, j int) bool {
			a, aerr := strconv.Atoi(eq[i].key)
			b, berr := strconv.Atoi(eq[j].key)
			if aerr != nil || berr != nil {
				return false
			}
			return a < b
		})
		c.UserEQ = c.UserEQ[:0]
		for _, e := range eq {
			c.UserEQ = append(c.UserEQ, e.value)
		}
	}
	return nil
}

// ignoredKeys are cava keys that belong to features this port does not have.
// They are recorded and skipped rather than rejected, so an existing config
// file loads without being edited.
var ignoredKeys = map[string]bool{
	"general:overshoot":           true,
	"general:bar_height":          true,
	"general:live-config":         true,
	"general:zero_test":           true,
	"general:non_zero_test":       true,
	"input:autoconnect":           true,
	"input:active":                true,
	"input:remix":                 true,
	"input:virtual":               true,
	"output:split_stereo":         true,
	"output:left_bottom":          true,
	"output:waveform":             true,
	"output:sdl_width":            true,
	"output:sdl_height":           true,
	"output:sdl_x":                true,
	"output:sdl_y":                true,
	"output:sdl_full_screen":      true,
	"output:sdl_glsl_gain":        true,
	"output:vertex_shader":        true,
	"output:fragment_shader":      true,
	"output:continuous_rendering": true,
	"output:disable_blanking":     true,
	"color:theme":                 true,
}

// set applies one key. It is also what the -o command line flag uses, so a
// setting can be overridden without a file.
func (c *Config) set(section, key, value string) error {
	full := section + ":" + key
	if ignoredKeys[full] {
		c.Ignored = append(c.Ignored, full)
		return nil
	}

	atoi := func(dst *int) error {
		v, err := strconv.Atoi(value)
		if err != nil {
			return fmt.Errorf("%s: %w", full, err)
		}
		*dst = v
		return nil
	}
	atof := func(dst *float64) error {
		v, err := strconv.ParseFloat(value, 64)
		if err != nil {
			return fmt.Errorf("%s: %w", full, err)
		}
		*dst = v
		return nil
	}
	atob := func(dst *bool) error {
		v, err := strconv.Atoi(value)
		if err != nil {
			return fmt.Errorf("%s: %w", full, err)
		}
		*dst = v != 0
		return nil
	}

	switch full {
	case "general:framerate":
		return atoi(&c.Framerate)
	case "general:autosens":
		return atoi(&c.Autosens)
	case "general:sensitivity":
		return atof(&c.Sensitivity)
	case "general:scaling":
		switch value {
		case "linear":
			c.Scaling = ScalingLinear
		case "decibel":
			c.Scaling = ScalingDecibel
		default:
			return fmt.Errorf("general:scaling must be 'linear' or 'decibel', got %q", value)
		}
	case "general:bars":
		return atoi(&c.Bars)
	case "general:bar_width":
		return atoi(&c.BarWidth)
	case "general:bar_spacing":
		return atoi(&c.BarSpacing)
	case "general:center_align":
		return atob(&c.CenterAlign)
	case "general:max_height":
		var v int
		if err := atoi(&v); err != nil {
			return err
		}
		c.MaxHeight = float64(v) / 100
	case "general:lower_cutoff_freq":
		return atoi(&c.LowerCutOff)
	case "general:higher_cutoff_freq":
		return atoi(&c.HigherCutOff)
	case "general:sleep_timer":
		return atoi(&c.SleepTimer)
	case "general:draw_and_quit":
		return atoi(&c.DrawAndQuit)

	case "input:method":
		c.InputMethod = value
	case "input:source":
		c.Source = value
	case "input:sample_rate":
		return atoi(&c.SampleRate)
	case "input:sample_bits":
		return atoi(&c.SampleBits)
	case "input:sample_float":
		return atob(&c.SampleFloat)
	case "input:channels":
		return atoi(&c.InputChannels)

	case "output:method":
		c.OutputMethod = value
	case "output:orientation":
		c.Orientation = value
	case "output:channels":
		c.Channels = value
	case "output:mono_option":
		c.MonoOption = value
	case "output:reverse":
		return atob(&c.Reverse)
	case "output:raw_target":
		c.RawTarget = value
	case "output:data_format":
		c.DataFormat = value
	case "output:bit_format":
		var v string
		v = strings.TrimSuffix(value, "bit")
		n, err := strconv.Atoi(v)
		if err != nil {
			return fmt.Errorf("%s: %w", full, err)
		}
		c.BitFormat = n
	case "output:ascii_max_range":
		return atoi(&c.AsciiMaxRange)
	case "output:bar_delimiter":
		var v int
		if err := atoi(&v); err != nil {
			return err
		}
		c.BarDelimiter = byte(v) //nolint:gosec // a delimiter is one byte by definition
	case "output:frame_delimiter":
		var v int
		if err := atoi(&v); err != nil {
			return err
		}
		c.FrameDelimiter = byte(v) //nolint:gosec // a delimiter is one byte by definition
	case "output:show_idle_bar_heads":
		return atob(&c.ShowIdleBarHeads)
	case "output:xaxis":
		c.XAxis = value
	case "output:synchronized_sync":
		return atob(&c.SynchronizedSync)

	case "color:foreground":
		c.Foreground = value
	case "color:background":
		c.Background = value
	case "color:gradient":
		return atob(&c.Gradient)
	case "color:horizontal_gradient":
		return atob(&c.HorizontalGradient)
	case "color:blend_direction":
		c.BlendDirection = value

	case "smoothing:monstercat":
		return atof(&c.Monstercat)
	case "smoothing:waves":
		return atoi(&c.Waves)
	case "smoothing:noise_reduction":
		var v float64
		if err := atof(&v); err != nil {
			return err
		}
		c.NoiseReduction = v / 100

	default:
		// The gradient colors are numbered keys rather than a fixed set.
		if n, ok := gradientIndex(key, "gradient_color_"); ok && section == "color" {
			c.GradientColors = setColorAt(c.GradientColors, n, value)
			return nil
		}
		if n, ok := gradientIndex(key, "horizontal_gradient_color_"); ok && section == "color" {
			c.HorizontalColors = setColorAt(c.HorizontalColors, n, value)
			return nil
		}
		return fmt.Errorf("unknown key %q", full)
	}
	return nil
}

// gradientIndex parses "gradient_color_3" into 3.
func gradientIndex(key, prefix string) (int, bool) {
	rest, ok := strings.CutPrefix(key, prefix)
	if !ok {
		return 0, false
	}
	n, err := strconv.Atoi(rest)
	if err != nil || n < 1 || n > 8 {
		return 0, false
	}
	return n, true
}

// setColorAt writes the nth (1-based) color, growing the slice with the
// previous color rather than with an empty string so a sparse config does not
// produce an invalid one.
func setColorAt(list []string, n int, value string) []string {
	for len(list) < n {
		fill := "default"
		if len(list) > 0 {
			fill = list[len(list)-1]
		}
		list = append(list, fill)
	}
	list[n-1] = value
	return list
}

// Set applies a "section:key=value" override, which is what cava's -o flag
// does. It is exported so the command can share the config file's parser and
// its error messages.
func (c *Config) Set(assignment string) error {
	lhs, value, ok := strings.Cut(assignment, "=")
	if !ok {
		return fmt.Errorf("expected section.key=value, got %q", assignment)
	}
	section, key, ok := strings.Cut(lhs, ".")
	if !ok {
		return fmt.Errorf("expected section.key=value, got %q", assignment)
	}
	return c.set(strings.ToLower(strings.TrimSpace(section)),
		strings.ToLower(strings.TrimSpace(key)),
		strings.Trim(strings.TrimSpace(value), "'\""))
}
