package experiment

import (
	"math/rand"
	"sort"
)

type Effect struct {
	MedianPercent         float64 `json:"medianPercent"`
	BootstrapLowerPercent float64 `json:"bootstrapLowerPercent"`
	BootstrapUpperPercent float64 `json:"bootstrapUpperPercent"`
	Pairs                 int     `json:"pairs"`
}

func median(values []float64) float64 {
	copy := append([]float64(nil), values...)
	sort.Float64s(copy)
	if len(copy)%2 == 1 {
		return copy[len(copy)/2]
	}
	return (copy[len(copy)/2-1] + copy[len(copy)/2]) / 2
}

// pairedEffect bootstraps complete pairs, never independent on/off observations.
// A fixed seed makes report generation deterministic. Small-N CIs remain exploratory.
func pairedEffect(deltas []float64) Effect {
	result := Effect{Pairs: len(deltas), MedianPercent: median(deltas)}
	rng := rand.New(rand.NewSource(20261020))
	bootstrap := make([]float64, 10000)
	sample := make([]float64, len(deltas))
	for i := range bootstrap {
		for j := range sample {
			sample[j] = deltas[rng.Intn(len(deltas))]
		}
		bootstrap[i] = median(sample)
	}
	sort.Float64s(bootstrap)
	result.BootstrapLowerPercent = bootstrap[249]
	result.BootstrapUpperPercent = bootstrap[9749]
	return result
}
