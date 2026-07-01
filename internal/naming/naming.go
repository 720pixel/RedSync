// Package naming works out a sensible output filename.
//
// Most of the time the chosen video file already carries a proper release name,
// so the smart move is to keep that base and only fix the bits that actually
// changed: the audio tag (channels / Atmos) and the dynamic-range tag (after we
// build a DV or HDR hybrid). Mappings follow the usual scene/P2P release tags so
// names stay consistent with the rest of a release.
package naming

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/720pixel/RedSync/internal/media"
)

// audio codec -> release tag
var audioCodecMap = map[string]string{
	"eac3": "DDP", "e-ac-3": "DDP",
	"ac3": "DD", "ac-3": "DD",
	"aac": "AAC",
	"flac": "FLAC",
	"truehd": "TrueHD",
	"dts": "DTS",
	"opus": "OPUS",
}

// dynamic range tag, longest-match first so "DV HDR10+" wins over "HDR10".
var rangeTags = []string{"DV HDR10+", "DV HDR10", "HDR10+", "HDR10", "DV", "HLG", "HDR"}

// Plan describes the final muxed result well enough to name it.
type Plan struct {
	VideoPath string       // file the video came from (our naming base)
	Audio     *media.Track // primary audio we muxed in, if any
	Range     string       // final dynamic-range tag, e.g. "DV HDR10+" (empty = unchanged)
	Override  string       // explicit -o from the user, wins outright
	OutDir    string       // where the file should land
}

// Build returns the final output path.
func Build(p Plan) string {
	if p.Override != "" {
		return placeIn(p.OutDir, ensureMKV(p.Override))
	}
	base := strings.TrimSuffix(filepath.Base(p.VideoPath), filepath.Ext(p.VideoPath))

	if p.Audio != nil {
		base = patchAudioTag(base, *p.Audio)
	}
	if p.Range != "" && p.Range != "SDR" {
		base = patchRangeTag(base, p.Range)
	}
	base = sanitize(base)
	return placeIn(p.OutDir, base+".mkv")
}

// patchAudioTag upgrades an existing audio tag in place (DDP5.1 -> DDP5.1.Atmos,
// 5.1 -> 7.1), or appends one if the name has none.
func patchAudioTag(name string, a media.Track) string {
	tag := audioCodecMap[strings.ToLower(a.Codec)]
	if tag == "" {
		tag = strings.ToUpper(a.Codec)
	}
	ch := channelString(a)

	// try to rewrite an existing "DDP5.1"-style token
	re := regexp.MustCompile(`(?i)\b(DDP|DD|AAC|FLAC|TrueHD|DTS|OPUS)([0-9]\.[0-9])(\.Atmos)?`)
	if re.MatchString(name) {
		repl := tag + ch
		if a.Atmos {
			repl += ".Atmos"
		}
		return re.ReplaceAllString(name, repl)
	}
	// nothing to rewrite - append before any group tag
	add := tag + ch
	if a.Atmos {
		add += ".Atmos"
	}
	return appendToken(name, add)
}

func patchRangeTag(name, rng string) string {
	dotted := strings.ReplaceAll(rng, " ", ".") // "DV HDR10+" -> "DV.HDR10+"
	// if any range token already exists, swap it for the new one
	for _, t := range rangeTags {
		dt := strings.ReplaceAll(t, " ", ".")
		re := regexp.MustCompile(`(?i)\b` + regexp.QuoteMeta(dt) + `\b`)
		if re.MatchString(name) {
			return re.ReplaceAllString(name, dotted)
		}
	}
	return appendToken(name, dotted)
}

func channelString(a media.Track) string {
	switch {
	case a.Channels >= 8:
		return "7.1"
	case a.Channels == 7:
		return "6.1"
	case a.Channels >= 6:
		return "5.1"
	case a.Channels == 2:
		return "2.0"
	case a.Channels == 1:
		return "1.0"
	default:
		if a.Layout != "" {
			return ""
		}
		return ""
	}
}

// appendToken slips a token in before a trailing -GROUP tag if there is one.
func appendToken(name, token string) string {
	if i := strings.LastIndex(name, "-"); i > 0 && !strings.Contains(name[i:], ".") {
		return name[:i] + "." + token + name[i:]
	}
	return name + "." + token
}

// sanitize normalises a release name: spaces to dots, drop junk, collapse
// repeats. keeps it filesystem-safe on both linux and windows.
func sanitize(s string) string {
	s = strings.ReplaceAll(s, "&", " and ")
	s = regexp.MustCompile(`[\\/:*?"<>|]+`).ReplaceAllString(s, "")
	s = regexp.MustCompile(`\s+`).ReplaceAllString(s, ".")
	s = regexp.MustCompile(`\.+`).ReplaceAllString(s, ".")
	s = strings.Trim(s, ". ")
	if len(s) > 255 {
		s = s[:255]
	}
	return s
}

func ensureMKV(name string) string {
	if strings.EqualFold(filepath.Ext(name), ".mkv") {
		return name
	}
	return name + ".mkv"
}

func placeIn(dir, name string) string {
	// An absolute -o wins outright: the user named an exact path, so --out-dir
	// must not be joined onto it (that would give /dir/abs/name).
	if dir == "" || filepath.IsAbs(name) {
		return name
	}
	return filepath.Join(dir, name)
}

// Describe is a short human summary of what the name will be, for the dry-run UI.
func Describe(p Plan) string {
	return fmt.Sprintf("%s", filepath.Base(Build(p)))
}
