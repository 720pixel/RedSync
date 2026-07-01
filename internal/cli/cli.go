package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/720pixel/RedSync/internal/media"
	"github.com/720pixel/RedSync/internal/ui"
	"github.com/spf13/cobra"
)

// Version is stamped at build time via -ldflags. Defaults to "dev" otherwise.
var Version = "dev"

// rootFlags holds everything the top-level command can take, so you can sync
// straight from `RedSync ...` without remembering a subcommand.
type rootFlags struct {
	video    string
	dv       string
	plus     string // --hdr10plus: HDR10+ source grafted onto the DV --video base
	audio    []string
	subs     []string
	subsAlt  []string // --subtitles, merged into subs
	chapters string
	chapsAlt string // --chaps, merged into chapters
	output   string
	outDir   string

	quick      bool // --sync: treat positional files as quick source list
	audioOnly  bool
	subsOnly   bool
	chapsOnly  bool
	keep       bool // keep the video source's own audio + subs + chapters too
	unique     bool // dedupe subtitles to one per language/role, merging in unique ones from every source
	dryRun     bool

	shift    int  // manual constant delay (ms); bypasses audio measurement
	shiftSet bool // whether --shift was supplied
}

// flagJSON and flagQuiet are persistent across every subcommand: --json emits a
// machine-readable result on stdout (for scripting RedSync), --quiet drops the
// decorative stderr output. Both are wired once in Execute.
var (
	flagJSON  bool
	flagQuiet bool
)

func Execute() {
	f := &rootFlags{}
	root := &cobra.Command{
		Use:   "RedSync [files...] | --video FILE --audio FILE ...",
		Short: "Multi-source MKV sync and Dolby Vision / HDR hybrids",
		Long:  ui.Banner(Version) + helpBody,
		Args:  cobra.ArbitraryArgs,
		PersistentPreRun: func(cmd *cobra.Command, _ []string) {
			// --json implies quiet so scripts get a clean stdout AND stderr.
			ui.SetQuiet(flagQuiet || flagJSON)
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			f.shiftSet = cmd.Flags().Changed("shift")
			return dispatch(cmd.Context(), f, args)
		},
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.CompletionOptions.DisableDefaultCmd = true
	root.SetVersionTemplate("RedSync {{.Version}}\n")
	root.Version = Version

	pf := root.PersistentFlags()
	pf.BoolVar(&flagJSON, "json", false, "emit a machine-readable JSON result on stdout (for scripts)")
	pf.BoolVar(&flagQuiet, "quiet", false, "silence the decorative output; only errors and results")

	fl := root.Flags()
	fl.StringVarP(&f.video, "video", "v", "", "source for the video track / HDR base (the reference)")
	fl.StringVar(&f.dv, "dv", "", "source to take the Dolby Vision layer from (builds a DV+HDR hybrid)")
	fl.StringVar(&f.plus, "hdr10plus", "", "HDR10+ source to graft onto the DV --video base (builds a DV HDR10+ hybrid)")
	fl.StringArrayVarP(&f.audio, "audio", "a", nil, "source(s) to take audio from")
	fl.StringArrayVarP(&f.subs, "subs", "s", nil, "source(s) to take subtitles from")
	fl.StringArrayVar(&f.subsAlt, "subtitles", nil, "alias for --subs")
	fl.StringVarP(&f.chapters, "chapters", "c", "", "source to take chapters from")
	fl.StringVar(&f.chapsAlt, "chaps", "", "alias for --chapters")
	fl.StringVarP(&f.output, "output", "o", "", "output filename (auto-named if omitted)")
	fl.StringVar(&f.outDir, "out-dir", "", "directory to write the result into")
	fl.BoolVar(&f.quick, "sync", false, "quick mode: RedSync a.mkv b.mkv --sync  (a=video, rest=audio+subs+chapters)")
	fl.BoolVar(&f.audioOnly, "audio-only", false, "with --sync, take only audio from the other sources")
	fl.BoolVar(&f.subsOnly, "subs-only", false, "with --sync, take only subtitles")
	fl.BoolVar(&f.chapsOnly, "chapters-only", false, "with --sync, take only chapters")
	fl.BoolVar(&f.keep, "keep", false, "also keep the video source's own audio, subtitles and chapters")
	fl.BoolVar(&f.unique, "unique", false, "keep only unique subtitles per language/role, merging in the ones each source alone has")
	fl.BoolVar(&f.dryRun, "dry-run", false, "print the plan and mkvmerge command without writing anything")
	fl.IntVar(&f.shift, "shift", 0, "set the sync offset manually in ms (skips audio measurement; can be negative)")

	root.AddCommand(analyzeCmd(), hybridCmd(), doctorCmd())

	if err := root.ExecuteContext(context.Background()); err != nil {
		ui.Err(err.Error())
		os.Exit(1)
	}
}

// helpBody is appended under the banner so `-h` reads like a real tool page.
const helpBody = `
Examples:
  RedSync movie/                         pick interactively from a folder
  RedSync a.mkv b.mkv --sync             quick: audio+subs+chapters from b onto a
  RedSync a.mkv b.mkv --sync --subs-only just the subtitles from b
  RedSync a.mkv b.mkv c.mkv --sync --unique   subs from b+c, deduped to one per language/role
  RedSync --video a.mkv --audio b.mkv --subs c.mkv --chapters a.mkv
  RedSync --video hdr.mkv --dv dv.mkv --keep   DV+HDR hybrid, keep original tracks
  RedSync analyze *.mkv                  show tracks, fps, HDR/DV
  RedSync hybrid --hdr hdr.mkv --dv dv.mkv     standalone DV+HDR hybrid mkv

Add --dry-run to preview the plan without writing.
`

// dispatch routes a bare `RedSync ...` invocation to the right action.
func dispatch(ctx context.Context, f *rootFlags, args []string) error {
	f.subs = append(f.subs, f.subsAlt...)
	if f.chapters == "" {
		f.chapters = f.chapsAlt
	}

	explicit := f.video != "" || len(f.audio) > 0 || len(f.subs) > 0 || f.chapters != "" || f.dv != "" || f.plus != ""

	switch {
	case explicit:
		// flag-driven multi-source sync (any combination of sources)
		return runSync(ctx, selection{
			video: f.video, dv: f.dv, plusFrom: f.plus, audio: f.audio, subs: f.subs, chapters: f.chapters,
			keep: f.keep, unique: f.unique, output: f.output, outDir: f.outDir, dryRun: f.dryRun,
			shift: f.shift, shiftSet: f.shiftSet, jsonOut: flagJSON,
		})
	case f.quick && len(args) > 0:
		// quick positional mode: a.mkv b.mkv [c.mkv ...] --sync
		return runQuick(ctx, f, args)
	case len(args) > 0:
		// files but no action => interactive picker
		return runInteractive(ctx, args, f.unique)
	default:
		return errHelp
	}
}

var errHelp = fmt.Errorf("nothing to do. try `RedSync -h`, point at a folder, or use --sync / --video")

// --- analyze ---

func analyzeCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "analyze <files...>",
		Short: "Inspect tracks, languages, fps and HDR/DV in one or more files",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			files, err := probeAll(cmd.Context(), expand(args))
			if err != nil {
				return err
			}
			if flagJSON {
				out := make([]jsonFile, 0, len(files))
				for _, f := range files {
					out = append(out, toJSONFile(f))
				}
				return emitJSON(out)
			}
			for _, f := range files {
				printFile(f)
			}
			return nil
		},
	}
}

func printFile(f media.File) {
	ui.Section(filepath.Base(f.Path))
	if len(f.Video) > 0 {
		v := f.Video[0]
		ui.Field("video", fmt.Sprintf("%s  %dx%d  %s  %s", v.Codec, v.Width, v.Height, fpsString(f.FPS()), ui.Pill(v.HDR.Range())))
	}
	for _, a := range f.Audio {
		extra := fmt.Sprintf("%dch", a.Channels)
		if a.Atmos {
			extra += " Atmos"
		}
		ui.Field("audio", fmt.Sprintf("[%d] %-6s %-4s %s %s", a.Index, a.Codec, a.Language, extra, flagBits(a)))
	}
	for _, s := range f.Subs {
		ui.Field("subtitle", fmt.Sprintf("[%d] %-6s %-4s %s", s.Index, s.Codec, s.Language, flagBits(s)))
	}
	if f.HasChapters {
		ui.Field("chapters", "yes")
	}
}

func flagBits(t media.Track) string {
	var b []string
	if t.Default {
		b = append(b, "default")
	}
	if t.Forced {
		b = append(b, "forced")
	}
	if t.HearImp {
		b = append(b, "SDH")
	}
	if t.VisImp {
		b = append(b, "descriptive")
	}
	if t.Title != "" {
		b = append(b, `"`+t.Title+`"`)
	}
	return ui.Muted.Render(strings.Join(b, " "))
}

func fpsString(f float64) string {
	if f == 0 {
		return ""
	}
	return fmt.Sprintf("%.3f fps", f)
}

// --- shared file gathering ---

var mediaExt = map[string]bool{".mkv": true, ".mp4": true, ".m4v": true, ".mka": true, ".mks": true, ".hevc": true, ".ts": true}

// expand turns args (files or folders) into a flat, sorted list of media files.
func expand(args []string) []string {
	var out []string
	for _, a := range args {
		info, err := os.Stat(a)
		if err != nil {
			continue
		}
		if info.IsDir() {
			entries, _ := os.ReadDir(a)
			for _, e := range entries {
				if !e.IsDir() && mediaExt[strings.ToLower(filepath.Ext(e.Name()))] {
					out = append(out, filepath.Join(a, e.Name()))
				}
			}
		} else {
			out = append(out, a)
		}
	}
	sort.Strings(out)
	return out
}

// probeAll inspects every file concurrently - probing is io-bound so this is a
// real speedup when you point RedSync at a folder.
func probeAll(ctx context.Context, paths []string) ([]media.File, error) {
	if len(paths) == 0 {
		return nil, fmt.Errorf("no media files found")
	}
	out := make([]media.File, len(paths))
	errs := make([]error, len(paths))
	done := make(chan int, len(paths))
	for i, p := range paths {
		go func(i int, p string) {
			out[i], errs[i] = media.Probe(ctx, p)
			done <- i
		}(i, p)
	}
	for range paths {
		<-done
	}
	for i, e := range errs {
		if e != nil {
			return nil, fmt.Errorf("probe %s: %w", paths[i], e)
		}
	}
	return out, nil
}
