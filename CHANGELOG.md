# Changelog

All notable changes to RedSync are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and the project uses
[Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [0.2.0] - 2026-09-01

### Added

- `RedSync sync <reference> <target...>` for standalone audio-to-audio and
  subtitle-to-subtitle synchronization without a video track or MKVToolNix.
- Batch targets and recursive directory scanning, format conversion, manual
  shift/factor overrides, audio stream selection, confidence gates, dry runs
  and JSON reports.
- Multi-probe robust drift fitting for audio and language-independent FFT cue
  activity alignment for SRT, WebVTT and FFmpeg-readable text subtitles.
- Piecewise internal-edit detection and repair for audio and subtitles, with
  explicit silence-insertion/target-removal gap records and final remeasurement.
- Stable standalone JSON schema version 2 with segment, gap, language,
  confidence/residual, and verification fields.
- Optional audio bitrate, channel-count, sample-rate, codec and language output
  controls for non-interactive integration pipelines.

### Fixed

- Corrected the direction of the exact video FPS timestamp ratio and inferred
  audio drift factor.
- Retry low-confidence head matches at known film/TV clock ratios, so a valid
  25 fps dub is not rejected before 23.976 fps conversion is evaluated.
- Refine multi-edit black-gap boundaries across the original anchor bracket,
  avoiding misplaced cuts when several discontinuities occur in one episode.

## [0.1.0] - 2026-07-02

Initial release.

### Hybrids

- Inject a Dolby Vision RPU onto an HDR10 / HDR10+ base and produce a DV HDR10 /
  DV HDR10+ hybrid (`hybrid --hdr H --dv D`). The DV source can be a smaller rip;
  the active-area crop is worked out from the layer geometry and the RPU, so
  letterboxed bars are described correctly and per-frame (IMAX) aspect changes
  are carried through.
- Build a DV HDR10+ hybrid from a Dolby Vision file and a separate HDR10+ file:
  keep the DV video and graft the HDR10+ metadata onto it (`hybrid --dv D
  --hdr10plus S`). Profile 5 DV sources have no HDR10 base layer and are refused
  with guidance toward the other build direction.
- Convert an HDR10+ source into DV HDR10+ from its own dynamic metadata
  (`hybrid --hdr10plus S`).
- `--hevc-only` writes just the elementary stream when you want to mux yourself.

### Sync

- Measure the real audio offset between two releases and apply it, correcting
  frame-rate drift with a linear factor when the clocks differ.
- Pull audio, subtitles and chapters from one or more sources onto the video of
  another, keeping every language tag, title and disposition flag intact.
- `--unique` keeps one subtitle track per language/role while merging in the
  one-of-a-kind tracks each source alone has.
- `--shift <ms>` sets the offset by hand and skips the audio measurement.

### Interface

- Interactive picker: point at a folder, see the versions you can build, and
  compose one with short codes (`v1 dv2 a2 s2 c1`).
- `analyze` inspects tracks, frame rate and HDR/DV in one or more files.
- `--json` emits a machine-readable result on stdout for `analyze`, `doctor` and
  every run, so RedSync drops into a script cleanly; `--quiet` drops the
  decorative output.
- Per-stage timings printed at the end of every run.

### Notes

- The multi-GB HEVC layers are streamed from ffmpeg into `dovi_tool` /
  `hdr10plus_tool` when only metadata is needed, so they never stage on disk, and
  the RPU active-area edit is skipped when the layers already share geometry.
- `dovi_tool` and `hdr10plus_tool` ship inside the binary; ffmpeg is fetched on
  first run if it isn't already present.

[0.1.0]: https://github.com/720pixel/RedSync/releases/tag/v0.1.0
[0.2.0]: https://github.com/720pixel/RedSync/compare/v0.1.0...v0.2.0
