// Package offset finds the time delay between two audio tracks.
//
// Approach is the same idea bbc/audio-offset-finder uses (compare spectral
// feature envelopes, take the peak of the cross-correlation) but reworked for
// speed: we decode straight from ffmpeg into memory, build a light log-mel
// envelope, and run the correlation through an FFT instead of the O(n*m)
// per-lag dot product the python version does. Same result, a lot less waiting.
package offset

import (
	"fmt"
	"math"

	"gonum.org/v1/gonum/dsp/fourier"
)

const (
	sampleRate = 8000 // everything gets resampled here, plenty for speech alignment
	winLen     = 256
	hopLen     = 128
	nFFT       = 512
	nBands     = 24 // log-spaced energy bands, our stand-in for mfcc coeffs
)

// Result is what a single comparison gives back.
type Result struct {
	Offset float64 // seconds. positive => second file starts later than the first
	Score  float64 // z-score of the peak. higher is more trustworthy, ~8+ is solid
}

// Millis returns the offset rounded to whole milliseconds, which is what
// mkvmerge --sync actually wants.
func (r Result) Millis() int {
	return int(math.Round(r.Offset * 1000))
}

// Find takes two decoded mono signals (already at sampleRate) and returns the
// delay of b relative to a.
func Find(a, b []float64) (Result, error) {
	fa := envelope(a)
	fb := envelope(b)
	if len(fa) == 0 || len(fb) == 0 {
		return Result{}, fmt.Errorf("not enough audio to compare")
	}
	c := correlate(fa, fb)
	peak, idx := 0.0, 0
	var sum, sumSq float64
	for i, v := range c {
		sum += v
		sumSq += v * v
		if v > peak {
			peak, idx = v, i
		}
	}
	n := float64(len(c))
	mean := sum / n
	std := math.Sqrt(sumSq/n - mean*mean)
	score := 0.0
	if std > 0 {
		score = (peak - mean) / std
	}

	// idx maps back to a frame lag centered on len(fb)-1, then frames -> seconds.
	lag := idx - (len(fb) - 1)
	secs := float64(lag) * float64(hopLen) / float64(sampleRate)
	return Result{Offset: secs, Score: score}, nil
}

// envelope turns a raw signal into a [frames][nBands] log-energy matrix,
// z-normalised per band so loudness differences between encodes don't matter.
func envelope(sig []float64) [][]float64 {
	if len(sig) < winLen {
		return nil
	}
	fft := fourier.NewFFT(nFFT)
	win := hann(winLen)
	mel := melBands(nBands, nFFT, sampleRate)

	frames := 1 + (len(sig)-winLen)/hopLen
	out := make([][]float64, frames)
	buf := make([]float64, nFFT)
	for f := 0; f < frames; f++ {
		start := f * hopLen
		for i := 0; i < winLen; i++ {
			buf[i] = sig[start+i] * win[i]
		}
		for i := winLen; i < nFFT; i++ {
			buf[i] = 0
		}
		spec := fft.Coefficients(nil, buf)
		power := make([]float64, len(spec))
		for i, c := range spec {
			power[i] = real(c)*real(c) + imag(c)*imag(c)
		}
		row := make([]float64, nBands)
		for b := 0; b < nBands; b++ {
			var e float64
			for _, fb := range mel[b] {
				e += power[fb.bin] * fb.w
			}
			row[b] = math.Log(e + 1e-9)
		}
		out[f] = row
	}
	zNormalise(out)
	return out
}

// correlate slides fb over fa and returns the summed-band correlation for every
// lag. each band is correlated with an FFT (linear conv via zero-padding), which
// is where the speedup over the reference tool comes from.
func correlate(fa, fb [][]float64) []float64 {
	na, nb := len(fa), len(fb)
	full := na + nb - 1
	size := nextPow2(full)
	fft := fourier.NewFFT(size)

	acc := make([]float64, full)
	ca := make([]float64, size)
	cb := make([]float64, size)
	for band := 0; band < nBands; band++ {
		for i := range ca {
			ca[i], cb[i] = 0, 0
		}
		for i := 0; i < na; i++ {
			ca[i] = fa[i][band]
		}
		// reverse fb so the linear convolution becomes a cross-correlation
		for i := 0; i < nb; i++ {
			cb[nb-1-i] = fb[i][band]
		}
		fa2 := fft.Coefficients(nil, ca)
		fb2 := fft.Coefficients(nil, cb)
		for i := range fa2 {
			fa2[i] *= fb2[i]
		}
		conv := fft.Sequence(nil, fa2)
		norm := float64(size)
		for i := 0; i < full; i++ {
			acc[i] += conv[i] / norm
		}
	}
	return acc
}

// --- small dsp helpers, nothing fancy ---

func zNormalise(m [][]float64) {
	if len(m) == 0 {
		return
	}
	bands := len(m[0])
	for b := 0; b < bands; b++ {
		var sum, sumSq float64
		for _, row := range m {
			sum += row[b]
			sumSq += row[b] * row[b]
		}
		n := float64(len(m))
		mean := sum / n
		std := math.Sqrt(sumSq/n - mean*mean)
		if std == 0 {
			std = 1
		}
		for _, row := range m {
			row[b] = (row[b] - mean) / std
		}
	}
}

func hann(n int) []float64 {
	w := make([]float64, n)
	for i := range w {
		w[i] = 0.5 - 0.5*math.Cos(2*math.Pi*float64(i)/float64(n-1))
	}
	return w
}

type melBin struct {
	bin int
	w   float64
}

// melBands builds triangular mel filters and returns, per band, which fft bins
// feed it and with what weight.
func melBands(bands, fftSize, sr int) [][]melBin {
	bins := fftSize/2 + 1
	hzToMel := func(hz float64) float64 { return 2595 * math.Log10(1+hz/700) }
	melToHz := func(m float64) float64 { return 700 * (math.Pow(10, m/2595) - 1) }

	lo := hzToMel(0)
	hi := hzToMel(float64(sr) / 2)
	points := make([]float64, bands+2)
	for i := range points {
		mel := lo + (hi-lo)*float64(i)/float64(bands+1)
		points[i] = melToHz(mel) * float64(fftSize) / float64(sr) // -> fft bin
	}

	out := make([][]melBin, bands)
	for b := 0; b < bands; b++ {
		left, center, right := points[b], points[b+1], points[b+2]
		for k := 0; k < bins; k++ {
			fk := float64(k)
			var w float64
			switch {
			case fk >= left && fk <= center && center > left:
				w = (fk - left) / (center - left)
			case fk > center && fk <= right && right > center:
				w = (right - fk) / (right - center)
			}
			if w > 0 {
				out[b] = append(out[b], melBin{bin: k, w: w})
			}
		}
	}
	return out
}

func nextPow2(n int) int {
	p := 1
	for p < n {
		p <<= 1
	}
	return p
}
