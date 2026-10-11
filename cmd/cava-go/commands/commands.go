// Package commands is the cava-go command: a console audio visualizer, a Go
// port of cava by Karl Stavestrand.
//
// It reads raw PCM from a fifo or from standard input and draws it as a
// spectrum. The audio backends the original has — ALSA, PulseAudio, PipeWire,
// PortAudio, JACK, sndio, OSS, CoreAudio, WASAPI — all need C libraries and
// are not here; feed it a fifo instead:
//
//	mkfifo /tmp/mpd.fifo                       # and point mpd's fifo output at it
//	cava-go --source /tmp/mpd.fifo
//
//	ffmpeg -i track.flac -f s16le -ar 44100 -ac 2 - | cava-go --input stdin
//
// Configuration is cava's own INI file, read from $XDG_CONFIG_HOME/cava/config
// or ~/.config/cava/config unless --config says otherwise, and any key in it
// can be set on the command line with -o.
package commands

import (
	"fmt"
	"os"
	"os/signal"
	"runtime/debug"
	"sync"
	"syscall"

	"github.com/gdamore/tcell/v3"
	"github.com/spf13/cobra"

	"github.com/0magnet/calvin/clihelp"

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

var (
	configPath  string
	showVersion bool
	input       string
	source      string
	rate        int
	channels    int
	bits        int
	asFloat     bool
	bars        int
	framerate   int
	raw         bool
	set         []string
)

// RootCmd is the cava-go command.
var RootCmd = &cobra.Command{
	Use:   "cava-go",
	Short: "console audio visualizer, a port of cava by Karl Stavestrand",
	Long: `Console audio visualizer, a port of cava by Karl Stavestrand.

  cava-go --source /tmp/mpd.fifo
  ffmpeg -i track.flac -f s16le -ar 44100 -ac 2 - | cava-go --input stdin
  cava-go --raw -o output.data_format=ascii --bars 20

keys:
  q, Escape, Ctrl-C   quit
  up, down            sensitivity
  left, right         bar width

Only the 'fifo' and 'stdin' input methods exist in this port; the rest need C
libraries. See the README for what that leaves out.`,
	Args:          cobra.NoArgs,
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE: func(_ *cobra.Command, _ []string) error {
		return run()
	},
}

func init() {
	f := RootCmd.Flags()
	f.StringVarP(&configPath, "config", "p", "", "path to the config file (default: $XDG_CONFIG_HOME/cava/config)")
	f.BoolVarP(&showVersion, "version", "v", false, "print the version and exit")
	f.StringVar(&input, "input", "", "input method: fifo or stdin")
	f.StringVar(&source, "source", "", "fifo path to read PCM from")
	f.IntVar(&rate, "rate", 0, "input sample rate in Hz")
	f.IntVar(&channels, "channels", 0, "input channels, 1 or 2")
	f.IntVar(&bits, "bits", 0, "input sample size in bits: 8, 16, 24 or 32")
	f.BoolVar(&asFloat, "float", false, "input samples are 32-bit IEEE floats")
	f.IntVar(&bars, "bars", -1, "number of bars, 0 fills the terminal")
	f.IntVar(&framerate, "framerate", 0, "frames per second")
	f.BoolVar(&raw, "raw", false, "write bar heights to stdout instead of drawing")
	f.StringArrayVarP(&set, "set", "o", nil, "set a config key, as section.key=value; may be repeated")
	clihelp.Init(RootCmd, "cava-go")
}

// Execute runs the command and exits non-zero on error.
func Execute() {
	if err := RootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "cava-go:", err)
		os.Exit(1)
	}
}

func run() error {
	if showVersion {
		fmt.Printf("cava-go %s (a port of cava by Karl Stavestrand)\n", buildVersion())
		return nil
	}

	cfg := cava.DefaultConfig()
	path := configPath
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
	if input != "" {
		cfg.InputMethod = input
	}
	if source != "" {
		cfg.Source = source
		if cfg.InputMethod == "stdin" && input == "" {
			cfg.InputMethod = "fifo"
		}
	}
	if rate != 0 {
		cfg.SampleRate = rate
	}
	if channels != 0 {
		cfg.InputChannels = channels
	}
	if bits != 0 {
		cfg.SampleBits = bits
	}
	if asFloat {
		cfg.SampleFloat = true
		if bits == 0 {
			cfg.SampleBits = 32
		}
	}
	if bars >= 0 {
		cfg.Bars = bars
	}
	if framerate != 0 {
		cfg.Framerate = framerate
	}
	if raw {
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
