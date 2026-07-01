package sync

import (
	"context"
	"fmt"
	"math"

	"github.com/720pixel/RedSync/internal/media"
	"github.com/720pixel/RedSync/internal/offset"
)

// Drift is what we learn from comparing an external source against the video.
type Drift struct {
	DelayMS   int     // constant delay for mkvmerge --sync
	Linear    string  // o/p factor for mkvmerge, "" when no fps stretch needed
	FPSStretch bool   // true if we corrected a frame-rate mismatch
	Score     float64 // confidence of the audio match
	Probe1, Probe2 int // measured delay at the two probe points (ms), for the log
}

// probe windows. we sample two spots so we can tell a constant delay apart from
// a slow drift. lengths are short on purpose - speech aligns fine in ~25s and it
// keeps the decode cheap. positions adapt to the file length so short clips
// still work.
const (
	probeLen   = 25.0
	probeEarly = 60.0 // seconds in, for a full-length episode/movie
	probeLate  = 0.70 // fraction of duration for the second probe
)

// probePositions picks where to sample given the runtime, keeping each window
// inside the file. Returns the early position and the late one (-1 if the file
// is too short for a second probe).
func probePositions(dur float64) (float64, float64) {
	if dur <= 0 {
		return 0, -1 // unknown length: one probe from the top
	}
	win := math.Min(probeLen, math.Max(5, dur*0.4))
	early := probeEarly
	if early+win > dur {
		early = math.Max(0, dur*0.1) // short file: start near the beginning
	}
	late := dur * probeLate
	if late+win > dur || late <= early+win {
		late = -1
	}
	return early, late
}

// Measure figures out how to line ext up with ref. It decodes the primary audio
// of each at two points (in parallel), finds the offset at each, and:
//   - if both files have video at different fps, trusts the exact fps ratio for
//     the linear stretch (the reliable fix for PAL/NTSC style mismatches)
//   - otherwise infers drift from how much the two measured delays disagree
func Measure(ctx context.Context, ref, ext media.File, refDur float64) (Drift, error) {
	if len(ref.Audio) == 0 || len(ext.Audio) == 0 {
		return Drift{}, fmt.Errorf("need an audio track in both files to sync")
	}
	refIdx := ref.Audio[0].Index
	extIdx := ext.Audio[0].Index

	early, late := probePositions(refDur)
	win := math.Min(probeLen, math.Max(5, refDur*0.4))
	if refDur <= 0 {
		win = probeLen
	}

	d1, s1, err := offsetAt(ctx, ref.Path, refIdx, ext.Path, extIdx, early, win)
	if err != nil {
		return Drift{}, err
	}
	dr := Drift{DelayMS: d1, Score: s1, Probe1: d1, Probe2: d1}

	if late > 0 {
		d2, s2, err := offsetAt(ctx, ref.Path, refIdx, ext.Path, extIdx, late, win)
		if err == nil && s2 > 4 {
			dr.Probe2 = d2
			// disagreement between the two probes => the clocks are drifting
			if math.Abs(float64(d2-d1)) > 40 {
				dr.FPSStretch = true
				dr.Linear = fpsLinear(ref, ext, d1, d2, early, late, &dr)
			}
		}
	}

	// if we know both frame rates and they differ, that exact ratio beats any
	// inference - use it and don't guess.
	if rf, ef := ref.FPS(), ext.FPS(); rf > 0 && ef > 0 && math.Abs(rf-ef) > 0.01 {
		dr.FPSStretch = true
		dr.Linear = exactFPSRatio(ref.Video[0], ext.Video[0])
	}
	return dr, nil
}

// offsetAt decodes one window from each file at position p and returns the delay
// in ms plus the match score.
func offsetAt(ctx context.Context, refPath string, refIdx int, extPath string, extIdx int, at, win float64) (int, float64, error) {
	var a, b []float64
	var ea, eb error
	done := make(chan struct{}, 2)
	go func() { a, ea = offset.Decode(ctx, refPath, refIdx, at, win); done <- struct{}{} }()
	go func() { b, eb = offset.Decode(ctx, extPath, extIdx, at, win); done <- struct{}{} }()
	<-done
	<-done
	if ea != nil {
		return 0, 0, ea
	}
	if eb != nil {
		return 0, 0, eb
	}
	r, err := offset.Find(a, b)
	if err != nil {
		return 0, 0, err
	}
	return r.Millis(), r.Score, nil
}

// exactFPSRatio builds the mkvmerge o/p factor from the two video fps as a clean
// rational: (refNum*extDen)/(refDen*extNum). Multiplying ext timestamps by this
// slews it onto the video's clock.
func exactFPSRatio(refV, extV media.Track) string {
	o := refV.FPSNum * extV.FPSDen
	p := refV.FPSDen * extV.FPSNum
	if g := gcd(o, p); g > 1 {
		o /= g
		p /= g
	}
	return fmt.Sprintf("%d/%d", o, p)
}

// fpsLinear is the fallback when we can't read both frame rates: derive the
// slope straight from the two probe measurements.
func fpsLinear(ref, ext media.File, d1, d2 int, t1, t2 float64, dr *Drift) string {
	if rf, ef := ref.FPS(), ext.FPS(); rf > 0 && ef > 0 {
		return exactFPSRatio(ref.Video[0], ext.Video[0])
	}
	slope := float64(d2-d1) / ((t2 - t1) * 1000) // ms per ms
	op := 1.0 - slope
	// keep the constant term anchored at t=0
	dr.DelayMS = d1 - int(math.Round(slope*t1*1000))
	return fmt.Sprintf("%.9f", op)
}

func gcd(a, b int) int {
	for b != 0 {
		a, b = b, a%b
	}
	if a < 0 {
		return -a
	}
	return a
}
