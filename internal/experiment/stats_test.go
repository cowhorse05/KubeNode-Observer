package experiment

import (
	"testing"
	"time"
)

func TestPairedEffectResamplesPairs(t *testing.T) {
	got := pairedEffect([]float64{-4, -2, 2, 4})
	if got.MedianPercent != 0 || got.Pairs != 4 || got.BootstrapLowerPercent < -4 || got.BootstrapUpperPercent > 4 {
		t.Fatalf("effect: %+v", got)
	}
	if again := pairedEffect([]float64{-4, -2, 2, 4}); got != again {
		t.Fatal("bootstrap is not reproducible")
	}
}

func TestConfigRejectsUnbalancedPairsAndDuplicateArtifacts(t *testing.T) {
	config := Config{Mode: "all", Context: "test", Pairs: 4, Repeats: 2, Duration: 10 * time.Second, Warmup: time.Second, Settle: time.Second, Intervals: "250ms,1s,2s"}
	if _, err := validate(config); err != nil {
		t.Fatal(err)
	}
	odd := config
	odd.Pairs = 3
	if _, err := validate(odd); err == nil {
		t.Fatal("unbalanced AB/BA accepted")
	}
	duplicate := config
	duplicate.Intervals = "1s,1000ms"
	if _, err := validate(duplicate); err == nil {
		t.Fatal("duplicate interval would overwrite raw artifacts")
	}
}
