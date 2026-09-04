package cava

import "math"

// MonstercatFilter spreads each bar's height into its neighbours, in place.
//
// This is the filter cava calls "monstercat smoothing" after the YouTube
// channel whose visualiser it imitates. It does not smooth in time — that is
// the integral and gravity filters inside [Plan.Execute] — it smooths across
// the spectrum, so that a bar with energy in it lifts the bars either side and
// the row of bars reads as a curve rather than as a comb.
//
// Two shapes are available and waves wins if both are set, matching the
// original:
//
//   - waves, an inverted parabola. Each bar is first cut to 80% of itself, and
//     then every other bar is raised to at least this bar's height less the
//     square of the distance between them. The falloff is subtractive, so it
//     is the same number of bars wide wherever it is on the screen — wide and
//     rounded, hence the name.
//   - monstercat, an exponential. Every other bar is raised to at least this
//     bar's height divided by (monstercat * 1.5) to the power of the distance.
//     The falloff is multiplicative, so a tall bar spreads further than a short
//     one and the skirt is narrower. cava's own default when enabled is 1.5.
//
// height is the height of the drawing surface in the same units as the bars.
// It only matters above 1000 — a pixel surface rather than a terminal — where
// the parabola is widened so that waves look the same on a graphical output as
// they do in a terminal.
//
// bars is returned for convenience; it is the same slice, modified.
func MonstercatFilter(bars []float64, waves int, monstercat float64, height int) []float64 {
	heightNormalizer := 1.0
	if height > 1000 {
		heightNormalizer = float64(height) / 912.76
	}

	switch {
	case waves > 0:
		for z := range bars {
			// Cut before spreading. Without it every bar would be lifted by
			// its neighbours and never lowered, and a busy spectrum would
			// saturate into a solid block.
			bars[z] /= 1.25
			// The two loops walk outwards from z. Note that the bars below z
			// have already been cut and the bars above have not, so the filter
			// is not symmetric — that asymmetry is in the original and is part
			// of how it looks.
			for my := z - 1; my >= 0; my-- {
				de := float64(z - my)
				bars[my] = math.Max(bars[z]-heightNormalizer*de*de, bars[my])
			}
			for my := z + 1; my < len(bars); my++ {
				de := float64(my - z)
				bars[my] = math.Max(bars[z]-heightNormalizer*de*de, bars[my])
			}
		}
	case monstercat > 0:
		for z := range bars {
			for my := z - 1; my >= 0; my-- {
				de := float64(z - my)
				bars[my] = math.Max(bars[z]/math.Pow(monstercat*1.5, de), bars[my])
			}
			for my := z + 1; my < len(bars); my++ {
				de := float64(my - z)
				bars[my] = math.Max(bars[z]/math.Pow(monstercat*1.5, de), bars[my])
			}
		}
	}
	return bars
}
