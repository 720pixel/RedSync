// Package sync muxes tracks pulled from several mkv sources into one file,
// lining each external source up against the video with a measured delay (and a
// frame-rate stretch when the clocks differ). Video is always stream-copied, so
// the only real cost is the short audio probes and the mux itself.
package sync

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/720pixel/RedSync/internal/media"
	"github.com/720pixel/RedSync/internal/tools"
	"github.com/720pixel/RedSync/internal/ui"
)

// Contribution is everything we take from one source file, plus how that source
// lines up against the reference video.
type Contribution struct {
	File     media.File
	Drift    Drift
	Video    []media.Track
	Audio    []media.Track
	Subs     []media.Track
	Chapters bool

	// FPSFix pins the video track's frame rate via --default-duration. Needed
	// when the video is a raw HEVC elementary stream (our DV+HDR hybrid output),
	// which carries no container timing - without this mkvmerge guesses 25fps and
	// the whole mux drifts.
	FPSFix bool
}

func (c Contribution) reference() bool { return c.Drift.DelayMS == 0 && c.Drift.Linear == "" }

// Job is a full mux: the reference is index 0, the rest get synced to it.
type Job struct {
	Contribs []Contribution
	Output   string
	DryRun   bool
}

// Run measures any sources that haven't been measured yet, builds the mkvmerge
// command, and runs it (unless DryRun).
func Run(ctx context.Context, j Job) error {
	if len(j.Contribs) == 0 {
		return fmt.Errorf("nothing to mux")
	}
	args := build(j)

	if j.DryRun {
		ui.Section("dry run - mkvmerge command")
		fmt.Println("mkvmerge " + strings.Join(quoteAll(args), " "))
		return nil
	}

	// --gui-mode makes mkvmerge emit "#GUI#progress N%" lines we can turn into a
	// clean bar instead of its raw progress spam.
	cmd, err := tools.Cmd(tools.MkvMerge, append([]string{"--gui-mode"}, args...)...)
	if err != nil {
		return err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		return err
	}

	bar := ui.NewBar("muxing")
	sc := bufio.NewScanner(stdout)
	for sc.Scan() {
		if m := guiProgress.FindStringSubmatch(sc.Text()); m != nil {
			pct, _ := strconv.Atoi(m[1])
			bar.Update(pct)
		} else if w := guiWarn.FindStringSubmatch(sc.Text()); w != nil {
			ui.Warn("mkvmerge: " + w[1])
		}
	}
	if err := cmd.Wait(); err != nil {
		return fmt.Errorf("mkvmerge: %w", err)
	}
	bar.Done("wrote " + filepath.Base(j.Output))
	return nil
}

var (
	guiProgress = regexp.MustCompile(`#GUI#progress (\d+)%`)
	guiWarn     = regexp.MustCompile(`#GUI#(?:warning|error) (.+)`)
)

// CommandString returns the full mkvmerge command line this job would run, shell
// quoted, so dry-runs and JSON callers can inspect or reuse it verbatim.
func CommandString(j Job) string {
	return "mkvmerge " + strings.Join(quoteAll(build(j)), " ")
}

// build turns a Job into the full mkvmerge argv.
func build(j Job) []string {
	args := []string{"--no-date", "--output", j.Output}

	var order []string // for --track-order, video first then audio then subs
	defaultAudioSet := false

	for fi, c := range j.Contribs {
		seg := perFile(c, fi, &order, &defaultAudioSet)
		args = append(args, seg...)
	}

	if len(order) > 0 {
		args = append(args, "--track-order", strings.Join(order, ","))
	}
	return args
}

// perFile emits the options + parenthesised input for a single source file.
func perFile(c Contribution, fileIdx int, order *[]string, defAudioSet *bool) []string {
	var a []string

	// which track kinds survive from this file
	if len(c.Video) == 0 {
		a = append(a, "-D")
	} else {
		a = append(a, "--video-tracks", ids(c.Video))
	}
	if len(c.Audio) == 0 {
		a = append(a, "-A")
	} else {
		a = append(a, "--audio-tracks", ids(c.Audio))
	}
	if len(c.Subs) == 0 {
		a = append(a, "-S")
	} else {
		a = append(a, "--subtitle-tracks", ids(c.Subs))
	}
	if c.Chapters {
		if d := c.Drift.DelayMS; d != 0 {
			a = append(a, "--chapter-sync", strconv.Itoa(d))
		}
	} else {
		a = append(a, "--no-chapters")
	}
	a = append(a, "--no-global-tags", "--no-attachments")

	syncSuffix := syncArg(c.Drift)

	for _, v := range c.Video {
		a = append(a, trackMeta(v, true, false)...)
		if c.FPSFix && v.FPSNum > 0 && v.FPSDen > 0 {
			a = append(a, "--default-duration", fmt.Sprintf("%d:%d/%dfps", v.Index, v.FPSNum, v.FPSDen))
			a = append(a, "--fix-bitstream-timing-information", fmt.Sprintf("%d:1", v.Index))
		}
		a = append(a, "--compression", fmt.Sprintf("%d:none", v.Index))
		*order = append(*order, fmt.Sprintf("%d:%d", fileIdx, v.Index))
	}
	for _, au := range c.Audio {
		def := !*defAudioSet && au.Default
		a = append(a, trackMeta(au, def, false)...)
		if syncSuffix != "" {
			a = append(a, "--sync", fmt.Sprintf("%d:%s", au.Index, syncSuffix))
		}
		a = append(a, "--compression", fmt.Sprintf("%d:none", au.Index))
		if def {
			*defAudioSet = true
		}
		*order = append(*order, fmt.Sprintf("%d:%d", fileIdx, au.Index))
	}
	for _, s := range c.Subs {
		a = append(a, trackMeta(s, s.Default, s.Forced)...)
		if syncSuffix != "" {
			a = append(a, "--sync", fmt.Sprintf("%d:%s", s.Index, syncSuffix))
		}
		a = append(a, "--sub-charset", fmt.Sprintf("%d:UTF-8", s.Index))
		a = append(a, "--compression", fmt.Sprintf("%d:none", s.Index))
		*order = append(*order, fmt.Sprintf("%d:%d", fileIdx, s.Index))
	}

	a = append(a, "(", c.File.Path, ")")
	return a
}

// trackMeta carries language / title / disposition forward so we never lose the
// original tags - that was a hard requirement.
func trackMeta(t media.Track, def, forced bool) []string {
	id := t.Index
	out := []string{
		"--language", fmt.Sprintf("%d:%s", id, muxLang(t.Language)),
		"--default-track", fmt.Sprintf("%d:%s", id, yesno(def)),
	}
	if t.Title != "" {
		out = append(out, "--track-name", fmt.Sprintf("%d:%s", id, t.Title))
	}
	if t.Kind == media.Subtitle {
		out = append(out, "--forced-track", fmt.Sprintf("%d:%s", id, yesno(forced)))
		out = append(out, "--hearing-impaired-flag", fmt.Sprintf("%d:%s", id, yesno(t.HearImp)))
	}
	if t.Kind == media.Audio && t.VisImp {
		out = append(out, "--visual-impaired-flag", fmt.Sprintf("%d:yes", id))
	}
	return out
}

// syncArg builds the value after "id:" for --sync: delay, plus the linear factor
// when we're correcting frame-rate drift.
func syncArg(d Drift) string {
	if d.DelayMS == 0 && d.Linear == "" {
		return ""
	}
	if d.Linear != "" {
		return fmt.Sprintf("%d,%s", d.DelayMS, d.Linear)
	}
	return strconv.Itoa(d.DelayMS)
}

func ids(ts []media.Track) string {
	parts := make([]string, len(ts))
	for i, t := range ts {
		parts[i] = strconv.Itoa(t.Index)
	}
	return strings.Join(parts, ",")
}

// muxLang maps a couple of language tags mkvmerge won't accept to ones it will.
func muxLang(l string) string {
	switch l {
	case "", "none":
		return "und"
	case "cmn":
		return "zh"
	case "yue":
		return "zh-yue"
	}
	return l
}

func yesno(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}

func quoteAll(args []string) []string {
	out := make([]string, len(args))
	for i, a := range args {
		if strings.ContainsAny(a, " ()") {
			out[i] = "'" + a + "'"
		} else {
			out[i] = a
		}
	}
	return out
}
