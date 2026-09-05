package cli

import (
	"context"
	"fmt"
	"math"

	"github.com/720pixel/RedSync/internal/subtitle"
	"github.com/720pixel/RedSync/internal/timeline"
)

func corroboratedSubtitleSourceClock(reference, target []subtitle.Cue, plan alignmentPlan) (subtitle.Alignment, error) {
	base, err := authoritativeSubtitleAlignmentFromSourceTimeline(reference, target, plan)
	if err != nil {
		return subtitle.Alignment{}, err
	}
	residual, err := subtitle.MeasureClockResidual(reference, subtitle.Apply(target, base))
	if err != nil {
		return subtitle.Alignment{}, err
	}
	base = composeGlobalSubtitleResidual(base, residual)
	if _, err := subtitle.VerifyClockActivity(reference, subtitle.Apply(target, base), 1); err != nil {
		return subtitle.Alignment{}, err
	}
	base.Method = "source_timeline_plan_activity_verified"
	return base, nil
}

func verifyCorroboratedSubtitleClock(ctx context.Context, reference, expected []subtitle.Cue, output string) (*standaloneVerification, error) {
	integrity, err := verifyPlannedSubtitleOutput(ctx, expected, output)
	if err != nil {
		return nil, err
	}
	if !integrity.Passed {
		return nil, fmt.Errorf("source-clock subtitle render failed cue integrity")
	}
	finished, err := subtitle.Read(ctx, output)
	if err != nil {
		return nil, err
	}
	observed, err := subtitle.VerifyClockActivity(reference, finished, 1)
	if err != nil {
		return nil, err
	}
	_, refEnd := subtitleCueBounds(reference)
	_, outEnd := subtitleCueBounds(finished)
	return &standaloneVerification{
		Passed: true, Policy: "source-timeline-independent-activity",
		SyncMS: observed.OffsetMS, Scale: observed.Scale, DriftPPM: (observed.Scale - 1) * 1e6,
		FPSConversion: timingDescription(observed.Scale), Score: observed.Score,
		Samples: observed.Samples, ResidualMS: observed.ResidualMS,
		ReferenceDurationSeconds: refEnd, OutputDurationSeconds: outEnd,
		DurationDeltaMS: int(math.Round((outEnd - refEnd) * 1000)), Gaps: []timeline.Gap{},
	}, nil
}
