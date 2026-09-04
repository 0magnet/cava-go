// Package cava is a Go port of cavacore, the processing engine of
// [cava] — Console-based Audio Visualizer for ALSA, by Karl Stavestrand.
//
// The engine takes interleaved PCM samples and returns one value per bar,
// nominally between 0 and 1. What it does between those two points is what
// makes a spectrum look like cava rather than like a raw FFT:
//
//   - Two transforms rather than one. Bass needs frequency resolution and
//     treble needs time resolution, and a single window cannot have both, so
//     bars below 100 Hz are read from a transform twice as long as the one
//     everything above them is read from.
//   - A logarithmic band distribution, because that is how pitch is heard.
//   - An integral filter and a gravity fall-off, because the raw output of an
//     FFT jitters far too much to look like anything.
//   - Sensitivity that follows the signal, so quiet passages still fill the
//     screen and loud ones do not clip flat.
//
// The arithmetic is transcribed from cavacore.c, including the places where
// the original computes in C float rather than double or divides two ints —
// see [Init]. That is deliberate: bar boundaries fall on integer FFT bins, and
// a bin's worth of drift is visible.
//
// [cava]: https://github.com/karlstav/cava
package cava

import (
	"fmt"
	"math"
	"math/cmplx"

	"github.com/0magnet/go-dsp/fft"
)

// ScalingMode selects how band energy becomes a bar height.
type ScalingMode int

const (
	// ScalingLinear is cava's legacy scaling: the summed magnitudes times a
	// hard-coded per-bar equaliser.
	ScalingLinear ScalingMode = iota
	// ScalingDecibel converts amplitude to decibels over a 70 dB range, which
	// resembles perceived loudness more closely and is the better choice when
	// there are many bars.
	ScalingDecibel
)

// bassCutOff is the frequency below which a bar is read from the long
// transform. Hard-coded in the original.
const bassCutOff = 100

// maxDecibel is the dB range decibel scaling maps onto 0..1.
const maxDecibel = 70

// Plan holds the buffers and the band layout for one visualisation. Create it
// with [Init] and drive it with [Plan.Execute]. It is not safe for concurrent
// use; one goroutine owns it.
type Plan struct {
	// fftBassBufferSize is the length of the long transform, used for bars
	// below bassCutOff. It is twice fftBufferSize.
	fftBassBufferSize int
	// fftBufferSize is the length of the short transform, used for everything
	// above bassCutOff.
	fftBufferSize int

	numberOfBars  int
	audioChannels int
	rate          int

	// bassCutOffBar is the number of bars taken from the long transform. Bars
	// with an index below it are bass.
	bassCutOffBar int

	// sensInit is true until the first overshoot. While it holds, sensitivity
	// climbs much faster, so a fresh plan reaches a usable range in a second
	// or so rather than a minute.
	sensInit bool
	autosens int
	// frameSkip counts executions that carried no new samples, so the
	// framerate estimate does not collapse when the input starves.
	frameSkip   int
	scalingMode ScalingMode

	sens           float64
	framerate      float64
	noiseReduction float64

	// inputBuffer is the running history of input, newest first, interleaved
	// exactly as it arrived.
	inputBuffer []float64

	inBassL, inBassR []float64
	inL, inR         []float64

	outBassL, outBassR []complex128
	outL, outR         []complex128

	// bassMultiplier and multiplier are the Hann windows for the two
	// transforms, computed once.
	bassMultiplier []float64
	multiplier     []float64

	// The smoothing state, one entry per bar per channel.
	prevOut []float64
	mem     []float64
	peak    []float64
	fall    []float64

	// eq is the per-bar normalisation applied under linear scaling.
	eq []float64

	// cutOffFrequency is the lower edge of each bar in Hz, with one extra
	// entry for the upper edge of the last. Held as float32 because the
	// original does and the rounding reaches the bin indices below.
	cutOffFrequency []float32

	// lowerCutOff and upperCutOff are inclusive FFT bin ranges per bar.
	lowerCutOff []int
	upperCutOff []int
}

// Init plans a visualisation.
//
// numberOfBars is per channel. rate is the sample rate in Hz and channels is 1
// or 2; more than two is not supported, as in the original. autosens enables
// adaptive sensitivity — 0 off, 1 normal, higher values climb faster.
// noiseReduction runs from 0 (fast and noisy) to 1 (slow and smooth); cava's
// default is 0.77. lowCutOff and highCutOff bound the displayed spectrum in Hz
// and must satisfy Nyquist.
//
// The transform length follows the sample rate: 512 points up to 8125 Hz,
// doubling at each of a fixed set of thresholds, so 44100 Hz gets a 4096-point
// short transform and an 8192-point long one. Bars cannot outnumber the bins
// of the short transform.
func Init(numberOfBars, rate, channels, autosens int, noiseReduction float64, lowCutOff, highCutOff int, scaling ScalingMode) (*Plan, error) {
	if channels < 1 || channels > 2 {
		return nil, fmt.Errorf("cava: illegal number of channels: %d, number of channels supported are 1 and 2", channels)
	}
	if rate < 1 || rate > 384000 {
		return nil, fmt.Errorf("cava: illegal sample rate: %d", rate)
	}

	fftBufferSize := 512
	switch {
	case rate > 300000:
		fftBufferSize *= 64
	case rate > 150000:
		fftBufferSize *= 32
	case rate > 75000:
		fftBufferSize *= 16
	case rate > 32500:
		fftBufferSize *= 8
	case rate > 16250:
		fftBufferSize *= 4
	case rate > 8125:
		fftBufferSize *= 2
	}

	if numberOfBars < 1 {
		return nil, fmt.Errorf("cava: illegal number of bars: %d, number of bars must be a positive integer", numberOfBars)
	}
	if numberOfBars > fftBufferSize/2+1 {
		return nil, fmt.Errorf("cava: illegal number of bars: %d, for %d sample rate number of bars can't be more than %d",
			numberOfBars, rate, fftBufferSize/2+1)
	}
	if lowCutOff < 1 || highCutOff < 1 {
		return nil, fmt.Errorf("cava: low_cut_off must be a positive value")
	}
	if lowCutOff >= highCutOff {
		return nil, fmt.Errorf("cava: high_cut_off must be higher than low_cut_off")
	}
	if highCutOff > rate/2 {
		return nil, fmt.Errorf("cava: high_cut_off can't be higher than sample rate / 2 (Nyquist sampling theorem)")
	}
	if scaling != ScalingLinear && scaling != ScalingDecibel {
		return nil, fmt.Errorf("cava: unknown scaling mode: %d", scaling)
	}

	p := &Plan{
		fftBassBufferSize: fftBufferSize * 2,
		fftBufferSize:     fftBufferSize,
		numberOfBars:      numberOfBars,
		audioChannels:     channels,
		rate:              rate,
		autosens:          autosens,
		sensInit:          true,
		sens:              1.0,
		// Seeded at 75 rather than 0 because the estimate is a decaying
		// average: starting from zero would make the first second of gravity
		// and sensitivity behave as if the frame rate were impossibly low.
		framerate:      75,
		frameSkip:      1,
		noiseReduction: noiseReduction,
		scalingMode:    scaling,
	}

	p.inputBuffer = make([]float64, p.fftBassBufferSize*channels)

	p.lowerCutOff = make([]int, numberOfBars+1)
	p.upperCutOff = make([]int, numberOfBars+1)
	p.eq = make([]float64, numberOfBars+1)
	p.cutOffFrequency = make([]float32, numberOfBars+1)

	n := numberOfBars * channels
	p.fall = make([]float64, n)
	p.mem = make([]float64, n)
	p.peak = make([]float64, n)
	p.prevOut = make([]float64, n)

	// Hann windows. Note the divisor is length-1, the symmetric form, which is
	// what the original uses.
	p.bassMultiplier = make([]float64, p.fftBassBufferSize)
	for i := range p.bassMultiplier {
		p.bassMultiplier[i] = 0.5 * (1 - math.Cos(2*math.Pi*float64(i)/float64(p.fftBassBufferSize-1)))
	}
	p.multiplier = make([]float64, p.fftBufferSize)
	for i := range p.multiplier {
		p.multiplier[i] = 0.5 * (1 - math.Cos(2*math.Pi*float64(i)/float64(p.fftBufferSize-1)))
	}

	p.inBassL = make([]float64, p.fftBassBufferSize)
	p.inL = make([]float64, p.fftBufferSize)
	p.outBassL = make([]complex128, p.fftBassBufferSize/2+1)
	p.outL = make([]complex128, p.fftBufferSize/2+1)
	if channels == 2 {
		p.inBassR = make([]float64, p.fftBassBufferSize)
		p.inR = make([]float64, p.fftBufferSize)
		p.outBassR = make([]complex128, p.fftBassBufferSize/2+1)
		p.outR = make([]complex128, p.fftBufferSize/2+1)
	}

	p.planBands(lowCutOff, highCutOff)
	return p, nil
}

// planBands works out which FFT bins each bar sums, and the equaliser that
// normalizes them.
//
// The distribution is logarithmic: bar n covers the frequency the original
// calls upper * 10^c, where c walks linearly from log10(low/high) to 0. The
// float32 arithmetic below is not an oversight — the original computes these
// in C float, and the truncations to int that follow land on different bins if
// it is done in double.
func (p *Plan) planBands(lowCutOff, highCutOff int) {
	upperCutOff := highCutOff

	frequencyConstant := math.Log10(float64(float32(lowCutOff)/float32(upperCutOff))) /
		float64(float32(1)/(float32(p.numberOfBars)+1)-1)

	relativeCutOff := make([]float32, p.numberOfBars+1)

	p.bassCutOffBar = 0
	firstBar := true

	// The narrowest band the long transform can resolve, in Hz — and an
	// integer division in the original, so at 44100 over 8192 points it is 5
	// and not 5.38.
	minBandwidth := float32(p.rate / p.fftBassBufferSize)

	for n := 0; n <= p.numberOfBars; n++ {
		barDistributionCoefficient := frequencyConstant * -1
		barDistributionCoefficient += float64((float32(n)+1)/(float32(p.numberOfBars)+1)) * frequencyConstant
		p.cutOffFrequency[n] = float32(float64(upperCutOff) * math.Pow(10, barDistributionCoefficient))

		if n > 0 && p.cutOffFrequency[n-1] >= p.cutOffFrequency[n] {
			p.cutOffFrequency[n] = p.cutOffFrequency[n-1] + minBandwidth
		}

		// Remember Nyquist: bin index is the fraction of half the sample rate.
		relativeCutOff[n] = p.cutOffFrequency[n] / float32(p.rate/2)

		if p.cutOffFrequency[n] < bassCutOff {
			// Bass: read from the long transform.
			p.lowerCutOff[n] = int(relativeCutOff[n] * float32(p.fftBassBufferSize/2))
			p.bassCutOffBar++
			if p.bassCutOffBar > 1 {
				firstBar = false
			}
			if p.lowerCutOff[n] > p.fftBassBufferSize/2 {
				p.lowerCutOff[n] = p.fftBassBufferSize / 2
			}
		} else {
			// Mid and treble: read from the short transform.
			p.lowerCutOff[n] = int(math.Ceil(float64(relativeCutOff[n] * float32(p.fftBufferSize/2))))
			if n == p.bassCutOffBar {
				// The handover bar. The bar below it is the last of the bass,
				// so its upper edge has to be expressed in long-transform
				// bins, not short ones.
				firstBar = true
				if n > 0 {
					p.upperCutOff[n-1] = int(relativeCutOff[n]*float32(p.fftBassBufferSize/2) - 1)
				}
			} else {
				firstBar = false
			}
			if p.lowerCutOff[n] > p.fftBufferSize/2 {
				p.lowerCutOff[n] = p.fftBufferSize / 2
			}
		}

		if n > 0 {
			if !firstBar {
				p.upperCutOff[n-1] = p.lowerCutOff[n] - 1

				// The logarithm clumps in the bass: several bars can land on
				// the same bin, and a bar with no bins of its own is dead. If
				// there is room, push it up one bin — the spectrum ends up
				// stretched rather than the low bars ending up flat.
				if p.lowerCutOff[n] <= p.lowerCutOff[n-1] {
					roomForMore := false
					if n < p.bassCutOffBar {
						if p.lowerCutOff[n-1]+1 < p.fftBassBufferSize/2+1 {
							roomForMore = true
						}
					} else {
						if p.lowerCutOff[n-1]+1 < p.fftBufferSize/2+1 {
							roomForMore = true
						}
					}
					if roomForMore {
						p.lowerCutOff[n] = p.lowerCutOff[n-1] + 1
						p.upperCutOff[n-1] = p.lowerCutOff[n] - 1
					}
				}
			} else {
				if p.upperCutOff[n-1] < p.lowerCutOff[n-1] {
					p.upperCutOff[n-1] = p.lowerCutOff[n-1] + 1
				}
			}
		}

		// Report the frequency the bar actually got, not the one it asked for.
		if n < p.bassCutOffBar {
			relativeCutOff[n] = float32(p.lowerCutOff[n]) / (float32(p.fftBassBufferSize) / 2)
		} else {
			relativeCutOff[n] = float32(p.lowerCutOff[n]) / (float32(p.fftBufferSize) / 2)
		}
		p.cutOffFrequency[n] = relativeCutOff[n] * (float32(p.rate) / 2)
	}

	// The equaliser. Magnitudes out of an unnormalised FFT are enormous and
	// fall off with frequency; this divides them back into a usable range and
	// tilts the top end up so treble is visible next to bass.
	for n := 0; n < p.numberOfBars; n++ {
		p.eq[n] = 1 / math.Pow(2, 28)
		p.eq[n] *= math.Pow(float64(p.cutOffFrequency[n+1]), 0.85)
		if n < p.bassCutOffBar {
			p.eq[n] /= math.Log2(float64(p.fftBassBufferSize))
		} else {
			p.eq[n] /= math.Log2(float64(p.fftBufferSize))
		}
		p.eq[n] /= float64(p.upperCutOff[n] - p.lowerCutOff[n] + 1)
	}
}

// Bars reports the number of bars per channel.
func (p *Plan) Bars() int { return p.numberOfBars }

// Channels reports the number of interleaved input channels.
func (p *Plan) Channels() int { return p.audioChannels }

// InputBufferSize is the largest number of samples one Execute will consume.
// Anything beyond it is discarded.
func (p *Plan) InputBufferSize() int { return len(p.inputBuffer) }

// CutOffFrequencies returns the lower edge of every bar in Hz, plus one final
// entry for the upper edge of the last bar. The slice is the plan's own; treat
// it as read-only.
func (p *Plan) CutOffFrequencies() []float32 { return p.cutOffFrequency }

// BassCutOffBar reports how many of the low bars are read from the long
// transform.
func (p *Plan) BassCutOffBar() int { return p.bassCutOffBar }

// Sensitivity reports the current adaptive gain. It is only meaningful with
// autosens on.
func (p *Plan) Sensitivity() float64 { return p.sens }

// SetSensitivity overrides the adaptive gain, which is what cava's up and down
// arrow keys do.
func (p *Plan) SetSensitivity(s float64) { p.sens = s }

// Framerate reports the engine's own running estimate of how often it is being
// called, derived from how many samples arrive per execution. The smoothing
// constants are scaled by it so that a bar falls at the same speed on screen
// whatever the frame rate.
func (p *Plan) Framerate() float64 { return p.framerate }

// Execute runs one frame.
//
// in holds newSamples interleaved samples — with two channels that is
// newSamples/2 frames — and may be empty, which is how a caller says a frame
// passed with no audio. out receives Bars()*Channels() values, left channel
// first, ordered from lowest frequency to highest. out must be that long.
//
// Values are nominally 0..1 with autosens on, and unbounded without it.
func (p *Plan) Execute(in []float64, newSamples int, out []float64) {
	if newSamples > len(p.inputBuffer) {
		newSamples = len(p.inputBuffer)
	}
	if newSamples > len(in) {
		newSamples = len(in)
	}

	silence := true
	if newSamples > 0 {
		// Approximate the actual frame rate. This is off by about 10% at 60
		// fps, which is close enough for the smoothing and sensitivity
		// constants that depend on it.
		//
		// The frame count is an integer division in the original, so a call
		// carrying fewer samples than there are channels divides by zero and
		// leaves the estimate infinite for good. Here such a call is simply
		// not counted.
		if frames := newSamples / p.audioChannels; frames > 0 {
			p.framerate -= p.framerate / 64.0
			p.framerate += float64(p.rate*p.frameSkip) / float64(frames) / 64.0
		}
		p.frameSkip = 1

		// Shift the history along to make room. copy is memmove, so the
		// overlap is safe.
		copy(p.inputBuffer[newSamples:], p.inputBuffer[:len(p.inputBuffer)-newSamples])

		// New samples go in reversed: the buffer runs newest to oldest.
		for n := 0; n < newSamples; n++ {
			v := in[n]
			if p.scalingMode == ScalingDecibel {
				// Signals arrive in the range of a 16-bit sample; decibel
				// scaling wants them normalized to -1..1 first.
				v /= 32768.0
			}
			p.inputBuffer[newSamples-n-1] = v
			if in[n] != 0 {
				silence = false
			}
		}
	} else {
		p.frameSkip++
	}

	p.fillWindows()
	p.transform()
	p.sortBands(out)

	if p.autosens != 0 {
		for n := range out[:p.numberOfBars*p.audioChannels] {
			out[n] *= p.sens
		}
	}

	overshoot := p.smooth(out)

	if p.autosens != 0 {
		framerateMod := 66 / p.framerate
		if overshoot {
			p.sens *= 1 - 0.02*framerateMod
			p.sensInit = false
		} else if !silence {
			p.sens *= 1 + 0.001*framerateMod*float64(p.autosens)
			if p.sensInit {
				// Before the first clip there is no evidence about the signal
				// level at all, so climb hard rather than crawl up from
				// silence over a minute.
				p.sens *= 1 + 0.1*framerateMod
			}
		}
	}
}

// fillWindows de-interleaves the history into the two transform inputs and
// applies the Hann window in the same pass.
//
// Note which channel is which. The input arrives interleaved left first, but
// the history buffer was written back to front, which swaps the parity: even
// positions in it are the right channel and odd ones the left.
func (p *Plan) fillWindows() {
	if p.audioChannels == 2 {
		for n := 0; n < p.fftBassBufferSize; n++ {
			p.inBassR[n] = p.bassMultiplier[n] * p.inputBuffer[n*2]
			p.inBassL[n] = p.bassMultiplier[n] * p.inputBuffer[n*2+1]
		}
		for n := 0; n < p.fftBufferSize; n++ {
			p.inR[n] = p.multiplier[n] * p.inputBuffer[n*2]
			p.inL[n] = p.multiplier[n] * p.inputBuffer[n*2+1]
		}
		return
	}
	for n := 0; n < p.fftBassBufferSize; n++ {
		p.inBassL[n] = p.bassMultiplier[n] * p.inputBuffer[n]
	}
	for n := 0; n < p.fftBufferSize; n++ {
		p.inL[n] = p.multiplier[n] * p.inputBuffer[n]
	}
}

// transform runs the two (or four) real transforms and keeps the half of each
// result that is not a mirror image of the other half.
func (p *Plan) transform() {
	copy(p.outBassL, fft.FFTReal(p.inBassL))
	copy(p.outL, fft.FFTReal(p.inL))
	if p.audioChannels == 2 {
		copy(p.outBassR, fft.FFTReal(p.inBassR))
		copy(p.outR, fft.FFTReal(p.inR))
	}
}

// sortBands sums the magnitudes in each bar's bin range and scales the result.
func (p *Plan) sortBands(out []float64) {
	for n := 0; n < p.numberOfBars; n++ {
		var tempL, tempR float64
		bass := n < p.bassCutOffBar
		for i := p.lowerCutOff[n]; i <= p.upperCutOff[n]; i++ {
			if bass {
				tempL += cmplx.Abs(p.outBassL[i])
				if p.audioChannels == 2 {
					tempR += cmplx.Abs(p.outBassR[i])
				}
			} else {
				tempL += cmplx.Abs(p.outL[i])
				if p.audioChannels == 2 {
					tempR += cmplx.Abs(p.outR[i])
				}
			}
		}

		out[n] = p.scale(tempL, n)
		if p.audioChannels == 2 {
			out[n+p.numberOfBars] = p.scale(tempR, n)
		}
	}
}

// scale turns a summed magnitude into a bar height.
func (p *Plan) scale(v float64, bar int) float64 {
	if p.scalingMode == ScalingDecibel {
		// 20 log10 is the amplitude-ratio form of the decibel. Silence gives
		// -Inf, which is not a bar height.
		db := 20 * math.Log10(v) / maxDecibel
		if math.IsInf(db, 0) || math.IsNaN(db) {
			return 0
		}
		return db
	}
	return v * p.eq[bar]
}

// smooth applies the two filters that make the output watchable, and reports
// whether any bar hit the ceiling.
//
// Both are scaled by framerateMod so that the fall takes the same wall-clock
// time whatever the frame rate: at 66 fps it is 1, and at 33 fps a bar has to
// fall twice as far per frame to look the same.
func (p *Plan) smooth(out []float64) bool {
	overshoot := false

	framerateMod := 66 / p.framerate
	gravityMod := math.Pow(framerateMod, 2.5) * 2 / p.noiseReduction
	integralMod := math.Pow(framerateMod, 0.1)

	for n := 0; n < p.numberOfBars*p.audioChannels; n++ {
		// Fall-off. A bar never simply drops to its new value; it falls from
		// wherever it last peaked, accelerating, which is what makes the tops
		// look like they have weight. cava_fall is the time since the peak,
		// and the displacement goes as its square.
		//
		// Below a noise reduction of 0.1 the filter is off entirely — gravity
		// is divided by it, and at zero there would be nothing to divide by.
		if out[n] < p.prevOut[n] && p.noiseReduction > 0.1 {
			out[n] = p.peak[n] * (1.0 - p.fall[n]*p.fall[n]*gravityMod)
			if out[n] < 0.0 {
				out[n] = 0.0
			}
			p.fall[n] += 0.028
		} else {
			p.peak[n] = out[n]
			p.fall[n] = 0.0
		}
		p.prevOut[n] = out[n]

		// Integral. A leaky accumulator: the bar keeps a fraction of what it
		// was and adds what it is now. This is the filter noise_reduction
		// mostly names — it is why a bar rises smoothly instead of flickering
		// with every frame's worth of FFT noise.
		out[n] = p.mem[n]*p.noiseReduction/integralMod + out[n]
		p.mem[n] = out[n]

		if p.autosens != 0 && out[n] > 1.0 {
			overshoot = true
			out[n] = 1.0
		}
	}
	return overshoot
}

// SetSingleThreaded stops the FFT from using goroutines. Worth calling under
// js/wasm, where the worker pool costs more than it saves.
func SetSingleThreaded() { fft.SetWorkerPoolSize(-1) }
