package cli

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/720pixel/RedSync/internal/media"
	"github.com/720pixel/RedSync/internal/ui"
)

// The interactive picker. Sources are numbered once (1, 2, 3 ...) and never
// reset, so you compose an output with short codes: v1 = video from source 1,
// a2 = audio from source 2, s3 = subtitles from source 3, c1 = chapters from
// source 1, dv2 = Dolby Vision layer from source 2 (which makes a hybrid). It
// also offers ready-made "possible versions" you can pick by number.
func runInteractive(ctx context.Context, args []string, unique bool) error {
	files, err := probeAll(ctx, expand(args))
	if err != nil {
		return err
	}
	if len(files) == 0 {
		return fmt.Errorf("no media files to work with")
	}

	fmt.Fprintln(os.Stderr, ui.Banner(Version))
	printSources(files)

	versions := buildVersions(files, unique)
	printVersions(versions, files, unique)
	printLegend()

	in := bufio.NewReader(os.Stdin)
	fmt.Fprint(os.Stderr, ui.Accent.Render("compose")+ui.Muted.Render(" (a version number, or codes like ")+"v1 dv2 a2 s2 c2"+ui.Muted.Render("): "))
	line, _ := in.ReadString('\n')
	line = strings.TrimSpace(line)

	var sel selection
	switch {
	case line == "" && len(versions) > 0:
		sel = versions[0].sel
	case isNumber(line):
		n, _ := strconv.Atoi(line)
		if n < 1 || n > len(versions) {
			return fmt.Errorf("no version %d", n)
		}
		sel = versions[n-1].sel
	default:
		sel, err = parseCodes(line, files)
		if err != nil {
			return err
		}
	}
	sel.unique = unique
	// output folder, defaulting to alongside the sources
	defDir := filepath.Dir(files[0].Path)
	fmt.Fprint(os.Stderr, ui.Accent.Render("output folder")+ui.Muted.Render(" [enter for "+defDir+"]: "))
	dir, _ := in.ReadString('\n')
	dir = strings.TrimSpace(dir)
	if dir == "" {
		dir = defDir
	}
	sel.outDir = dir

	fmt.Fprint(os.Stderr, ui.Accent.Render("build")+ui.Muted.Render(" now, or type 'dry' for a dry run [build/dry]: "))
	ans, _ := in.ReadString('\n')
	if strings.HasPrefix(strings.ToLower(strings.TrimSpace(ans)), "d") {
		sel.dryRun = true
	}
	return runSync(ctx, sel)
}

func printSources(files []media.File) {
	ui.Section("sources")
	for i, f := range files {
		ui.Field(fmt.Sprintf("[%d]", i+1), ui.Title.Render(filepath.Base(f.Path)))
		ui.Field("", ui.Muted.Render(sourceSummary(f)))
	}
}

// sourceSummary is the one-line "what's in here" for a source.
func sourceSummary(f media.File) string {
	var parts []string
	if len(f.Video) > 0 {
		v := f.Video[0]
		parts = append(parts, fmt.Sprintf("video %s %dx%d", v.HDR.Range(), v.Width, v.Height))
	}
	if len(f.Audio) > 0 {
		a := f.Audio[0]
		s := fmt.Sprintf("audio %s %dch", a.Language, a.Channels)
		if a.Atmos {
			s += " Atmos"
		}
		if len(f.Audio) > 1 {
			s += fmt.Sprintf(" (+%d)", len(f.Audio)-1)
		}
		parts = append(parts, s)
	}
	if len(f.Subs) > 0 {
		langs := make([]string, 0, len(f.Subs))
		for _, s := range f.Subs {
			tag := s.Language
			if s.HearImp {
				tag += " SDH"
			}
			langs = append(langs, tag)
		}
		parts = append(parts, "subs "+strings.Join(langs, ","))
	}
	if f.HasChapters {
		parts = append(parts, "chapters")
	}
	return strings.Join(parts, "  ·  ")
}

// ivVersion is a ready-made recipe shown in the menu.
type ivVersion struct {
	label string
	codes string
	sel   selection
}

func printVersions(vs []ivVersion, files []media.File, unique bool) {
	ui.Section("possible versions")
	for i, v := range vs {
		label := ui.Title.Render(v.label)
		if i == 0 {
			label += "  " + ui.Pill("★ best pick")
		}
		ui.Field(fmt.Sprintf("[%d]", i+1), label+"   "+ui.Accent.Render(v.codes))
	}
	// say why the top one won, in plain terms
	if len(vs) > 0 {
		why := []string{"highest video tier"}
		if a := bestAudioLabel(files); a != "" {
			why = append(why, "best audio ("+a+")")
		}
		if n := mostSubsCount(files); n > 0 {
			subWhy := fmt.Sprintf("most subtitles (%d)", n)
			if unique && len(subSourcesByRichness(files)) > 1 {
				subWhy += ", plus unique ones from the other sources"
			}
			why = append(why, subWhy)
		}
		ui.Field("", ui.Muted.Render("best pick = "+strings.Join(why, ", ")))
	}
}

func printLegend() {
	ui.Section("compose your own")
	ui.Field("v<n>", ui.Muted.Render("video from source n")+"        "+ui.Accent.Render("dv<n>")+ui.Muted.Render("  DV layer from n (builds a hybrid)"))
	ui.Field("a<n>", ui.Muted.Render("audio from source n")+"        "+ui.Accent.Render("s<n>")+ui.Muted.Render("   subtitles from source n"))
	ui.Field("c<n>", ui.Muted.Render("chapters from source n")+"     "+ui.Muted.Render("repeat for more, e.g. a1 a2"))
}

// buildVersions proposes complete recipes, best first, each with a copyable code
// string. Audio comes from the best-sounding source. Subtitles come from the
// richest source alone, unless unique is set - then every subtitle-bearing
// source is handed over and runSync's --unique dedup merges in whatever each
// smaller source alone has, instead of throwing it away.
func buildVersions(files []media.File, unique bool) []ivVersion {
	audioN := srcNum(files, bestAudioPath(files))
	subsN := srcNum(files, richestSubs(files))
	var subPaths []string
	if unique {
		subPaths = subSourcesByRichness(files)
	}

	seen := map[string]bool{}
	var out []ivVersion
	for _, r := range videoRecipes(files) {
		if seen[r.rng] {
			continue // one entry per range tier
		}
		seen[r.rng] = true

		vN := srcNum(files, r.videoPath)
		if vN == 0 {
			continue
		}
		chapN := vN
		if !files[vN-1].HasChapters {
			if c := firstWithChapters(files); c > 0 {
				chapN = c
			}
		}

		sel := selection{video: r.videoPath, unique: unique}
		codes := fmt.Sprintf("v%d", vN)
		if r.dvPath != "" {
			sel.dv = r.dvPath
			codes += fmt.Sprintf(" dv%d", srcNum(files, r.dvPath))
		}
		if audioN > 0 {
			sel.audio = []string{files[audioN-1].Path}
			codes += fmt.Sprintf(" a%d", audioN)
		}
		if len(subPaths) > 0 {
			sel.subs = subPaths
			for _, p := range subPaths {
				codes += fmt.Sprintf(" s%d", srcNum(files, p))
			}
		} else if subsN > 0 {
			sel.subs = []string{files[subsN-1].Path}
			codes += fmt.Sprintf(" s%d", subsN)
		}
		if files[chapN-1].HasChapters {
			sel.chapters = files[chapN-1].Path
			codes += fmt.Sprintf(" c%d", chapN)
		}
		out = append(out, ivVersion{label: r.label, codes: codes, sel: sel})
	}
	return out
}

// parseCodes turns "v1 dv2 a2 s2 c2" into a selection.
var codeRe = regexp.MustCompile(`^(dv|v|a|s|c)([0-9]+)$`)

func parseCodes(line string, files []media.File) (selection, error) {
	var sel selection
	seenA, seenS := map[string]bool{}, map[string]bool{}
	for _, tok := range strings.Fields(strings.ToLower(line)) {
		m := codeRe.FindStringSubmatch(tok)
		if m == nil {
			return sel, fmt.Errorf("don't understand %q (use codes like v1 a2 s2 c1)", tok)
		}
		n, _ := strconv.Atoi(m[2])
		if n < 1 || n > len(files) {
			return sel, fmt.Errorf("there is no source %d", n)
		}
		p := files[n-1].Path
		switch m[1] {
		case "v":
			sel.video = p
		case "dv":
			sel.dv = p
		case "a":
			if !seenA[p] {
				seenA[p] = true
				sel.audio = append(sel.audio, p)
			}
		case "s":
			if !seenS[p] {
				seenS[p] = true
				sel.subs = append(sel.subs, p)
			}
		case "c":
			sel.chapters = p
		}
	}
	if sel.video == "" {
		return sel, fmt.Errorf("pick a video with v<n>, e.g. v1")
	}
	return sel, nil
}

// --- small helpers ---

func srcNum(files []media.File, path string) int {
	for i, f := range files {
		if f.Path == path {
			return i + 1
		}
	}
	return 0
}

func richestSubs(files []media.File) string {
	best, n := "", -1
	for _, f := range files {
		if len(f.Subs) > n {
			n, best = len(f.Subs), f.Path
		}
	}
	return best
}

// subSourcesByRichness returns every source that carries subtitles, most
// subtitle tracks first. Feeding all of them (instead of just the richest) to
// --unique's dedup is what lets a smaller source's one-of-a-kind language
// survive instead of being discarded along with its duplicates.
func subSourcesByRichness(files []media.File) []string {
	idx := make([]int, 0, len(files))
	for i, f := range files {
		if len(f.Subs) > 0 {
			idx = append(idx, i)
		}
	}
	sort.SliceStable(idx, func(a, b int) bool {
		return len(files[idx[a]].Subs) > len(files[idx[b]].Subs)
	})
	out := make([]string, len(idx))
	for i, fi := range idx {
		out[i] = files[fi].Path
	}
	return out
}

func firstWithChapters(files []media.File) int {
	for i, f := range files {
		if f.HasChapters {
			return i + 1
		}
	}
	return 0
}

func isNumber(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}
