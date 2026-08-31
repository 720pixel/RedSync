package sync

import (
	"context"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/720pixel/RedSync/internal/media"
	"github.com/720pixel/RedSync/internal/offset"
	"github.com/720pixel/RedSync/internal/timeline"
	"github.com/720pixel/RedSync/internal/tools"
)

type audioAnchor struct {
	x, delay, score float64
}

// MeasureOptions controls standalone audio analysis. Zero values select the
// normal defaults, which are deliberately conservative for episode/movie audio.
type MeasureOptions struct {
	MaxOffsetSeconds float64
	MinScore         float64
	MinGapSeconds    float64
	MaxSegments      int
	DisablePiecewise bool
}

// Factor returns the target-timestamp multiplier represented by a Drift.
// A value below 1 speeds a long target up; a value above 1 slows a short target
// down. Older Drift values only populated Linear, so both forms are accepted.
func (d Drift) Factor() float64 {
	if d.Scale > 0 {
		return d.Scale
	}
	if d.Linear == "" {
		return 1
	}
	if n, den, ok := strings.Cut(d.Linear, "/"); ok {
		a, ea := strconv.ParseFloat(n, 64)
		b, eb := strconv.ParseFloat(den, 64)
		if ea == nil && eb == nil && b != 0 {
			return a / b
		}
	}
	if f, err := strconv.ParseFloat(d.Linear, 64); err == nil && f > 0 {
		return f
	}
	return 1
}

// MeasureAudio aligns one audio stream to another without requiring either file
// to contain video. It first obtains a broad start anchor, tries ordinary and
// common film/TV speed ratios, then measures dense windows across the runtime.
// A robust piecewise fit separates clock/FPS drift from internal edits.
func MeasureAudio(ctx context.Context, ref, target media.File, refTrack, targetTrack int, opts MeasureOptions) (Drift, error) {
	if refTrack < 0 {
		if len(ref.Audio) == 0 {
			return Drift{}, fmt.Errorf("reference has no audio track")
		}
		refTrack = ref.Audio[0].Index
	}
	if targetTrack < 0 {
		if len(target.Audio) == 0 {
			return Drift{}, fmt.Errorf("target has no audio track")
		}
		targetTrack = target.Audio[0].Index
	}
	if ref.Duration <= 0 || target.Duration <= 0 {
		return Drift{}, fmt.Errorf("audio duration is unavailable")
	}
	sharedDuration := math.Min(ref.Duration, target.Duration)
	if sharedDuration < 5 {
		return Drift{}, fmt.Errorf("need at least 5 seconds of audio to match")
	}
	maxOffset := opts.MaxOffsetSeconds
	if maxOffset <= 0 {
		maxOffset = 120
	}
	minScore := opts.MinScore
	if minScore <= 0 {
		minScore = 4
	}

	// A long window from the head copes with bumpers/padding while still giving
	// the spectral matcher enough shared programme audio in dubbed releases.
	initialWin := math.Min(maxOffset+35, sharedDuration*0.22)
	initialWin = clamp(initialWin, 35, 155)
	initialWin = math.Min(initialWin, sharedDuration)
	initial, initialScore, err := offsetAtPositions(ctx, ref.Path, refTrack, 0, target.Path, targetTrack, 0, initialWin)
	if err != nil {
		return Drift{}, fmt.Errorf("initial audio match: %w", err)
	}
	if initialScore < minScore {
		return Drift{}, fmt.Errorf("audio match confidence is too low (%.2f < %.2f); files may not contain the same programme", initialScore, minScore)
	}
	initialSec := float64(initial) / 1000
	initialCenter := initialWin / 2
	if math.Abs(initialSec) > maxOffset {
		return Drift{}, fmt.Errorf("measured offset %.3fs exceeds the %.0fs safety limit (raise --max-offset if expected)", initialSec, maxOffset)
	}

	candidates := speedCandidates(ref.Duration, target.Duration, initialSec, initialCenter)
	candidateWin := math.Min(32, math.Max(5, sharedDuration*0.18))
	bestScale := 1.0
	bestQuality := math.Inf(-1)
	for _, scale := range candidates {
		interceptGuess := initialSec - (scale-1)*initialCenter
		var scores, corrections []float64
		for _, frac := range []float64{0.22, 0.50, 0.78} {
			x := target.Duration * frac
			refAt := scale*x + interceptGuess
			if refAt < 0 || refAt+candidateWin >= ref.Duration || x+candidateWin >= target.Duration {
				continue
			}
			d, score, e := offsetAtPositionsScaled(ctx, ref.Path, refTrack, refAt, target.Path, targetTrack, x, candidateWin, scale)
			if e != nil || score < minScore {
				continue
			}
			expected := (scale-1)*x + interceptGuess
			scores = append(scores, score)
			corrections = append(corrections, math.Abs(float64(d)/1000-expected))
		}
		if len(scores) < 2 {
			continue
		}
		// Correct candidates need little local correction. Confidence remains the
		// primary signal, with a gentle penalty for being many seconds away.
		quality := median(scores) - 0.35*median(corrections)
		if quality > bestQuality {
			bestQuality, bestScale = quality, scale
		}
	}

	bestInterceptGuess := initialSec - (bestScale-1)*initialCenter
	var anchors []audioAnchor
	probeWin := math.Min(38, math.Max(6, sharedDuration*0.12))
	probeCount := int(math.Ceil(sharedDuration/75)) + 1
	probeCount = int(clamp(float64(probeCount), 9, 25))
	for i := 0; i < probeCount; i++ {
		frac := 0.04 + 0.92*float64(i)/float64(probeCount-1)
		x := target.Duration * frac
		refAt := bestScale*x + bestInterceptGuess
		win := probeWin
		if refAt < 0 || x < 0 || refAt+win >= ref.Duration || x+win >= target.Duration {
			continue
		}
		d, score, e := offsetAtPositionsScaled(ctx, ref.Path, refTrack, refAt, target.Path, targetTrack, x, win, bestScale)
		if e == nil && score >= minScore {
			anchors = append(anchors, audioAnchor{x: x, delay: float64(d) / 1000, score: score})
		}
	}
	if len(anchors) < 4 {
		return Drift{}, fmt.Errorf("only %d reliable audio anchors found; need at least 4", len(anchors))
	}

	timelineAnchors := make([]timeline.Anchor, len(anchors))
	for i, a := range anchors {
		timelineAnchors[i] = timeline.Anchor{TargetSeconds: a.x, DelaySeconds: a.delay, Score: a.score}
	}
	maxSegments := opts.MaxSegments
	if opts.DisablePiecewise {
		maxSegments = 1
	}
	fit := timeline.Piecewise(timelineAnchors, target.Duration, timeline.Options{
		MinJumpSeconds: opts.MinGapSeconds,
		MaxSegments:    maxSegments,
	})
	scale := fit.Scale
	if scale < 0.8 || scale > 1.2 {
		return Drift{}, fmt.Errorf("implausible audio speed factor %.6f", scale)
	}
	// Audio matching can refine a statistical midpoint into the first target
	// sample that confidently belongs to the segment after the edit.
	for i := range fit.Gaps {
		refineAudioBoundary(ctx, ref, target, refTrack, targetTrack, &fit, i, math.Max(minScore, 7))
	}
	d := Drift{
		DelayMS:    fit.OffsetMS,
		Scale:      scale,
		Score:      fit.Score,
		Samples:    fit.Samples,
		ResidualMS: fit.ResidualMS,
		Probe1:     int(math.Round(anchors[0].delay * 1000)),
		Probe2:     int(math.Round(anchors[len(anchors)-1].delay * 1000)),
		Segments:   fit.Segments,
		Gaps:       fit.Gaps,
	}
	if math.Abs(scale-1) >= 0.00002 {
		d.FPSStretch = true
		d.Linear = formatScale(scale)
	}
	return d, nil
}

func refineAudioBoundary(ctx context.Context, ref, target media.File, refTrack, targetTrack int, fit *timeline.Fit, gapIndex int, minScore float64) {
	if gapIndex < 0 || gapIndex >= len(fit.Gaps) || gapIndex+1 >= len(fit.Segments) {
		return
	}
	before, after := fit.Segments[gapIndex], fit.Segments[gapIndex+1]
	at := float64(fit.Gaps[gapIndex].TargetAtMS) / 1000
	// The statistical split lies between the nearest dense anchors and can be
	// half an anchor interval away from the real edit. Search a wide bracket;
	// the short side-specific probes below keep this accurate without decoding
	// that whole range.
	span := math.Min(120, math.Max(45, float64(fit.Gaps[gapIndex].DurationMS)/1000*4))
	lo := math.Max(float64(before.TargetStartMS)/1000, at-span)
	hi := math.Min(float64(after.TargetEndMS)/1000, at+span)
	win := clamp(float64(fit.Gaps[gapIndex].DurationMS)/8000, 0.75, 2)
	if hi-lo < win*2 {
		return
	}
	for n := 0; n < 10 && hi-lo > 0.04; n++ {
		mid := (lo + hi) / 2
		which, ok := classifyAudioMapping(ctx, ref, target, refTrack, targetTrack, mid, win, before, after, minScore)
		if fit.Gaps[gapIndex].DeltaMS < 0 {
			// Locate the first sample that no longer follows the old map. The
			// known jump duration then determines where programme audio resumes.
			if ok && which == 0 {
				lo = mid
			} else {
				hi = mid
			}
		} else if !ok {
			break
		} else if which == 0 {
			lo = mid
		} else {
			hi = mid
		}
	}
	boundary := hi - win/2
	gapSeconds := float64(fit.Gaps[gapIndex].DurationMS) / 1000
	if fit.Gaps[gapIndex].DeltaMS < 0 {
		if silenceAt, ok := findSilenceStart(ctx, target.Path, targetTrack, boundary, math.Max(8, gapSeconds*2), gapSeconds*.60); ok {
			boundary = silenceAt
		}
	} else {
		before := fit.Segments[gapIndex]
		refNear := before.Scale*boundary + float64(before.OffsetMS)/1000
		if silenceAt, ok := findSilenceStart(ctx, ref.Path, refTrack, refNear, math.Max(8, gapSeconds*2), gapSeconds*.60); ok {
			boundary = (silenceAt - float64(before.OffsetMS)/1000) / before.Scale
		}
	}
	timeline.SetBoundary(fit, gapIndex, math.Max(0, boundary))
}

func findSilenceStart(ctx context.Context, path string, track int, around, radius, minDuration float64) (float64, bool) {
	start := math.Max(0, around-radius)
	duration := radius * 2
	samples, err := offsetDecode(ctx, path, track, start, duration)
	if err != nil || len(samples) == 0 {
		return 0, false
	}
	samplesPerSecond := float64(len(samples)) / duration
	block := int(math.Round(samplesPerSecond * .05))
	if block < 1 {
		return 0, false
	}
	minBlocks := int(math.Ceil(minDuration / .05))
	bestDistance := math.Inf(1)
	bestStart := 0.0
	for i := 0; i+block <= len(samples); {
		if audioWindowActive(samples[i : i+block]) {
			i += block
			continue
		}
		runStart := i
		for i+block <= len(samples) && !audioWindowActive(samples[i:i+block]) {
			i += block
		}
		blocks := (i - runStart) / block
		if blocks < minBlocks {
			continue
		}
		candidate := start + float64(runStart)/samplesPerSecond
		distance := math.Abs(candidate - around)
		if distance < bestDistance {
			bestDistance, bestStart = distance, candidate
		}
	}
	return bestStart, !math.IsInf(bestDistance, 1)
}

func classifyAudioMapping(ctx context.Context, ref, target media.File, refTrack, targetTrack int, targetAt, win float64, before, after timeline.Segment, minScore float64) (int, bool) {
	type candidate struct {
		which int
		score float64
	}
	best := candidate{score: math.Inf(-1)}
	for which, seg := range []timeline.Segment{before, after} {
		probeAt := targetAt
		if which == 0 {
			// The old mapping must explain the audio immediately before a cut;
			// the new mapping must explain the audio immediately after it. Using
			// opposite sides avoids smearing the boundary by one probe window.
			probeAt -= win
		}
		refAt := seg.Scale*probeAt + float64(seg.OffsetMS)/1000
		if probeAt < 0 || refAt < 0 || refAt+win >= ref.Duration || probeAt+win >= target.Duration {
			continue
		}
		score, err := mappingWindowScore(ctx, ref.Path, refTrack, refAt, target.Path, targetTrack, probeAt, win, seg.Scale)
		if err == nil && score > best.score {
			best = candidate{which: which, score: score}
		}
	}
	return best.which, best.score >= minScore
}

func mappingWindowScore(ctx context.Context, refPath string, refTrack int, refAt float64, targetPath string, targetTrack int, targetAt, win, scale float64) (float64, error) {
	var a, b []float64
	var ea, eb error
	done := make(chan struct{}, 2)
	go func() { a, ea = offsetDecode(ctx, refPath, refTrack, refAt, win*scale); done <- struct{}{} }()
	go func() { b, eb = offsetDecode(ctx, targetPath, targetTrack, targetAt, win); done <- struct{}{} }()
	<-done
	<-done
	if ea != nil {
		return 0, ea
	}
	if eb != nil {
		return 0, eb
	}
	// Digital/near-digital silence produces meaningless normalized correlation
	// peaks. Treat it as an unmatched black gap during boundary refinement.
	if !audioWindowActive(a) || !audioWindowActive(b) {
		return 0, nil
	}
	r, err := offsetFindScaled(a, b, scale)
	if err != nil {
		return 0, err
	}
	return r.Score, nil
}

func audioWindowActive(samples []float64) bool {
	if len(samples) == 0 {
		return false
	}
	var energy float64
	for _, sample := range samples {
		energy += sample * sample
	}
	return math.Sqrt(energy/float64(len(samples))) >= 1e-6
}

// offsetAtPositions compares windows starting at independent timestamps and
// returns the absolute timestamp adjustment for target.
func offsetAtPositions(ctx context.Context, refPath string, refIdx int, refAt float64, targetPath string, targetIdx int, targetAt, win float64) (int, float64, error) {
	return offsetAtPositionsScaled(ctx, refPath, refIdx, refAt, targetPath, targetIdx, targetAt, win, 1)
}

func offsetAtPositionsScaled(ctx context.Context, refPath string, refIdx int, refAt float64, targetPath string, targetIdx int, targetAt, win, scale float64) (int, float64, error) {
	var a, b []float64
	var ea, eb error
	done := make(chan struct{}, 2)
	go func() { a, ea = offsetDecode(ctx, refPath, refIdx, refAt, win*scale); done <- struct{}{} }()
	go func() { b, eb = offsetDecode(ctx, targetPath, targetIdx, targetAt, win); done <- struct{}{} }()
	<-done
	<-done
	if ea != nil {
		return 0, 0, ea
	}
	if eb != nil {
		return 0, 0, eb
	}
	r, err := offsetFindScaled(a, b, scale)
	if err != nil {
		return 0, 0, err
	}
	abs := (refAt - targetAt) + r.Offset
	return int(math.Round(abs * 1000)), r.Score, nil
}

// Kept as variables so the numerical fit can be unit-tested without invoking
// FFmpeg and so callers still share the offset package implementation.
var (
	offsetDecode     = offset.Decode
	offsetFindScaled = offset.FindScaled
)

func speedCandidates(refDur, targetDur, initial, initialCenter float64) []float64 {
	durationScale := (refDur - initialCenter - initial) / (targetDur - initialCenter)
	vals := []float64{
		1,
		durationScale,
		25 / (24000.0 / 1001), (24000.0 / 1001) / 25,
		25.0 / 24, 24.0 / 25,
		24 / (24000.0 / 1001), (24000.0 / 1001) / 24,
		30 / (30000.0 / 1001), (30000.0 / 1001) / 30,
	}
	var out []float64
	for _, v := range vals {
		if v < 0.8 || v > 1.2 {
			continue
		}
		seen := false
		for _, old := range out {
			if math.Abs(old-v) < 0.000005 {
				seen = true
				break
			}
		}
		if !seen {
			out = append(out, v)
		}
	}
	return out
}

func robustDelayLine(a []audioAnchor, duration float64) (slope, intercept, residual float64) {
	var slopes []float64
	for i := range a {
		for j := i + 1; j < len(a); j++ {
			dx := a[j].x - a[i].x
			if math.Abs(dx) >= duration*0.15 {
				slopes = append(slopes, (a[j].delay-a[i].delay)/dx)
			}
		}
	}
	slope = median(slopes)
	ints := make([]float64, len(a))
	for i, p := range a {
		ints[i] = p.delay - slope*p.x
	}
	intercept = median(ints)
	res := make([]float64, len(a))
	for i, p := range a {
		res[i] = math.Abs(p.delay - (intercept + slope*p.x))
	}
	return slope, intercept, median(res)
}

func median(v []float64) float64 {
	if len(v) == 0 {
		return 0
	}
	c := append([]float64(nil), v...)
	sort.Float64s(c)
	m := len(c) / 2
	if len(c)%2 == 0 {
		return (c[m-1] + c[m]) / 2
	}
	return c[m]
}

func clamp(v, lo, hi float64) float64 {
	return math.Max(lo, math.Min(hi, v))
}

func formatScale(v float64) string { return strconv.FormatFloat(v, 'f', 9, 64) }

// AudioRenderOptions controls the standalone synced-audio output.
type AudioRenderOptions struct {
	Codec      string
	BitRate    string
	Channels   int
	SampleRate int
	Language   string
	Overwrite  bool
}

// RenderAudio applies the measured affine timestamp map to the target samples.
// atempo corrects speed without altering pitch, then atrim/adelay places the
// result on the reference timeline. apad+atrim makes its duration deterministic.
func RenderAudio(ctx context.Context, target media.File, track media.Track, drift Drift, refDuration float64, output string, opts AudioRenderOptions) error {
	if !opts.Overwrite {
		if _, err := os.Stat(output); err == nil {
			return fmt.Errorf("output already exists: %s (use --overwrite to replace it)", output)
		}
	}
	if err := os.MkdirAll(filepath.Dir(output), 0o755); err != nil {
		return err
	}
	segments := drift.Segments
	if len(segments) == 0 {
		scale := drift.Factor()
		if scale <= 0 {
			return fmt.Errorf("invalid speed factor %.9f", scale)
		}
		end := int(math.Round(target.Duration * 1000))
		segments = []timeline.Segment{{
			TargetStartMS: 0, TargetEndMS: end, ReferenceStartMS: drift.DelayMS,
			ReferenceEndMS: int(math.Round(float64(end)*scale)) + drift.DelayMS,
			OffsetMS:       drift.DelayMS, Scale: scale,
		}}
	}
	graph, err := piecewiseAudioFilter(track.Index, segments, drift.Gaps, refDuration)
	if err != nil {
		return err
	}

	codec := opts.Codec
	if codec == "" {
		codec = defaultAudioCodec(filepath.Ext(output), track.Codec)
	}
	args := []string{"-hide_banner", "-loglevel", "error", "-nostdin"}
	if opts.Overwrite {
		args = append(args, "-y")
	} else {
		args = append(args, "-n")
	}
	args = append(args,
		"-i", target.Path,
		"-filter_complex", graph,
		"-map", "[redsync_out]",
		"-map_metadata", "0",
		"-c:a", codec,
	)
	if opts.BitRate != "" {
		args = append(args, "-b:a", opts.BitRate)
	} else if isLossyCodec(codec) && track.BitRate > 0 {
		args = append(args, "-b:a", strconv.FormatInt(track.BitRate, 10))
	}
	if opts.Channels > 0 {
		args = append(args, "-ac", strconv.Itoa(opts.Channels))
	} else if strings.EqualFold(filepath.Ext(output), ".mp3") && track.Channels > 2 {
		args = append(args, "-ac", "2")
	}
	if opts.SampleRate > 0 {
		args = append(args, "-ar", strconv.Itoa(opts.SampleRate))
	}
	language := opts.Language
	if language == "" {
		language = track.Language
	}
	if language != "" && language != "und" {
		args = append(args, "-metadata:s:a:0", "language="+language)
	}
	// apad is intentionally unbounded so every codec receives enough samples.
	// An output-level duration is the authoritative stop condition; relying on
	// atrim alone leaves old FFmpeg builds consuming apad's infinite tail.
	if refDuration > 0 {
		args = append(args, "-t", strconv.FormatFloat(refDuration, 'f', 6, 64))
	}
	args = append(args, output)
	cmd, err := tools.Cmd(tools.FFmpeg, args...)
	if err != nil {
		return err
	}
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("render synced audio: %w", err)
	}
	return nil
}

func piecewiseAudioFilter(trackIndex int, segments []timeline.Segment, gaps []timeline.Gap, refDuration float64) (string, error) {
	if len(segments) == 0 {
		return "", fmt.Errorf("no timeline segments to render")
	}
	_ = gaps // segment target bounds already exclude target-only sections

	var graph []string
	inputs := make([]string, len(segments))
	if len(segments) == 1 {
		inputs[0] = fmt.Sprintf("[0:%d]", trackIndex)
	} else {
		labels := make([]string, len(segments))
		for i := range labels {
			labels[i] = fmt.Sprintf("[redsync_in_%d]", i)
			inputs[i] = labels[i]
		}
		graph = append(graph, fmt.Sprintf("[0:%d]asplit=%d%s", trackIndex, len(segments), strings.Join(labels, "")))
	}
	outputs := make([]string, 0, len(segments))
	for i, segment := range segments {
		scale := segment.Scale
		if scale <= 0 {
			return "", fmt.Errorf("segment %d has invalid scale %.9f", i+1, scale)
		}
		start := float64(segment.TargetStartMS) / 1000
		end := float64(segment.TargetEndMS) / 1000
		mappedStart := scale*start + float64(segment.OffsetMS)/1000
		if mappedStart < 0 {
			start += -mappedStart / scale
			mappedStart = 0
		}
		if end <= start {
			return "", fmt.Errorf("segment %d became empty after gap repair", i+1)
		}
		filters := []string{
			"atrim=start=" + strconv.FormatFloat(start, 'f', 6, 64) + ":end=" + strconv.FormatFloat(end, 'f', 6, 64),
			"asetpts=PTS-STARTPTS",
		}
		if math.Abs(scale-1) > 0.0000005 {
			filters = append(filters, "atempo="+strconv.FormatFloat(1/scale, 'f', 12, 64))
		}
		if mappedStart > 0 {
			filters = append(filters, "adelay="+strconv.Itoa(int(math.Round(mappedStart*1000)))+":all=1")
		}
		label := fmt.Sprintf("[redsync_piece_%d]", i)
		graph = append(graph, inputs[i]+strings.Join(filters, ",")+label)
		outputs = append(outputs, label)
	}
	mix := strings.Join(outputs, "")
	if len(outputs) == 1 {
		mix += "anull"
	} else {
		mix += fmt.Sprintf("amix=inputs=%d:duration=longest:normalize=0:dropout_transition=0", len(outputs))
	}
	if refDuration > 0 {
		mix += ",apad,atrim=duration=" + strconv.FormatFloat(refDuration, 'f', 6, 64)
	}
	mix += "[redsync_out]"
	graph = append(graph, mix)
	return strings.Join(graph, ";"), nil
}

func defaultAudioCodec(ext, source string) string {
	switch strings.ToLower(ext) {
	case ".m4a", ".mp4", ".aac":
		return "aac"
	case ".ac3":
		return "ac3"
	case ".eac3", ".ec3":
		return "eac3"
	case ".mp3":
		return "libmp3lame"
	case ".flac":
		return "flac"
	case ".wav", ".wave":
		return "pcm_s24le"
	case ".aiff", ".aif":
		return "pcm_s24be"
	case ".ogg", ".opus":
		return "libopus"
	case ".wv":
		return "wavpack"
	case ".wma":
		return "wmav2"
	case ".mka", ".mkv":
		switch source {
		case "aac", "ac3", "eac3", "flac", "alac":
			return source
		case "opus":
			return "libopus"
		case "vorbis":
			return "libvorbis"
		case "mp3":
			return "libmp3lame"
		default:
			return "flac"
		}
	default:
		return "flac"
	}
}

func isLossyCodec(codec string) bool {
	switch codec {
	case "aac", "ac3", "eac3", "mp3", "libmp3lame", "opus", "libopus", "vorbis", "libvorbis":
		return true
	default:
		return false
	}
}
