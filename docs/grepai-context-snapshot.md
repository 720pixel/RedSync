# RedSync grepai context snapshot

Update this snapshot when alignment architecture or CLI contracts change.

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
  use explicit in CDNMV's live log. The cached matches are reused for output
  verification, avoiding a second model call.
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
