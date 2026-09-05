# Sync reliability — 2026-09-06

The normal path is deterministic: unique shared dialogue when available,
programme-wide subtitle activity or multi-window spectral audio matching.
Semantic matching is a fallback, with different candidate/verification samples.
More retries alone do not make weak timing evidence trustworthy.

## New recovery paths

- Text clocks use at least 24 distinctive shared lines, distributed fit/held-out
  anchors and both programme edges. Common FPS changes and arbitrary linear
  drift are measured; partial/repeated/reordered matches fall back safely.
- Audio probes cover every programme quarter. Missing regions get alternate
  positions and two agreeing window sizes. Verified residuals can correct a
  missed cut through composition and re-rendering from original media.
- Same-container subtitles can inherit a verified audio plan, retaining every
  cue. Separate companion files require their own distributed activity evidence.
  A residual correction is limited to 1.5 s intercept and 1000 ppm drift, with
  median residual at most 200 ms. Finished output is checked on staggered windows:
  at least eight, 80% coverage, edge/internal coverage, score >=0.65,
  intercept <=120 ms, drift <=250 ppm, median residual <=200 ms. Ambiguous
  activity that also matches deliberately wrong offsets is rejected.
- UTF-16 BOM input, missing SRT blank separators and out-of-order cues are
  handled; invalid Unicode is rejected. FFT scratch memory is bounded.
- Decode/probe/render children honor cancellation instead of lingering.

`source-timeline-independent-activity` reports actual observed residual metrics;
`plan-integrity` checks exact preservation of an already accepted mapping. These
are different evidence policies, not interchangeable claims of speech recognition.

## Validation

Full `go test ./...` and race tests for subtitle, CLI, sync and tools pass.
Tests cover common FPS ratios both ways, arbitrary drift, offset signs,
held-out corrupt anchors, partial/repeated/reordered dialogue, late edits,
wrong companion clocks, dense ambiguous captions, Unicode errors, child
cancellation and tampered rendered output.

Read-only local media replays included a warped 45-minute English subtitle
(256 text anchors; finished 0 ms/0 ppm, under 0.1 s), a five-minute sped-up
audio clip (32 ms/0 ppm, about 11 s), and the exact recent CC French/German
problem releases. French audio passed strict verification with 22 ms duration
delta; German audio recovered an extra cut automatically. The separate German
subtitle passed 21 activity windows at −4 ms/−33 ppm, median residual 123 ms,
without AI. All 550 French cues survived same-container plan rendering.

Optional local replay: set `REDSYNC_REPLAY_DIR` containing `real-reference.vtt`
and `diagnostic-german-clock.vtt`, then run
`go test ./internal/subtitle -run TestVerifyClockActivityRealCompanion -v`.
Media is not committed or downloaded by tests.

## Limits

No algorithm can reconstruct absent dialogue, establish a reliable map without
shared evidence, or fix arbitrary wrong episodes. The actual German source omits
the reference's next-episode preview; its duration delta is deliberately retained.
Activity matching is not transcription-based word timing. Bitmap OCR and
unmarked legacy encodings remain unsupported here. Passing replays is not a
measured production failure rate or a guarantee of zero manual interventions.
