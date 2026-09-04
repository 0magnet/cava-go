package cava

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// realConfig is a cava config file as someone would actually write one, with
// the keys this port implements uncommented. Loading it is the test that the
// parser handles what cava's does: sections, comments in both styles, quoted
// values, the 'bit' suffix on bit_format, and an [eq] section with arbitrary
// keys.
const realConfig = `
## Configuration file for CAVA.

[general]
framerate = 30
autosens = 1
sensitivity = 120
bars = 40
bar_width = 3
bar_spacing = 1
lower_cutoff_freq = 30
higher_cutoff_freq = 16000
max_height = 80
scaling = decibel

[input]
method = fifo
source = /tmp/mpd.fifo
sample_rate = 48000
sample_bits = 16
channels = 2

[output]
method = noncurses
channels = mono
mono_option = left
reverse = 1
xaxis = frequency
; commented out, must not take effect
; bar_width = 99
bit_format = 16bit

[color]
foreground = '#33ffff'
background = default
gradient = 1
gradient_color_1 = '#59cc33'
gradient_color_2 = '#cc3333'

[smoothing]
monstercat = 1.5
waves = 0
noise_reduction = 50

[eq]
1 = 1
2 = 1.5
3 = 0.5
`

func TestLoadRealConfig(t *testing.T) {
	c := DefaultConfig()
	if err := LoadConfig(c, strings.NewReader(realConfig)); err != nil {
		t.Fatal(err)
	}
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name      string
		got, want any
	}{
		{"framerate", c.Framerate, 30},
		{"sensitivity", c.Sensitivity, 120.0},
		{"bars", c.Bars, 40},
		{"bar_width", c.BarWidth, 3},
		{"lower_cutoff_freq", c.LowerCutOff, 30},
		{"higher_cutoff_freq", c.HigherCutOff, 16000},
		{"max_height", c.MaxHeight, 0.8},
		{"scaling", c.Scaling, ScalingDecibel},
		{"sample_rate", c.SampleRate, 48000},
		{"channels", c.InputChannels, 2},
		{"output channels", c.Channels, "mono"},
		{"mono_option", c.MonoOption, "left"},
		{"reverse", c.Reverse, true},
		{"xaxis", c.XAxis, "frequency"},
		{"bit_format", c.BitFormat, 16},
		{"foreground", c.Foreground, "#33ffff"},
		{"gradient", c.Gradient, true},
		{"monstercat", c.Monstercat, 1.5},
		{"noise_reduction", c.NoiseReduction, 0.5},
	} {
		if tc.got != tc.want {
			t.Errorf("%s = %v, want %v", tc.name, tc.got, tc.want)
		}
	}

	if len(c.GradientColors) != 2 || c.GradientColors[0] != "#59cc33" || c.GradientColors[1] != "#cc3333" {
		t.Errorf("gradient colors = %v", c.GradientColors)
	}
	if len(c.UserEQ) != 3 || c.UserEQ[0] != 1 || c.UserEQ[1] != 1.5 || c.UserEQ[2] != 0.5 {
		t.Errorf("eq = %v", c.UserEQ)
	}
	if c.OutputChannels() != 1 {
		t.Errorf("mono output should be one channel, got %d", c.OutputChannels())
	}
}

// TestUnsupportedMethodsSayWhy is the point of keeping the names of the
// backends this port does not have: someone with an existing config should be
// told what is missing, not told their config is wrong.
func TestUnsupportedMethodsSayWhy(t *testing.T) {
	for method, want := range map[string]string{
		"pulse":     "libpulse",
		"alsa":      "libasound",
		"pipewire":  "libpipewire",
		"portaudio": "libportaudio",
		"jack":      "libjack",
		"sndio":     "libsndio",
		"oss":       "OSS",
		"coreaudio": "CoreAudio",
		"shmem":     "squeezelite",
	} {
		c := DefaultConfig()
		c.InputMethod = method
		err := c.Validate()
		if err == nil {
			t.Errorf("%s was accepted", method)
			continue
		}
		if !strings.Contains(err.Error(), want) {
			t.Errorf("%s: error %q does not mention %q", method, err, want)
		}
		if !strings.Contains(err.Error(), "fifo") {
			t.Errorf("%s: error %q does not say what to use instead", method, err)
		}
	}

	for _, method := range []string{"ncurses", "sdl", "sdl_glsl", "noritake"} {
		c := DefaultConfig()
		c.OutputMethod = method
		if err := c.Validate(); err == nil {
			t.Errorf("output method %s was accepted", method)
		}
	}
}

// TestUnknownKeyIsAnError: a typo in a config file is otherwise silent, and
// the symptom is a setting that does nothing.
func TestUnknownKeyIsAnError(t *testing.T) {
	c := DefaultConfig()
	err := LoadConfig(c, strings.NewReader("[general]\nframrate = 30\n"))
	if err == nil {
		t.Fatal("a misspelled key was accepted")
	}
	if !strings.Contains(err.Error(), "framrate") {
		t.Errorf("error %q does not name the key", err)
	}
}

// TestIgnoredKeysAreRecorded: keys that belong to features this port does not
// have must load without complaint, but be reportable.
func TestIgnoredKeysAreRecorded(t *testing.T) {
	c := DefaultConfig()
	in := "[output]\nsdl_width = 1024\nwaveform = 1\n[general]\novershoot = 20\n"
	if err := LoadConfig(c, strings.NewReader(in)); err != nil {
		t.Fatal(err)
	}
	if len(c.Ignored) != 3 {
		t.Errorf("ignored = %v, want three keys", c.Ignored)
	}
}

func TestValidateRejectsBadValues(t *testing.T) {
	for _, tc := range []struct {
		name  string
		apply func(*Config)
	}{
		{"framerate zero", func(c *Config) { c.Framerate = 0 }},
		{"max_height above 100%", func(c *Config) { c.MaxHeight = 1.5 }},
		{"max_height negative", func(c *Config) { c.MaxHeight = -0.1 }},
		{"three channels", func(c *Config) { c.InputChannels = 3 }},
		{"12-bit samples", func(c *Config) { c.SampleBits = 12 }},
		{"float at 16 bits", func(c *Config) { c.SampleBits = 16; c.SampleFloat = true }},
		{"one gradient color", func(c *Config) { c.Gradient = true; c.GradientColors = []string{"#ffffff"} }},
		{"nine gradient colors", func(c *Config) {
			c.Gradient = true
			c.GradientColors = []string{"#1", "#2", "#3", "#4", "#5", "#6", "#7", "#8", "#9"}
		}},
		{"invalid color", func(c *Config) { c.Foreground = "burgundy" }},
		{"short hex color", func(c *Config) { c.Foreground = "#fff" }},
		{"bit_format 12", func(c *Config) { c.BitFormat = 12 }},
		{"orientation left", func(c *Config) { c.Orientation = "left" }},
		{"xaxis note", func(c *Config) { c.XAxis = "note" }},
		{"data_format json", func(c *Config) { c.DataFormat = "json" }},
	} {
		c := DefaultConfig()
		tc.apply(c)
		if err := c.Validate(); err == nil {
			t.Errorf("%s was accepted", tc.name)
		}
	}
}

// TestSetOverride covers the -o flag, which shares the file's parser so that
// the two cannot drift apart.
func TestSetOverride(t *testing.T) {
	c := DefaultConfig()
	for _, s := range []string{
		"general.bars=20",
		"smoothing.noise_reduction=90",
		"color.foreground='#ff0000'",
		"output.method=raw",
	} {
		if err := c.Set(s); err != nil {
			t.Fatalf("%s: %v", s, err)
		}
	}
	if c.Bars != 20 || c.NoiseReduction != 0.9 || c.Foreground != "#ff0000" || c.OutputMethod != "raw" {
		t.Errorf("overrides gave bars=%d noise=%v fg=%s method=%s", c.Bars, c.NoiseReduction, c.Foreground, c.OutputMethod)
	}
	if err := c.Set("nonsense"); err == nil {
		t.Error("an override with no '=' was accepted")
	}
	if err := c.Set("bars=20"); err == nil {
		t.Error("an override with no section was accepted")
	}
}

// TestMissingConfigFileIsFine: cava runs with no config at all, and so should
// this.
func TestMissingConfigFileIsFine(t *testing.T) {
	c := DefaultConfig()
	if err := LoadConfigFile(c, filepath.Join(t.TempDir(), "nothing", "config")); err != nil {
		t.Errorf("a missing config file was an error: %v", err)
	}
	if c.Framerate != 60 {
		t.Errorf("defaults were disturbed: framerate = %d", c.Framerate)
	}
}

func TestLoadConfigFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config")
	if err := os.WriteFile(path, []byte("[general]\nbars = 12\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	c := DefaultConfig()
	if err := LoadConfigFile(c, path); err != nil {
		t.Fatal(err)
	}
	if c.Bars != 12 {
		t.Errorf("bars = %d, want 12", c.Bars)
	}
}

// TestStdinMethodRewritesTheSource: "stdin" is not one of cava's input
// methods, so it needs to behave sensibly on its own terms.
func TestStdinMethodRewritesTheSource(t *testing.T) {
	c := DefaultConfig()
	c.InputMethod = "stdin"
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	if c.Source != "-" {
		t.Errorf("source = %q, want %q", c.Source, "-")
	}
}
