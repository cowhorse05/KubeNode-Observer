package experiment

import (
	"testing"
	"time"
)

func TestMetricsParserAndRanks(t *testing.T) {
	result, err := parseMetrics([]byte("# comment\nkno_agent_metrics_collection_errors 1\nkno_agent_events_lost_total 2\nkno_agent_bpf_map_stats_ready{source=\"cpu\"} 1\nkno_agent_bpf_map_stats_ready{source=\"runqlat\"} 0\nkno_agent_bpf_map_stats_age_seconds{source=\"cpu\"} 17.5\nkno_agent_bpf_map_stats_refresh_errors_total{source=\"cpu\"} 3\n"))
	if err != nil || result.Errors != 1 || result.EventsLost != 2 || result.CacheSources != 2 || result.ReadySources != 1 || result.MaxAgeSeconds != 17.5 || result.RefreshErrors != 3 {
		t.Fatalf("parse: %+v %v", result, err)
	}
	for _, bad := range []string{"", "kno_agent_metrics_collection_errors NaN", "kno_agent_metrics_collection_errors +Inf"} {
		if _, err := parseMetrics([]byte(bad)); err == nil {
			t.Fatal("bad metrics accepted")
		}
	}
	if quantile([]float64{1, 2, 3, 4}, 0.95) != 4 || quantile([]float64{1, 2, 3, 4}, 0.5) != 2 {
		t.Fatal("incorrect nearest rank")
	}
}

func TestImageOverrideAndMetricsConfig(t *testing.T) {
	if _, err := imageManifest([]byte(`{}`), "image"); err == nil {
		t.Fatal("malformed manifest accepted")
	}
	config := Config{Mode: "metrics", Context: "test", BaselineImage: "old:dev", CandidateImage: "new:dev", Pairs: 4, Repeats: 1, Duration: 30 * time.Second, Warmup: time.Second, Settle: time.Second, Intervals: "1s"}
	if _, err := validate(config); err != nil {
		t.Fatal(err)
	}
	config.CandidateImage = config.BaselineImage
	if _, err := validate(config); err == nil {
		t.Fatal("identical images accepted")
	}
}
