<div align="center">

<img src="assets/banner.svg" alt="RedSync" width="620">


### One binary. Many sources. Perfectly in sync.

RedSync merges Dolby Vision and HDR10 / HDR10+ into one MKV, syncs audio,
subtitles and chapters taken from different sources, and injects a DV RPU onto an
HDR10 base with the active-area crop worked out automatically. A single command
line tool for Linux and Windows.

![platform](https://img.shields.io/badge/platform-Linux%20%7C%20Windows-2b2b2b?style=for-the-badge)
![go](https://img.shields.io/badge/built%20with-Go-00ADD8?style=for-the-badge&logo=go&logoColor=white)
![dv](https://img.shields.io/badge/Dolby%20Vision-P8.1-FF3B3B?style=for-the-badge)
![license](https://img.shields.io/badge/license-MIT-FF3B3B?style=for-the-badge)

</div>

---

RedSync is a command line tool for building Dolby Vision and HDR hybrid MKV files
and for syncing tracks across releases. It wraps `dovi_tool`, `hdr10plus_tool`,
`ffmpeg` and `mkvmerge` behind one command, measures audio offsets so tracks line
up, and keeps every language tag, title and flag intact.

Use it to:

- Add Dolby Vision to an HDR10 file (inject a DV RPU and produce a DV HDR10 / DV
  HDR10+ hybrid)
- Make a DV + HDR hybrid from a small DV rip and a full-size HDR10 video
- Combine a DV source and a separate HDR10+ source into one DV HDR10+ hybrid
- Convert HDR10+ to Dolby Vision
- Sync audio, subtitles or chapters from one release onto the video of another
- Create standalone synced audio from any two FFmpeg-readable audio sources
- Align one or a whole folder of subtitles to a correctly timed reference
- Remux tracks from several MKV sources into one file with correct timing
- Set the sync offset by hand when you already know it (`--shift`)
- Drive any of the above from a script and read the result back as JSON
  (`--json`)

## Contents

- [Install](#install)
- [Requirements](#requirements)
- [Quick start](#quick-start)
- [How to add Dolby Vision to an HDR10 file](#how-to-add-dolby-vision-to-an-hdr10-file)
- [How to sync audio from a different release](#how-to-sync-audio-from-a-different-release)
- [Standalone audio and subtitle sync](#standalone-audio-and-subtitle-sync)
- [Batch sync](#batch-sync)
- [Scripting RedSync](#scripting-redsync)
- [How the sync stays accurate](#how-the-sync-stays-accurate)
- [How the hybrid crop is automatic](#how-the-hybrid-crop-is-automatic)
- [Build from source](#build-from-source)
- [Credits](#credits)

## Install

Download the binary for your platform from the
[releases page](https://github.com/720pixel/RedSync/releases) and run it. It is
self-contained and needs no admin rights.

**Linux**

```bash
chmod +x RedSync
./RedSync -h
```

**Windows (terminal)**

```powershell
.\RedSync.exe -h
```

Then check your tooling in one step:

```bash
RedSync doctor
```

## Requirements

RedSync calls a few external programs. Run `RedSync doctor` at any time to see
what is present and what is missing.

| Tool | How it is provided |
|------|--------------------|
| `dovi_tool`, `hdr10plus_tool` | Bundled inside the binary. Nothing to install. |
| `ffmpeg`, `ffprobe` | Fetched automatically on first run if not already on your system. |
| `mkvmerge`, `mkvextract`, `mkvpropedit` (MKVToolNix) | You install these. |
| `mediainfo` | You install this. |

Standalone `RedSync sync ...` only needs FFmpeg/FFprobe. MKVToolNix and
MediaInfo are needed for the remux and hybrid features.

Install the two you need:

**Linux (Debian / Ubuntu)**

```bash
sudo apt install mkvtoolnix mediainfo
```

Other distributions: `dnf install mkvtoolnix mediainfo` (Fedora),
`pacman -S mkvtoolnix-cli mediainfo` (Arch).

**Windows**

```powershell
winget install MoritzBunkus.MKVToolNix MediaArea.MediaInfo
```

If a tool is missing, `RedSync doctor` prints the exact command to fix it.

## Quick start

**Pick interactively.** Point at a folder and choose a version:

```bash
RedSync ./my-clips/
```

RedSync numbers each source, lists the versions you can build (with a
recommended best pick based on the highest video tier, best audio and most
subtitles), and lets you compose one with short codes: `v1` video from source 1,
`a2` audio from source 2, `s2` subtitles from source 2, `c1` chapters from
source 1, `dv2` Dolby Vision from source 2.

**Quick two-file sync.** First file is the video, the second hands over its
audio, subtitles and chapters:

```bash
RedSync film_a.mkv film_b.mkv --sync
```

**Write a synced standalone audio file.** The correctly timed reference comes
first and the audio to fix comes second. Input formats can differ:

```bash
RedSync sync english.m4a thai.mka
# writes thai.synced.mka

RedSync sync english.flac commentary.mp3 --format m4a
# writes commentary.synced.m4a
```

**Align subtitles to a reference subtitle.** Text does not need to be in the
same language:

```bash
RedSync sync english.vtt thai.srt --format vtt
# writes thai.synced.vtt
```

Narrow it with `--audio-only`, `--subs-only` or `--chapters-only`.

**Pulling subtitles from more than one source?** Add `--unique` and RedSync
keeps one subtitle track per language/forced/SDH combination instead of
muxing every track from every source. Sources are deduped in the order
they're given, so nothing is lost - if source A has 4 subtitle languages and
source B has 8, and one of source A's languages isn't in source B at all,
that one still makes it into the output instead of being dropped in favor of
source B's larger set:

```bash
RedSync film_a.mkv film_b.mkv film_c.mkv --sync --unique
```

Region matters here too: a French track tagged `fr-CA` and one tagged `fr-FR`
count as different languages, not duplicates. RedSync reads that from
Matroska's BCP-47 language tag (mkvmerge's identify output), since plain
`ffprobe` collapses both down to a bare `fre`.

**Spell out three or more sources:**

```bash
RedSync --video film_a.mkv --audio film_b.mkv --subtitles film_c.mkv --chapters film_a.mkv
```

`--subs` / `--subtitles` and `--chapters` / `--chaps` are interchangeable.

**Inspect a file:**

```bash
RedSync analyze *.mkv
```

Add `--dry-run` to any command to print the plan and the exact `mkvmerge` line
without writing anything, and `--out-dir <folder>` to choose where the result is
written.

On a real hybrid run the layer extractions, the RPU work and the offset
measurement run in parallel, and the large intermediate files are deleted as soon
as they are used, so nothing piles up in the cache.

Every run ends with a timings breakdown - how long probing, offset
measurement, the hybrid build and the final mux each took, plus the total -
so it's obvious where the time actually went.

## How to add Dolby Vision to an HDR10 file

Take the HDR10 video and the Dolby Vision from two sources and build a DV HDR10
hybrid, keeping the HDR10 source's own audio, subtitles and chapters:

```bash
RedSync hybrid --hdr movie_hdr10.mkv --dv movie_dv.mkv
```

The DV source can be a smaller rip (for example 1080p Dolby Vision onto a 2160p
HDR10 base). The RPU is the same metadata at any resolution, and RedSync scales
the active-area crop to the base for you.

Turn an HDR10+ file into DV HDR10+ from a single source:

```bash
RedSync hybrid --hdr10plus movie_hdr10plus.mkv
```

**Combine a DV source and a separate HDR10+ source.** If you have one file with
Dolby Vision and a different file with HDR10+, RedSync can graft the HDR10+
dynamic metadata onto the DV video to make a DV HDR10+ hybrid:

```bash
RedSync hybrid --dv movie_dv.mkv --hdr10plus movie_hdr10plus.mkv
```

This keeps the **DV file's** video and borrows only the HDR10+ metadata, so the
DV RPU (and its active-area crop) is preserved untouched. It needs a DV source
that already carries an HDR10 base layer - profile 8.1 (or 7). A profile 5
source (typical of iTunes rips) is single-layer DV with no HDR10 base, so its
pixels can't stand in for HDR10; RedSync detects that and points you at the
other direction instead, which keeps the HDR10+ video and grafts the DV onto it:

```bash
RedSync hybrid --hdr movie_hdr10plus.mkv --dv movie_dv.mkv
```

Both directions produce a DV HDR10+ file - they differ only in which source's
video pixels survive.

Want only the elementary stream to mux yourself? Add `--hevc-only`.

## How to sync audio from a different release

```bash
RedSync --video keep_this_video.mkv --audio other_release.mkv
```

RedSync measures the real offset from the audio and applies it. If the two
releases run at different frame rates it corrects the drift with a linear factor
instead of a single delay that slips over time. Subtitles and chapters sync the
same way.

**Already know the offset?** Skip the measurement and set it yourself with
`--shift` (milliseconds, negative allowed). The value is applied as a constant
delay to every source being synced onto the video:

```bash
RedSync a.mkv b.mkv --sync --shift -320
```

## Standalone audio and subtitle sync

The `sync` subcommand does not need a video track or MKVToolNix:

```bash
RedSync sync <correct-reference> <target-to-fix> [more-targets...]
```

For audio, RedSync decodes spectral probes from the beginning and across the
runtime. It robustly fits the map from target timestamps to reference
timestamps, separating a fixed offset from linear drift. Dense anchors also
detect persistent internal discontinuities: reference-only sections become
silence of the exact measured duration, while target-only sections are cut.
An isolated weak probe is rejected instead of being treated as an edit. Speed
candidates include the common 23.976/24/25/29.97/30 fps conversions and a
factor inferred from file durations. FFmpeg's `atempo` corrects speed without
changing pitch; padding or trimming makes the result exactly the reference
duration.

The output container follows the target by default. Use `--format mka`,
`--format m4a`, `--format mp3`, `--format flac` or another FFmpeg-writable audio
extension to convert while syncing. RedSync keeps the channel layout, language
tag and source bitrate where the selected encoder supports them. Use `--codec`
to choose an FFmpeg encoder explicitly. Integration pipelines can also set
`--bitrate 96k`, `--channels 2`, `--sample-rate 48000`, and `--language tha`.

For subtitles, RedSync turns both cue timelines into language-independent
activity signals, searches offsets and speed ratios with FFT correlation, then
refines the result against exact cue intervals. SRT and WebVTT are handled
natively; other FFmpeg-readable text subtitle formats are normalized through
FFmpeg. `--format vtt` and `--format srt` provide predictable interchange
outputs.

### Extreme cross-language subtitle alignment

When audio is unavailable and English and translated subtitle activity differs
too much, `--semantic-codex-model` enables the exceptional cross-language path.
It reuses the local Codex CLI login and launches one short-lived, ephemeral,
read-only process from the system temporary directory. CinemaCity uses the
`gpt-5.4-mini` model with low reasoning effort. In the production Hindi SDH
23.976/25 FPS fixture it produced more than twice as many clean anchors as
Spark, while the extra latency remains isolated to this rare fallback:

```bash
RedSync sync english.vtt tamil.srt \
  --semantic-codex-model gpt-5.4-mini \
  --format vtt --json --events-json
```

RedSync samples distinctive English dialogue across the complete programme and
sends only those sparse entries plus nearby one/two-cue target candidates. The
prompt asks Codex to identify unambiguous translated meaning while ignoring
target-only SDH sounds, speaker labels, repeated short lines and timestamp
differences. It does not ask AI to translate the full subtitle or calculate
timings.

After the sparse matches return, deterministic code extracts the strongest
monotonic chain, calculates exact fixed delay and arbitrary linear clock/FPS
drift (including 23.976/24/25), detects supported internal edits, removes timing
outliers, renders the file and remeasures the output. Verification reuses the
same semantic matches, so each target normally makes one Codex call. Live
`--events-json` output explicitly announces both the AI start (including model)
and the handoff back to deterministic timing.

`--semantic-window` limits candidate timing hypotheses,
`--semantic-codex-timeout` bounds the subprocess, and `--semantic-codex-bin`
selects the CLI executable. Without `--semantic-codex-model`, Codex is never
invoked and the built-in cue-activity matcher is unchanged. This mode is meant
for private, rare recovery jobs; normal CinemaCity jobs use audio or a target
English subtitle as the verified anchor and replay that exact timing plan for
all sibling languages.

Useful controls:

```bash
RedSync sync ref.m4a target.mka --shift -5000 --factor 1.0
RedSync sync ref.vtt target.srt --max-offset 600 --dry-run
RedSync sync ref.mkv target.mka --reference-track 1 --target-track 0
RedSync sync ref.m4a target.flac --format m4a --codec aac --bitrate 96k --channels 2 --sample-rate 48000 --language tha
RedSync sync english.vtt telugu.srt --semantic-codex-model gpt-5.4-mini --dry-run
```

`--shift` is the correction applied to the target: a negative value advances
it. `--factor` multiplies target timestamps (less than 1 speeds a long target
up; greater than 1 slows a short target down). Supplying either option bypasses
automatic measurement.

Internal edit repair is enabled by default. `--min-gap 0.35` controls the
smallest discontinuity that can become a segment boundary, and
`--max-segments 8` is the safety cap. Use `--detect-gaps=false` when a known
workflow explicitly requires one affine map. Finished files are re-measured by
default; `--verify=false` is available for callers that perform their own
independent verification.

## Batch sync

Pass several targets, shell globs, or directories. Directories are scanned
recursively and existing `*.synced.*` files are skipped:

```bash
RedSync sync english.vtt subtitles/ --format vtt --out-dir synced-subs/
RedSync sync english.m4a thai.mka japanese.flac spanish.mp3 --out-dir synced-audio/
```

Every target is measured independently. This matters when a folder mixes
releases with different offsets or frame-rate conversions. `--output` is only
valid for one target; use `--out-dir` for a batch. Existing files are protected
unless `--overwrite` is given. Each result clearly reports `sync (ms)`, the
timestamp scale, detected FPS conversion (or arbitrary drift in ppm), confidence
and output path. JSON schema version 2 preserves `sync_ms`, `scale`,
`drift_ppm` and `fps_conversion`, and adds `language`, `segments`, `gaps`, and
`verification`. Each gap has an `action` of `insert_silence` or
`remove_target`, its signed `delta_ms`, absolute `duration_ms`, and target and
reference boundary timestamps. Verification reports the remaining offset,
scale/drift, confidence, residual, reference/output duration and delta,
remaining gaps, and a `passed` gate.

### Reuse one verified source timeline

Tracks extracted from the same release share one timeline even when their
languages differ. Measure a reliable anchor—normally that source's English
track—once, then export its exact verified mapping:

```bash
RedSync sync cc-english.m4a source-english.mka \
  --write-alignment-plan source-s01e01.timeline.json --json
```

The plan is written only after normal output verification passes. It contains
schema version 1, the affine shift/scale, every piecewise segment and internal
gap, confidence metadata, the verification gate, source/reference durations,
and a SHA-256 digest of the reference. It stores basenames rather than absolute
paths.

Apply that exact mapping to sibling tracks from the same source release:

```bash
RedSync sync cc-english.m4a source-hindi.mka \
  --alignment-plan source-s01e01.timeline.json
RedSync sync cc-english.m4a source-tamil.mka source-telugu.mka \
  --alignment-plan source-s01e01.timeline.json --out-dir synced/
```

When a source has internal black intervals, subtitle cue activity alone can
misidentify those edits. Reuse the already verified audio timeline for the
source's English subtitle anchor, then export a normal subtitle plan for its
translated siblings:

```bash
RedSync sync cc-english-sdh.vtt source-english-sdh.vtt \
  --source-timeline-plan source-s01e01.timeline.json \
  --write-alignment-plan source-s01e01.subtitles.json
```

`--source-timeline-plan` accepts only a verified audio plan. RedSync applies
its piecewise source edits, fits only one bounded subtitle residual offset,
and independently verifies the rendered subtitle against the exact subtitle
reference before writing the subtitle plan. If that reference is translated,
`--semantic-codex-model` may be combined with the source timeline. RedSync
compares the semantic residual with deterministic cue activity and uses only
the tighter safe fit; semantic evidence cannot replace or alter the verified
audio edit/gap topology.

Callers that have independently proved the subtitle is embedded in the same
source container as the verified audio anchor can add
`--source-timeline-authoritative`. RedSync then applies the verified piecewise
audio timeline directly, requires every cue to remain inside the source and
reference coverage, and verifies every rendered timestamp and text line
exactly. Sparse or translated subtitle references cannot veto that stronger
same-container clock evidence; without this explicit flag, residual fitting
and independent subtitle-reference verification remain unchanged.

Plan reuse skips measurement but not rendering or final verification. Before
rendering, RedSync rejects unknown schema fields, an unverified plan, media-type
mismatch, a different reference digest/duration, unsafe scale, overlapping or
non-monotonic segments, inconsistent reference bounds, malformed gaps, and—for
audio—a sibling duration inconsistent with the anchor source. Subtitle sibling
durations are not required to match because languages commonly omit signs,
songs, or end credits. `--alignment-plan` cannot be combined with manual
shift/factor, semantic measurement, or plan export. Existing behavior is
unchanged when neither plan flag is present.

## Scripting RedSync

Every command is non-interactive when you pass explicit flags, and `--json`
turns the result into a single machine-readable object on stdout while all the
decorative output stays on stderr - so a wrapping script can parse stdout
cleanly.

```bash
RedSync analyze --json *.mkv                       # tracks, fps, HDR/DV per file
RedSync doctor --json                              # tool availability + paths
RedSync hybrid --dv dv.mkv --hdr10plus hdr.mkv --json
RedSync a.mkv b.mkv --sync --json                  # output path, offsets, timings
RedSync sync english.m4a thai.mka --json            # standalone audio result
RedSync sync english.vtt subtitles/ --format vtt --json  # subtitle batch results
```

Long-running standalone sync callers can add `--events-json`. RedSync then
keeps the final `--json` result unchanged on stdout and writes one compact JSON
event per line to stderr, prefixed with `[redsync-event] `. The event sequence
for each target is `target_started`, `measuring_started`,
`measuring_complete`, `rendering_started`, `rendering_complete`, optional
`verification_started` and `verification_complete`, then `target_complete`.
Measurement and completion events include the detected offset, scale,
confidence, samples, residual and edit counts; verification includes the
`passed` gate. Every event has stable `target`, `current`, `total`, and
human-readable `message` fields (with `index` retained as an alias for
`current`).
Only basenames are included, so live logs do not expose source directory paths.

```bash
RedSync sync english.m4a tamil.mka --json --events-json
```

A run's JSON reports the output filename, the final dynamic-range tag, the delay
(and any frame-rate stretch) measured for each source, the exact `mkvmerge`
command, and per-stage timings. Add `--dry-run` to get all of that without
writing anything, or `--quiet` to keep the pretty output but drop the spinners.

## How the sync stays accurate

The offset between two tracks comes from their audio. RedSync decodes a short
window from each, builds a log-mel energy envelope, and cross-correlates them
with an FFT. Standalone sync samples up to 25 positions and uses a robust
piecewise fit, so one weak scene does not decide the result and genuine edits
remain distinct from clock drift. The peak is the delay, and how sharp that
peak is says how far to trust it.

It checks two points in the runtime. The same delay at both means a constant
offset that goes straight into `mkvmerge --sync`. Different delays mean the
sources run at different speeds, so RedSync applies a linear factor from the
exact frame-rate ratio and the alignment holds from first frame to last.

## How the hybrid crop is automatic

A Dolby Vision display reads the RPU active area to know where the real picture
sits. When you put DV metadata from a cropped source onto a letterboxed base, the
active area has to describe those black bars, or the display tone-maps the black.

RedSync fits the DV picture inside the base frame, takes the leftover as the
bars, splits it evenly, and writes that into the RPU. A 2160p base over a 1608
picture gives `(2160 - 1608) / 2 = 276` pixels top and bottom, from the geometry
alone. Per-frame active areas in the source RPU are carried through and scaled,
so variable-aspect titles keep their changes.

## Build from source

You need Go 1.24 or newer.

**Install Go on Linux**

```bash
wget https://go.dev/dl/go1.24.0.linux-amd64.tar.gz
sudo rm -rf /usr/local/go && sudo tar -C /usr/local -xzf go1.24.0.linux-amd64.tar.gz
echo 'export PATH=$PATH:/usr/local/go/bin' >> ~/.profile && source ~/.profile
go version
```

**Install Go on Windows (terminal)**

```powershell
winget install GoLang.Go
go version
```

**Build**

```bash
git clone https://github.com/720pixel/RedSync
cd RedSync
make build        # native binary
make release      # linux + windows into dist/
```

## Credits

Synchronization recovery, verification policies and replay evidence are documented
in [Sync reliability](docs/sync-reliability.md).

RedSync would not exist without these projects:

- **[dovi_tool](https://github.com/quietvoid/dovi_tool)** by quietvoid - all
  Dolby Vision RPU work (extract, edit, inject, generate). Bundled in the binary.
- **[hdr10plus_tool](https://github.com/quietvoid/hdr10plus_tool)** by quietvoid -
  HDR10+ metadata extraction. Bundled in the binary.
- **[audio-offset-finder](https://github.com/bbc/audio-offset-finder)** by the
  BBC - the audio cross-correlation approach RedSync's offset engine is based on.
- **[ffsubsync](https://github.com/smacke/ffsubsync)** by Stephen Macke - its
  documented language-independent subtitle activity approach informed RedSync's
  original Go subtitle matcher. No ffsubsync source code is included.

Also relies on [FFmpeg](https://ffmpeg.org),
[MKVToolNix](https://mkvtoolnix.download) and
[MediaInfo](https://mediaarea.net/MediaInfo).

## License

MIT, see [LICENSE](LICENSE). Bundled tools keep their own licenses, listed in
[NOTICE](NOTICE).

<div align="center">

**RedSync** · Dolby Vision and HDR hybrid muxing, audio and subtitle sync, for Linux and Windows

</div>
