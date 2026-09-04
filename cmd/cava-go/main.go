// Command cava-go is a console audio visualiser: a Go port of cava by Karl
// Stavestrand.
//
// It reads raw PCM from a fifo or from standard input and draws it as a
// spectrum. The audio backends the original has — ALSA, PulseAudio, PipeWire,
// PortAudio, JACK, sndio, OSS, CoreAudio, WASAPI — all need C libraries and
// are not here; feed it a fifo instead:
//
//	mkfifo /tmp/mpd.fifo                       # and point mpd's fifo output at it
//	cava-go -source /tmp/mpd.fifo
//
//	ffmpeg -i track.flac -f s16le -ar 44100 -ac 2 - | cava-go -input stdin
//
// Configuration is cava's own INI file, read from $XDG_CONFIG_HOME/cava/config
// or ~/.config/cava/config unless -p says otherwise, and any key in it can be
// set on the command line with -o.
package main

import (
	"flag"
	"fmt"
	"os"
	"os/signal"
	"runtime/debug"
	"strings"
	"sync"
	"syscall"

	"github.com/gdamore/tcell/v3"

	"github.com/0magnet/cava-go"
	"github.com/0magnet/cava-go/render"
)

// version is stamped by the linker for a release build and falls back to
// whatever the module system knows.
var version = ""

func buildVersion() string {
	if version != "" {
		return version
	}
	if info, ok := debug.ReadBuildInfo(); ok && info.Main.Version != "" {
		return info.Main.Version
	}
	return "devel"
}

// overrides collects repeated -o flags in the order they were given.
type overrides []string

func (o *overrides) String() string { return strings.Join(*o, ",") }

func (o *overrides) Set(v string) error {
	*o = append(*o, v)
	return nil
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "cava-go:", err)
		os.Exit(1)
	}
}

func run() error {
	var (
		configPath  = flag.String("p", "", "path to the config file (default: $XDG_CONFIG_HOME/cava/config)")
		showVersion = flag.Bool("v", false, "print the version and exit")
		input       = flag.String("input", "", "input method: fifo or stdin")
		source      = flag.String("source", "", "fifo path to read PCM from")
		rate        = flag.Int("rate", 0, "input sample rate in Hz")
		channels    = flag.Int("channels", 0, "input channels, 1 or 2")
		bits        = flag.Int("bits", 0, "input sample size in bits: 8, 16, 24 or 32")
		asFloat     = flag.Bool("float", false, "input samples are 32-bit IEEE floats")
		bars        = flag.Int("bars", -1, "number of bars, 0 fills the terminal")
		framerate   = flag.Int("framerate", 0, "frames per second")
		raw         = flag.Bool("raw", false, "write bar heights to stdout instead of drawing")
		set         overrides
	)
	flag.Var(&set, "o", "set a config key, as section.key=value; may be repeated")
	flag.Usage = usage
	flag.Parse()

	if *showVersion {
		fmt.Printf("cava-go %s (a port of cava by Karl Stavestrand)\n", buildVersion())
		return nil
	}

	cfg := cava.DefaultConfig()
	path := *configPath
	if path == "" {
		path = cava.ConfigPath()
	}
	if path != "" {
		if err := cava.LoadConfigFile(cfg, path); err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
	}

	// The config file first, then -o, then the specific flags: the more
	// precise the way it was said, the later it is applied.
	for _, s := range set {
		if err := cfg.Set(s); err != nil {
			return err
		}
	}
	if *input != "" {
		cfg.InputMethod = *input
	}
	if *source != "" {
		cfg.Source = *source
		if cfg.InputMethod == "stdin" && *input == "" {
			cfg.InputMethod = "fifo"
		}
	}
	if *rate != 0 {
		cfg.SampleRate = *rate
	}
	if *channels != 0 {
		cfg.InputChannels = *channels
	}
	if *bits != 0 {
		cfg.SampleBits = *bits
	}
	if *asFloat {
		cfg.SampleFloat = true
		if *bits == 0 {
			cfg.SampleBits = 32
		}
	}
	if *bars >= 0 {
		cfg.Bars = *bars
	}
	if *framerate != 0 {
		cfg.Framerate = *framerate
	}
	if *raw {
		cfg.OutputMethod = "raw"
	}

	if err := cfg.Validate(); err != nil {
		return err
	}
	for _, key := range cfg.Ignored {
		fmt.Fprintf(os.Stderr, "cava-go: ignoring %s, which belongs to a feature this port does not have\n", key)
	}

	// Ctrl-C has to reach the loop rather than the process, or a terminal
	// screen is left in raw mode with the cursor hidden.
	//
	// Closing it on the way out as well as on a signal is what unwinds the
	// reader goroutine: it waits on the playback clock, and a wait nobody ever
	// cancels is a goroutine that outlives the program that made it.
	stop := make(chan struct{})
	halt := sync.OnceFunc(func() { close(stop) })
	defer halt()
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-sig
		halt()
	}()

	// cava's own buffer size: about 185 ms at 44100 stereo.
	stream := cava.NewStream(16384)
	go cava.PumpSource(stream, cfg.Source, cava.Input{
		Format:       cfg.Format(),
		Rate:         cfg.SampleRate,
		Channels:     cfg.InputChannels,
		FrameSamples: 512 * cfg.InputChannels,
	}, stop)

	if cfg.OutputMethod == "raw" {
		return rawTo(cfg, stream, stop)
	}

	screen, err := tcell.NewScreen()
	if err != nil {
		return err
	}
	if err := screen.Init(); err != nil {
		return err
	}
	defer screen.Fini()
	screen.HideCursor()

	return render.Run(screen, cfg, stream, stop)
}

func usage() {
	fmt.Fprintf(os.Stderr, `cava-go %s — console audio visualiser, a port of cava by Karl Stavestrand

usage: cava-go [flags]

  cava-go -source /tmp/mpd.fifo
  ffmpeg -i track.flac -f s16le -ar 44100 -ac 2 - | cava-go -input stdin
  cava-go -raw -o output.data_format=ascii -bars 20

flags:
`, buildVersion())
	flag.PrintDefaults()
	fmt.Fprintf(os.Stderr, `
keys:
  q, Escape, Ctrl-C   quit
  up, down            sensitivity
  left, right         bar width

Only the 'fifo' and 'stdin' input methods exist in this port; the rest need C
libraries. See the README for what that leaves out.
`)
}

// rawTo opens the configured raw target and hands it to the library's raw
// loop. Only the choice of file belongs to the command; the pipeline itself is
// in the package, where it can be tested.
func rawTo(cfg *cava.Config, stream *cava.Stream, stop <-chan struct{}) error {
	target := os.Stdout
	if cfg.RawTarget != "" && cfg.RawTarget != "/dev/stdout" {
		f, err := os.OpenFile(cfg.RawTarget, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600) //nolint:gosec // the path is the user's own config
		if err != nil {
			return err
		}
		defer f.Close() //nolint:errcheck
		target = f
	}
	return cava.RunRaw(target, cfg, stream, stop)
}
