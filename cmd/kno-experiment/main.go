package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/cowhorse05/KubeNode-Observer/internal/experiment"
)

func main() {
	var config experiment.Config
	flag.StringVar(&config.Mode, "mode", "all", "paired, accuracy, all (paired+accuracy), cost, or metrics")
	flag.StringVar(&config.Context, "context", "kno-dev", "explicit single-node test cluster context")
	flag.StringVar(&config.Output, "output", "", "new output directory (default log/experiments/<UTC timestamp>)")
	flag.StringVar(&config.Image, "image", "kubenode-observer/observer-benchmark:dev", "preloaded workload image")
	flag.StringVar(&config.AgentImage, "cost-agent-image", "kubenode-observer/kno-agent:cost", "separate preloaded diagnostic Agent image (cost mode only)")
	flag.StringVar(&config.BaselineImage, "baseline-agent-image", "kubenode-observer/kno-agent:cost", "preloaded baseline image (metrics mode)")
	flag.StringVar(&config.CandidateImage, "candidate-agent-image", "kubenode-observer/kno-agent:metrics-cache", "preloaded candidate image (metrics mode)")
	flag.StringVar(&config.TestAgentImage, "test-agent-image", "", "optional preloaded image override for paired/accuracy/all; original deployment is restored")
	flag.IntVar(&config.Pairs, "pairs", 4, "even number of paired rounds, alternating off/on and on/off")
	flag.IntVar(&config.Repeats, "accuracy-repeats", 2, "repeats per workload and accuracy interval")
	flag.StringVar(&config.Intervals, "intervals", "250ms,1s,2s", "accuracy map scan intervals")
	flag.DurationVar(&config.Duration, "duration", 10*time.Second, "measurement window per case")
	flag.DurationVar(&config.Warmup, "warmup", 3*time.Second, "workload warmup before measurement")
	flag.DurationVar(&config.Settle, "settle", 5*time.Second, "agent settling time before each on case")
	flag.Parse()
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	if err := experiment.Run(ctx, config); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
