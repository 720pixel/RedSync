package hdr

import "math"

// Geometry works out how the DV source's picture sits inside the HDR base frame.
//
// The two layers are the same image at (often) different framings. The DV source
// might be cropped tight (iTunes hands you 3840x1608 with no bars) while the HDR
// base keeps hard black bars (MA/Vudu give 3840x2160 with the scope image
// letterboxed inside). To put DV metadata onto the HDR base correctly, the RPU's
// active area has to describe those bars, so a DV display crops to the real
// picture instead of tone-mapping the black.
//
// We scale the DV picture to fit the HDR frame preserving aspect, then the bars
// are whatever's left over, split evenly. When both layers already match (same
// size, no bars) every offset comes out zero and we leave the RPU alone.
type Geometry struct {
	Scale  float64 // DV pixels -> HDR pixels; multiply DV-frame L5 offsets by this
	Left   int
	Right  int
	Top    int
	Bottom int
}

// ComputeGeometry returns the active-area offsets (in HDR-frame pixels) for a DV
// picture of dvW x dvH placed inside an HDR frame of hdrW x hdrH.
func ComputeGeometry(hdrW, hdrH, dvW, dvH int) Geometry {
	if dvW <= 0 || dvH <= 0 || hdrW <= 0 || hdrH <= 0 {
		return Geometry{Scale: 1}
	}
	// fit-inside scale: the dimension that's already equal pins to 1.0, the other
	// leaves the bars. min() keeps the whole picture visible (contain, not cover).
	scale := math.Min(float64(hdrW)/float64(dvW), float64(hdrH)/float64(dvH))

	placedW := int(math.Round(float64(dvW) * scale))
	placedH := int(math.Round(float64(dvH) * scale))

	left := (hdrW - placedW) / 2
	top := (hdrH - placedH) / 2

	return Geometry{
		Scale:  scale,
		Left:   max0(left),
		Right:  max0(hdrW - placedW - left), // remainder, so odd gaps still sum right
		Top:    max0(top),
		Bottom: max0(hdrH - placedH - top),
	}
}

// Base is the geometric crop as a preset, before any per-frame RPU L5 is added.
func (g Geometry) Base() areaPreset {
	return areaPreset{L: g.Left, R: g.Right, T: g.Top, B: g.Bottom}
}

func (g Geometry) zero() bool {
	return g.Left == 0 && g.Right == 0 && g.Top == 0 && g.Bottom == 0
}

// identity is true when the two layers share the exact same geometry: no bars
// and a 1:1 scale. In that case the DV RPU's own active area already fits the
// base and needs no editing. A non-unit scale (e.g. 1080p DV onto a 2160p base)
// is deliberately not identity - the per-frame L5 still has to be scaled up.
func (g Geometry) identity() bool {
	return g.zero() && g.Scale == 1
}

func max0(v int) int {
	if v < 0 {
		return 0
	}
	return v
}
