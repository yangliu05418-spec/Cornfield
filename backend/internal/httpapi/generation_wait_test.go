package httpapi

import "testing"

func TestGenerationWaitRequiresSamplesAndAccountsForCapacity(t *testing.T) {
	if got := estimateGenerationWait(10, 2, 4, 2, 60, 100); got.UpperSeconds != 0 {
		t.Fatal("invented estimate without samples")
	}
	if got := estimateGenerationWait(0, 0, 10, 2, 63, 102); got.LowerSeconds != 60 || got.UpperSeconds != 105 {
		t.Fatalf("rounding: %+v", got)
	}
	if got := estimateGenerationWait(4, 2, 10, 2, 60, 100); got.UpperSeconds != 400 {
		t.Fatalf("queue pressure: %+v", got)
	}
}
