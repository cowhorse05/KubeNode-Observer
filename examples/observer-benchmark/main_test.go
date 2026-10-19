package main

import "testing"

func TestPercentileUS(t *testing.T) {
	histogram := make([]uint64, 101)
	histogram[4] = 50
	histogram[8] = 45
	histogram[25] = 5
	if got := percentileUS(histogram, 100, 50); got != 4 {
		t.Fatalf("p50 = %d, want 4", got)
	}
	if got := percentileUS(histogram, 100, 95); got != 8 {
		t.Fatalf("p95 = %d, want 8", got)
	}
	if got := percentileUS(histogram, 100, 99); got != 25 {
		t.Fatalf("p99 = %d, want 25", got)
	}
}
