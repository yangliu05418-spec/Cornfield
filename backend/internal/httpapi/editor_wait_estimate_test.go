package httpapi

import (
	"math"
	"testing"
)

func TestRoundedWaitEstimate(t *testing.T) {
	estimate := roundedWaitEstimate("exact", 10, 73.9, 86.1)
	if estimate.LowerSeconds != 70 || estimate.UpperSeconds != 90 || estimate.SampleSize != 10 || estimate.Basis != "exact" {
		t.Fatalf("estimate = %#v", estimate)
	}
}

func TestRoundedWaitEstimateFallsBackForInvalidSamples(t *testing.T) {
	for _, input := range []struct {
		samples  int
		p50, p90 float64
	}{
		{0, 70, 90},
		{5, math.NaN(), 90},
		{5, 70, math.Inf(1)},
		{5, -1, 90},
	} {
		estimate := roundedWaitEstimate("global", input.samples, input.p50, input.p90)
		if estimate.LowerSeconds != 60 || estimate.UpperSeconds != 100 || estimate.Basis != "fallback" {
			t.Fatalf("fallback = %#v", estimate)
		}
	}
}
