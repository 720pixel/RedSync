package offset

import (
	"math"
	"testing"
)

func TestResampleFrames(t *testing.T) {
	in := [][]float64{{0, 10}, {2, 12}, {4, 14}}
	got := resampleFrames(in, 2)
	if len(got) != 6 {
		t.Fatalf("len = %d, want 6", len(got))
	}
	if math.Abs(got[1][0]-1) > 1e-12 || math.Abs(got[1][1]-11) > 1e-12 {
		t.Fatalf("interpolated row = %v, want [1 11]", got[1])
	}
}
