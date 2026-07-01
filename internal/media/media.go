// Package media inspects mkv/mp4 files and gives back a tidy view of their
// tracks. We use ffprobe's json output because it's everywhere and fast.
package media

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/720pixel/RedSync/internal/tools"
)

type Kind string

const (
	Video    Kind = "video"
	Audio    Kind = "audio"
	Subtitle Kind = "subtitle"
)

// Track is one stream inside a file, normalised to the bits we actually care
// about for muxing and naming.
type Track struct {
	Index    int    // ffmpeg stream index within the file
	Kind     Kind
	Codec    string // raw codec_name, e.g. eac3, hevc, subrip
	Language string // iso code, "und" if unknown
	Title    string // track name / handler title
	Default  bool
	Forced   bool
	HearImp  bool // hearing impaired (SDH)
	VisImp   bool // visual impaired / descriptive
	Channels int  // audio only
	Layout   string
	Atmos    bool // JOC present
	Width    int  // video only
	Height   int

	// fps as a rational so we never lose 24000/1001 to float rounding
	FPSNum int
	FPSDen int

	HDR HDRInfo // video only
}

// HDRInfo is what kind of high-dynamic-range a video stream carries.
type HDRInfo struct {
	DV       bool
	DVProfile int
	HDR10    bool
	HDR10Plus bool
	HLG      bool
	PQ       bool
}

// File is a probed source with its tracks split out by kind.
type File struct {
	Path        string
	Duration    float64 // seconds, 0 if unknown
	HasChapters bool
	Video       []Track
	Audio       []Track
	Subs        []Track
}

func (f File) AllTracks() []Track {
	out := append([]Track{}, f.Video...)
	out = append(out, f.Audio...)
	out = append(out, f.Subs...)
	return out
}

// FPS returns the video frame rate as a float, or 0 if there's no video.
func (f File) FPS() float64 {
	if len(f.Video) == 0 || f.Video[0].FPSDen == 0 {
		return 0
	}
	return float64(f.Video[0].FPSNum) / float64(f.Video[0].FPSDen)
}

// --- ffprobe json shapes (only the fields we read) ---

type probeOut struct {
	Streams []probeStream `json:"streams"`
	Format  struct {
		Duration string            `json:"duration"`
		Tags     map[string]string `json:"tags"`
	} `json:"format"`
	Chapters []json.RawMessage `json:"chapters"`
}

type probeStream struct {
	Index         int               `json:"index"`
	CodecName     string            `json:"codec_name"`
	CodecType     string            `json:"codec_type"`
	CodecTag      string            `json:"codec_tag_string"`
	Profile       string            `json:"profile"`
	Width         int               `json:"width"`
	Height        int               `json:"height"`
	Channels      int               `json:"channels"`
	ChannelLayout string            `json:"channel_layout"`
	RFrameRate    string            `json:"r_frame_rate"`
	AvgFrameRate  string            `json:"avg_frame_rate"`
	ColorTransfer string            `json:"color_transfer"`
	Disposition   map[string]int    `json:"disposition"`
	Tags          map[string]string `json:"tags"`
	SideData      []map[string]any  `json:"side_data_list"`
}

// Probe runs ffprobe and returns a normalised File.
func Probe(ctx context.Context, path string) (File, error) {
	cmd, err := tools.Cmd(tools.FFprobe,
		"-v", "quiet",
		"-print_format", "json",
		"-show_streams",
		"-show_format",
		"-show_chapters",
		path,
	)
	if err != nil {
		return File{}, err
	}
	out, err := cmd.Output()
	if err != nil {
		return File{}, fmt.Errorf("ffprobe failed on %s: %w", path, err)
	}
	var po probeOut
	if err := json.Unmarshal(out, &po); err != nil {
		return File{}, fmt.Errorf("parse ffprobe output: %w", err)
	}

	f := File{Path: path, HasChapters: len(po.Chapters) > 0}
	if d, err := strconv.ParseFloat(po.Format.Duration, 64); err == nil {
		f.Duration = d
	}
	for _, s := range po.Streams {
		t := normalise(s)
		switch t.Kind {
		case Video:
			f.Video = append(f.Video, t)
		case Audio:
			f.Audio = append(f.Audio, t)
		case Subtitle:
			f.Subs = append(f.Subs, t)
		}
	}

	// let mediainfo have the final say on HDR, it sees HDR10+ that ffprobe misses.
	if len(f.Video) > 0 {
		if mi, prof, ok := enrichHDR(ctx, path); ok {
			h := &f.Video[0].HDR
			h.DV = h.DV || mi.DV
			h.HDR10 = mi.HDR10 || h.HDR10
			h.HDR10Plus = mi.HDR10Plus || h.HDR10Plus
			h.HLG = mi.HLG || h.HLG
			h.PQ = mi.PQ || h.PQ
			if h.DVProfile == 0 {
				h.DVProfile = prof
			}
		}
	}
	// ffprobe only gives the bare ISO 639-2 code, so ask mkvmerge for the
	// BCP-47 form (fr-CA vs fr-FR) whenever there are subtitles to tell apart.
	if len(f.Subs) > 0 {
		if langs := enrichLangIETF(ctx, path); len(langs) > 0 {
			applyLangIETF(&f, langs)
		}
	}
	return f, nil
}

func normalise(s probeStream) Track {
	t := Track{
		Index:    s.Index,
		Codec:    s.CodecName,
		Language: tagOr(s.Tags, "language", "und"),
		Title:    tagOr(s.Tags, "title", ""),
		Width:    s.Width,
		Height:   s.Height,
		Channels: s.Channels,
		Layout:   s.ChannelLayout,
	}
	switch s.CodecType {
	case "video":
		t.Kind = Video
	case "audio":
		t.Kind = Audio
	case "subtitle":
		t.Kind = Subtitle
	}
	if d := s.Disposition; d != nil {
		t.Default = d["default"] == 1
		t.Forced = d["forced"] == 1
		t.HearImp = d["hearing_impaired"] == 1
		t.VisImp = d["visual_impaired"] == 1
	}
	t.FPSNum, t.FPSDen = parseRational(firstNonEmpty(s.AvgFrameRate, s.RFrameRate))
	// atmos shows up as a profile string on eac3
	if strings.Contains(strings.ToUpper(s.Profile), "JOC") || strings.Contains(strings.ToUpper(s.Profile), "ATMOS") {
		t.Atmos = true
	}
	if t.Kind == Video {
		t.HDR = detectHDR(s)
	}
	return t
}

func detectHDR(s probeStream) HDRInfo {
	var h HDRInfo
	tag := strings.ToLower(s.CodecTag)
	prof := strings.ToLower(s.Profile)

	// dolby vision: codec tag dvh1/dvhe, or a "DOVI configuration record" side
	// data block (that's how mkv-muxed DV shows up, not the words "dolby vision").
	if strings.HasPrefix(tag, "dvh") || strings.HasPrefix(tag, "dav") || strings.Contains(prof, "dolby vision") {
		h.DV = true
	}
	for _, sd := range s.SideData {
		dt, _ := sd["side_data_type"].(string)
		dtl := strings.ToLower(dt)
		if strings.Contains(dtl, "dovi") || strings.Contains(dtl, "dolby vision") {
			h.DV = true
			if p, ok := sd["dv_profile"].(float64); ok {
				h.DVProfile = int(p)
			}
			// bl signal compatibility: 1 = HDR10 base, 4 = HLG base. profile 5
			// (compat 0) is DV-only and has no separate HDR10 layer.
			if c, ok := sd["dv_bl_signal_compatibility_id"].(float64); ok {
				switch int(c) {
				case 1:
					h.HDR10 = true
					h.PQ = true
				case 4:
					h.HLG = true
				}
			}
		}
		if strings.Contains(dtl, "hdr10+") || strings.Contains(dtl, "hdr dynamic metadata") {
			h.HDR10Plus = true
		}
	}
	switch s.ColorTransfer {
	case "smpte2084":
		h.PQ = true
		h.HDR10 = true
	case "arib-std-b67":
		h.HLG = true
	}
	return h
}

// Range gives the release-style dynamic-range tag for a video track.
func (h HDRInfo) Range() string {
	switch {
	case h.HDR10Plus && h.DV:
		return "DV HDR10+"
	case h.HDR10 && h.DV:
		return "DV HDR10"
	case h.DV:
		return "DV"
	case h.HDR10Plus:
		return "HDR10+"
	case h.HDR10:
		return "HDR10"
	case h.HLG:
		return "HLG"
	case h.PQ:
		return "HDR"
	default:
		return "SDR"
	}
}

// --- helpers ---

func tagOr(tags map[string]string, key, def string) string {
	if tags == nil {
		return def
	}
	// tag keys can be cased oddly across muxers
	for k, v := range tags {
		if strings.EqualFold(k, key) && v != "" {
			return v
		}
	}
	return def
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" && v != "0/0" {
			return v
		}
	}
	return ""
}

// parseRational turns "24000/1001" into (24000, 1001). plain ints become n/1.
func parseRational(s string) (int, int) {
	if s == "" {
		return 0, 1
	}
	if i := strings.IndexByte(s, '/'); i >= 0 {
		num, err1 := strconv.Atoi(s[:i])
		den, err2 := strconv.Atoi(s[i+1:])
		if err1 == nil && err2 == nil && den != 0 {
			return num, den
		}
	}
	if f, err := strconv.ParseFloat(s, 64); err == nil {
		return int(math.Round(f * 1000)), 1000
	}
	return 0, 1
}
