package cli

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/720pixel/RedSync/internal/media"
)

// This is the "what can I make from these files" layer. Instead of dropping a
// raw track list on the user, we look across every source and work out the
// output versions that are actually possible (a DV+HDR hybrid, DV from HDR10+,
// or just the best single video), rank the audio, and dedupe subtitles. The
// interactive picker is built on top of this.

// videoRecipe is one way to produce the output video.
type videoRecipe struct {
	label     string // human description shown in the menu
	videoPath string // file the base video comes from
	dvPath    string // file the DV layer comes from, "" if no hybrid
	rng       string // resulting dynamic-range tag
	score     int    // higher = more desirable, used for ordering
}

// codecRank scores audio codecs roughly by quality for the "best audio" default.
var codecRank = map[string]int{
	"truehd": 60, "flac": 55, "dts": 40, "eac3": 35, "ac3": 25, "aac": 15, "opus": 14,
}

// videoRecipes enumerates every sensible output video from the sources, best
// first. Hybrids rank above plain videos, higher HDR tiers above lower.
func videoRecipes(files []media.File) []videoRecipe {
	var hdr, dv []media.File
	for _, f := range files {
		if len(f.Video) == 0 {
			continue
		}
		h := f.Video[0].HDR
		if h.HDR10 || h.HDR10Plus {
			hdr = append(hdr, f)
		}
		if h.DV {
			dv = append(dv, f)
		}
	}

	var out []videoRecipe
	// hybrids: pair an HDR10/HDR10+ base with a DV layer from another file.
	for _, base := range hdr {
		for _, d := range dv {
			if d.Path == base.Path {
				continue
			}
			rng := upgradeToDV(base.Video[0].HDR.Range())
			out = append(out, videoRecipe{
				label:     fmt.Sprintf("%s hybrid  (video: %s  +  DV: %s)", rng, short(base.Path), short(d.Path)),
				videoPath: base.Path,
				dvPath:    d.Path,
				rng:       rng,
				score:     1000 + tierScore(base.Video[0].HDR),
			})
		}
	}
	// plain: each video as-is.
	for _, f := range files {
		if len(f.Video) == 0 {
			continue
		}
		rng := f.Video[0].HDR.Range()
		out = append(out, videoRecipe{
			label:     fmt.Sprintf("%s  (video: %s)", rng, short(f.Path)),
			videoPath: f.Path,
			rng:       rng,
			score:     tierScore(f.Video[0].HDR),
		})
	}

	sort.SliceStable(out, func(i, j int) bool { return out[i].score > out[j].score })
	return out
}

// tierScore ranks dynamic range so DV+HDR10+ sits at the top and SDR at the
// bottom when we order recipes.
func tierScore(h media.HDRInfo) int {
	switch {
	case h.DV && h.HDR10Plus:
		return 6
	case h.DV && h.HDR10:
		return 5
	case h.HDR10Plus:
		return 4
	case h.DV:
		return 3
	case h.HDR10:
		return 2
	case h.HLG:
		return 1
	default:
		return 0
	}
}

// bestAudioPath returns the file whose top audio track looks best, so the picker
// can pre-select a sensible default.
func bestAudioPath(files []media.File) string {
	best, bestScore := "", -1
	for _, f := range files {
		for _, a := range f.Audio {
			s := audioScore(a)
			if s > bestScore {
				bestScore, best = s, f.Path
			}
		}
	}
	return best
}

func audioScore(a media.Track) int {
	s := codecRank[a.Codec]
	s += a.Channels * 3
	if a.Atmos {
		s += 100
	}
	return s
}

// bestAudioLabel describes why a source won the "best audio" pick, for the UI.
func bestAudioLabel(files []media.File) string {
	best, score := media.Track{}, -1
	for _, f := range files {
		for _, a := range f.Audio {
			if s := audioScore(a); s > score {
				score, best = s, a
			}
		}
	}
	if score < 0 {
		return ""
	}
	s := fmt.Sprintf("%s %dch", strings.ToUpper(best.Codec), best.Channels)
	if best.Atmos {
		s += " Atmos"
	}
	return s
}

// mostSubsCount is how many subtitle tracks the richest source has.
func mostSubsCount(files []media.File) int {
	n := 0
	for _, f := range files {
		if len(f.Subs) > n {
			n = len(f.Subs)
		}
	}
	return n
}

func short(p string) string { return filepath.Base(p) }
