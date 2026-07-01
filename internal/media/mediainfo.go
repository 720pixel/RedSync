package media

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"

	"github.com/720pixel/RedSync/internal/tools"
)

// mediainfo-based HDR detection. ffprobe is great for stream layout and catches
// Dolby Vision, but it routinely misses HDR10+ (the SMPTE ST 2094-40 dynamic
// metadata). MediaInfo reports all of it on a finished file, so we use it as the
// authority for the HDR fields and fall back to the ffprobe-derived guess when
// MediaInfo isn't available.

type miReport struct {
	Media struct {
		Track []map[string]any `json:"track"`
	} `json:"media"`
}

// enrichHDR runs mediainfo and, if it finds a video track, returns a better
// HDRInfo plus the DV profile. ok is false when mediainfo gave us nothing useful.
func enrichHDR(ctx context.Context, path string) (HDRInfo, int, bool) {
	cmd, err := tools.Cmd(tools.Mediainf, "--Output=JSON", path)
	if err != nil {
		return HDRInfo{}, 0, false
	}
	out, err := cmd.Output()
	if err != nil {
		return HDRInfo{}, 0, false
	}
	var rep miReport
	if json.Unmarshal(out, &rep) != nil {
		return HDRInfo{}, 0, false
	}
	for _, t := range rep.Media.Track {
		if s, _ := t["@type"].(string); s != "Video" {
			continue
		}
		return fromMediainfo(t)
	}
	return HDRInfo{}, 0, false
}

// fromMediainfo maps MediaInfo's HDR fields onto our model, in the same priority
// order we use when picking a range tag (DV+HDR first, SDR last).
func fromMediainfo(t map[string]any) (HDRInfo, int, bool) {
	get := func(k string) string {
		v, _ := t[k].(string)
		return v
	}
	format := get("HDR_Format")            // e.g. "Dolby Vision / SMPTE ST 2086 / SMPTE ST 2094-40"
	commercial := get("HDR_Format_Commercial")
	compat := get("HDR_Format_Compatibility")
	profile := get("HDR_Format_Profile") // e.g. "dvhe.08.06"
	transfer := get("transfer_characteristics")

	all := strings.ToLower(format + " " + commercial + " " + compat)
	if all == "  " {
		return HDRInfo{}, 0, false // no hdr fields at all
	}
	var h HDRInfo

	if strings.Contains(strings.ToLower(format), "dolby vision") || strings.Contains(strings.ToLower(profile), "dvh") {
		h.DV = true
	}
	// HDR10+ shows up as SMPTE ST 2094-40 (or the commercial "HDR10+").
	if strings.Contains(all, "2094-40") || strings.Contains(all, "hdr10+") {
		h.HDR10Plus = true
		h.HDR10 = true
	}
	// plain HDR10 / HDR10-compatible base.
	if strings.Contains(all, "hdr10") || strings.Contains(all, "2086") || strings.Contains(strings.ToUpper(transfer), "PQ") {
		h.HDR10 = true
		h.PQ = true
	}
	if strings.Contains(all, "hlg") || strings.Contains(strings.ToUpper(transfer), "HLG") {
		h.HLG = true
	}

	return h, dvProfileFrom(profile), true
}

// dvProfileFrom pulls the profile number out of strings like "dvhe.08.06".
func dvProfileFrom(s string) int {
	parts := strings.Split(s, ".")
	if len(parts) >= 2 {
		if n, err := strconv.Atoi(parts[1]); err == nil {
			return n
		}
	}
	return 0
}
