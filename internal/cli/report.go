package cli

import (
	"encoding/json"
	"os"
	"strings"
	"time"

	"github.com/720pixel/RedSync/internal/media"
	rsync "github.com/720pixel/RedSync/internal/sync"
)

// This file holds the machine-readable side of RedSync: stable JSON shapes for
// `analyze`, for a sync/hybrid run, and for `doctor`, all written to stdout so a
// script can drive RedSync and parse the result. The pretty terminal output
// stays on stderr, so the two never mix.

// emitJSON writes v to stdout as indented JSON.
func emitJSON(v any) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

// --- analyze ---

type jsonFile struct {
	Path      string      `json:"path"`
	Duration  float64     `json:"duration_seconds"`
	FPS       float64     `json:"fps"`
	Chapters  bool        `json:"chapters"`
	Video     []jsonTrack `json:"video"`
	Audio     []jsonTrack `json:"audio"`
	Subtitles []jsonTrack `json:"subtitles"`
}

type jsonTrack struct {
	Index       int      `json:"index"`
	Codec       string   `json:"codec"`
	Language    string   `json:"language"`
	Title       string   `json:"title,omitempty"`
	Default     bool     `json:"default"`
	Forced      bool     `json:"forced,omitempty"`
	SDH         bool     `json:"sdh,omitempty"`
	Descriptive bool     `json:"descriptive,omitempty"`
	Channels    int      `json:"channels,omitempty"`
	Atmos       bool     `json:"atmos,omitempty"`
	Width       int      `json:"width,omitempty"`
	Height      int      `json:"height,omitempty"`
	Range       string   `json:"range,omitempty"`
	HDR         *jsonHDR `json:"hdr,omitempty"`
}

type jsonHDR struct {
	DV        bool `json:"dv"`
	DVProfile int  `json:"dv_profile,omitempty"`
	HDR10     bool `json:"hdr10"`
	HDR10Plus bool `json:"hdr10_plus"`
	HLG       bool `json:"hlg"`
}

func toJSONFile(f media.File) jsonFile {
	jf := jsonFile{Path: f.Path, Duration: f.Duration, FPS: f.FPS(), Chapters: f.HasChapters}
	for _, v := range f.Video {
		jf.Video = append(jf.Video, toJSONTrack(v))
	}
	for _, a := range f.Audio {
		jf.Audio = append(jf.Audio, toJSONTrack(a))
	}
	for _, s := range f.Subs {
		jf.Subtitles = append(jf.Subtitles, toJSONTrack(s))
	}
	return jf
}

func toJSONTrack(t media.Track) jsonTrack {
	jt := jsonTrack{
		Index: t.Index, Codec: t.Codec, Language: t.Language, Title: t.Title,
		Default: t.Default, Forced: t.Forced, SDH: t.HearImp, Descriptive: t.VisImp,
		Channels: t.Channels, Atmos: t.Atmos, Width: t.Width, Height: t.Height,
	}
	if t.Kind == media.Video {
		jt.Range = t.HDR.Range()
		jt.HDR = &jsonHDR{
			DV: t.HDR.DV, DVProfile: t.HDR.DVProfile, HDR10: t.HDR.HDR10,
			HDR10Plus: t.HDR.HDR10Plus, HLG: t.HDR.HLG,
		}
	}
	return jt
}

// --- run (sync / hybrid) ---

type jsonRun struct {
	Output          string           `json:"output"`
	Range           string           `json:"range"`
	Hybrid          bool             `json:"hybrid"`
	DryRun          bool             `json:"dry_run"`
	Sources         []jsonSource     `json:"sources"`
	MkvmergeCommand string           `json:"mkvmerge_command"`
	TimingsMS       map[string]int64 `json:"timings_ms"`
	TotalMS         int64            `json:"total_ms"`
}

type jsonSource struct {
	Path       string   `json:"path"`
	Roles      []string `json:"roles"`
	DelayMS    int      `json:"delay_ms"`
	Linear     string   `json:"linear,omitempty"`
	FPSStretch bool     `json:"fps_stretch"`
	Score      float64  `json:"score,omitempty"`
}

func buildRunJSON(job rsync.Job, sel selection, rng string, stages []stageTime, total time.Duration) jsonRun {
	r := jsonRun{
		Output:          job.Output,
		Range:           rng,
		Hybrid:          sel.makesHybrid(),
		DryRun:          sel.dryRun,
		MkvmergeCommand: rsync.CommandString(job),
		TimingsMS:       map[string]int64{},
		TotalMS:         total.Milliseconds(),
	}
	for _, s := range stages {
		r.TimingsMS[strings.ReplaceAll(s.name, " ", "_")] = s.dur.Milliseconds()
	}
	for _, c := range job.Contribs {
		var roles []string
		if len(c.Video) > 0 {
			roles = append(roles, "video")
		}
		if len(c.Audio) > 0 {
			roles = append(roles, "audio")
		}
		if len(c.Subs) > 0 {
			roles = append(roles, "subtitles")
		}
		if c.Chapters {
			roles = append(roles, "chapters")
		}
		r.Sources = append(r.Sources, jsonSource{
			Path:       c.File.Path,
			Roles:      roles,
			DelayMS:    c.Drift.DelayMS,
			Linear:     c.Drift.Linear,
			FPSStretch: c.Drift.FPSStretch,
			Score:      c.Drift.Score,
		})
	}
	return r
}

// --- doctor ---

type jsonDoctor struct {
	OK    bool         `json:"ok"`
	Tools []jsonTooled `json:"tools"`
}

type jsonTooled struct {
	Name    string `json:"name"`
	Found   bool   `json:"found"`
	Path    string `json:"path,omitempty"`
	Bundled bool   `json:"bundled"`
	Hint    string `json:"hint,omitempty"`
}
