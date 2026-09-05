# RedSync grepai context snapshot

Update this snapshot when alignment architecture or CLI contracts change.

## Reliability additions (2026-09-06)

- `subtitle/text_align.go`: fast affine fitting from distributed unique dialogue,
  checked against held-out anchors; supports FPS ratios and arbitrary drift.
- `subtitle/clock_verify.go` and `cli/source_clock.go`: bounded residual fitting
  for separate subtitle companions, followed by staggered activity-window and
  exact render-integrity checks. New schema-v2 verification policy is
  `source-timeline-independent-activity`; consumers must enforce metric bounds.
- Audio measurement automatically reprobes missing programme quarters; two
  alternate window sizes must agree. Finished audio can trigger one bounded
  residual composition/re-render and independent verification.
- Media probing, decoding, conversion and audio rendering honor cancellation.
  Subtitle parsing supports BOM UTF-16, missing SRT separators and stable ordering;
  FFT scratch allocation is capped for extreme timestamp spans.
- `docs/sync-reliability.md` records test/replay evidence and explicit limits.

## Entry points

- `main.go` starts the Cobra CLI.
- `internal/cli/standalone.go` implements `RedSync sync` and `RedSync analyze`.
- `internal/subtitle/align.go` implements deterministic subtitle activity
  alignment and timeline rendering.
- `internal/subtitle/semantic_codex.go` implements the production extreme-case
  sparse Codex matcher and deterministic cross-language timing fit.
- `internal/subtitle/semantic.go` retains the model-agnostic semantic fitting
  primitives and deterministic timing helpers.

## CinemaCity integration

- `--events-json` emits prefixed one-line progress events on stderr so CDNMV
  can persist download/sync/verification/render phase progress without parsing
  decorative terminal output.
- `--write-alignment-plan` exports a plan only after rendering and verification.
  The versioned plan contains reference SHA-256/duration, anchor duration,
  shift, scale, piecewise segments, gaps, samples, confidence, and residual.
- `--alignment-plan` replays that exact verified mapping for sibling language
  tracks. It validates reference identity, durations, monotonic segments,
  affine bounds, and gaps before rendering. This is the normal multilingual
  CinemaCity path: measure target English once, then make Hindi/Tamil/Telugu
  and other siblings follow English exactly.
- `--source-timeline-plan` handles mixed audio/subtitle sources with internal
  black intervals. It applies a verified audio plan to the same source's
  English subtitle anchor, fits one bounded residual subtitle offset, performs
  normal exact-reference subtitle verification, and can export the resulting
  subtitle plan for translated siblings. When the exact reference is
  translated, semantic matching is allowed only for that residual; its result
  is compared with deterministic cue activity and the tighter safe fit wins.
- `--source-timeline-authoritative` is the explicit same-container fallback.
  It replays the already verified audio segments/gaps without fitting a
  translated subtitle residual, bounds every cue to the verified source and
  reference coverage, and uses exact plan-integrity verification. Callers must
  prove the audio and subtitle share one source container before enabling it.

## Cross-language fallback

- Semantic matching is used when no target-English subtitle reference exists,
  either with a verified source-audio timeline or in the extreme subtitle-only
  case. `CodexAnchorMatcher` samples distinctive reference dialogue and
  nearby one/two-cue target candidates, then asks a short-lived Codex CLI
  process for sparse meaning-equivalent pairs. It never asks AI to calculate
  timings or translate the complete subtitle.
- Production uses subscription-authenticated `gpt-5.4-mini` at low
  effort through an ephemeral, read-only process in the system temporary
  directory. `semantic_ai_started` and `semantic_ai_complete` events make AI
  use explicit in CDNMV's live log. Output verification uses a different sample
  variant rather than certifying a candidate with its own cached anchors.
- Semantic evidence is combined with programme-wide coverage, timing
  consistency, residual cleanup, FPS/linear drift fitting, and internal-edit
  detection. A different or ambiguous programme is not turned into a
  plausible-looking timeline.

## Useful grepai queries

- `grepai search "verified alignment plan sibling languages" --limit 5`
- `grepai search "cross language subtitle semantic monotonic anchors" --limit 5`
- `grepai search "structured sync progress events" --limit 5`
- `grepai trace callers "AlignCodexSemantic" --mode precise`
- `grepai trace callers "readAlignmentPlan" --mode precise`
