package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"time"

	"github.com/spf13/cobra"
)

func main() {
	var endpoint string

	root := &cobra.Command{
		Use:   "kno",
		Short: "KubeNode-Observer command line client",
	}
	root.PersistentFlags().StringVar(&endpoint, "endpoint", "http://127.0.0.1:18080", "kno-agent HTTP endpoint")

	health := &cobra.Command{
		Use:   "health",
		Short: "Check kno-agent health",
		RunE: func(_ *cobra.Command, _ []string) error {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()

			request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint+"/healthz", nil)
			if err != nil {
				return err
			}
			response, err := http.DefaultClient.Do(request)
			if err != nil {
				return err
			}
			defer response.Body.Close()

			body, err := io.ReadAll(response.Body)
			if err != nil {
				return err
			}
			if response.StatusCode != http.StatusOK {
				return fmt.Errorf("agent returned %s: %s", response.Status, body)
			}
			fmt.Print(string(body))
			return nil
		},
	}
	root.AddCommand(health)

	var namespace string
	var pod string
	topology := &cobra.Command{
		Use:   "topology",
		Short: "Show the node container and process topology",
		RunE: func(_ *cobra.Command, _ []string) error {
			query := url.Values{}
			if namespace != "" {
				query.Set("namespace", namespace)
			}
			if pod != "" {
				query.Set("pod", pod)
			}
			path := endpoint + "/api/v1/topology"
			if encoded := query.Encode(); encoded != "" {
				path += "?" + encoded
			}

			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			request, err := http.NewRequestWithContext(ctx, http.MethodGet, path, nil)
			if err != nil {
				return err
			}
			response, err := http.DefaultClient.Do(request)
			if err != nil {
				return err
			}
			defer response.Body.Close()
			body, err := io.ReadAll(response.Body)
			if err != nil {
				return err
			}
			if response.StatusCode != http.StatusOK {
				return fmt.Errorf("agent returned %s: %s", response.Status, body)
			}
			var formatted bytes.Buffer
			if err := json.Indent(&formatted, body, "", "  "); err != nil {
				return fmt.Errorf("decode topology response: %w", err)
			}
			fmt.Println(formatted.String())
			return nil
		},
	}
	topology.Flags().StringVarP(&namespace, "namespace", "n", "", "filter by namespace")
	topology.Flags().StringVar(&pod, "pod", "", "filter by pod name")
	root.AddCommand(topology)
	var sandboxID string
	kata := &cobra.Command{Use: "kata", Short: "Inspect Kata through the node's official Kata Monitor"}
	kataMetrics := &cobra.Command{
		Use: "metrics", Short: "Read Kata Monitor metrics for a local sandbox (not Guest Probe data)",
		RunE: func(_ *cobra.Command, _ []string) error {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint+"/api/v1/kata/metrics?"+url.Values{"sandbox": {sandboxID}}.Encode(), nil)
			if err != nil {
				return err
			}
			response, err := http.DefaultClient.Do(request)
			if err != nil {
				return err
			}
			defer response.Body.Close()
			body, err := io.ReadAll(io.LimitReader(response.Body, (8<<20)+1))
			if err != nil {
				return err
			}
			if response.StatusCode != http.StatusOK {
				return fmt.Errorf("agent returned %s: %s", response.Status, body)
			}
			if len(body) > 8<<20 {
				return fmt.Errorf("metrics exceed 8 MiB")
			}
			fmt.Print(string(body))
			return nil
		},
	}
	kataMetrics.Flags().StringVar(&sandboxID, "sandbox", "", "full sandbox ID from kno topology")
	_ = kataMetrics.MarkFlagRequired("sandbox")
	kata.AddCommand(kataMetrics)
	root.AddCommand(kata)

	var eventNamespace string
	var eventPod string
	var eventType string
	var eventLimit int
	events := &cobra.Command{
		Use:   "events",
		Short: "Show recent process lifecycle events",
		RunE: func(_ *cobra.Command, _ []string) error {
			query := url.Values{}
			if eventNamespace != "" {
				query.Set("namespace", eventNamespace)
			}
			if eventPod != "" {
				query.Set("pod", eventPod)
			}
			if eventType != "" {
				query.Set("type", eventType)
			}
			query.Set("limit", strconv.Itoa(eventLimit))

			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint+"/api/v1/events?"+query.Encode(), nil)
			if err != nil {
				return err
			}
			response, err := http.DefaultClient.Do(request)
			if err != nil {
				return err
			}
			defer response.Body.Close()
			body, err := io.ReadAll(response.Body)
			if err != nil {
				return err
			}
			if response.StatusCode != http.StatusOK {
				return fmt.Errorf("agent returned %s: %s", response.Status, body)
			}
			var formatted bytes.Buffer
			if err := json.Indent(&formatted, body, "", "  "); err != nil {
				return fmt.Errorf("decode events response: %w", err)
			}
			fmt.Println(formatted.String())
			return nil
		},
	}
	events.Flags().StringVarP(&eventNamespace, "namespace", "n", "", "filter by namespace")
	events.Flags().StringVar(&eventPod, "pod", "", "filter by pod name")
	events.Flags().StringVar(&eventType, "type", "", "filter by event type: exec or exit")
	events.Flags().IntVar(&eventLimit, "limit", 100, "maximum number of events")
	root.AddCommand(events)

	var topNamespace string
	var topPod string
	var topLimit int
	top := &cobra.Command{
		Use:   "top",
		Short: "Show CPU hotspots from the latest sampling window",
	}
	top.PersistentFlags().StringVarP(&topNamespace, "namespace", "n", "", "filter by namespace")
	top.PersistentFlags().StringVarP(&topPod, "pod", "p", "", "filter by pod name")
	top.PersistentFlags().IntVar(&topLimit, "limit", 20, "maximum number of results")
	for _, definition := range []struct {
		use   string
		level string
		short string
	}{
		{use: "pods", level: "pod", short: "Show hottest Pods"},
		{use: "containers", level: "container", short: "Show hottest containers"},
		{use: "processes", level: "process", short: "Show hottest processes"},
	} {
		definition := definition
		top.AddCommand(&cobra.Command{
			Use:   definition.use,
			Short: definition.short,
			RunE: func(_ *cobra.Command, _ []string) error {
				query := url.Values{
					"level": {definition.level},
					"limit": {strconv.Itoa(topLimit)},
				}
				if topNamespace != "" {
					query.Set("namespace", topNamespace)
				}
				if topPod != "" {
					query.Set("pod", topPod)
				}

				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()
				request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint+"/api/v1/cpu?"+query.Encode(), nil)
				if err != nil {
					return err
				}
				response, err := http.DefaultClient.Do(request)
				if err != nil {
					return err
				}
				defer response.Body.Close()
				body, err := io.ReadAll(response.Body)
				if err != nil {
					return err
				}
				if response.StatusCode != http.StatusOK {
					return fmt.Errorf("agent returned %s: %s", response.Status, body)
				}
				var formatted bytes.Buffer
				if err := json.Indent(&formatted, body, "", "  "); err != nil {
					return fmt.Errorf("decode CPU response: %w", err)
				}
				fmt.Println(formatted.String())
				return nil
			},
		})
	}
	root.AddCommand(top)

	var traceNamespace string
	var tracePod string
	var traceDuration time.Duration
	trace := &cobra.Command{
		Use:   "trace",
		Short: "Run an on-demand kernel latency measurement",
	}
	runQLat := &cobra.Command{
		Use:   "runqlat",
		Short: "Measure run queue latency for a workload",
		RunE: func(_ *cobra.Command, _ []string) error {
			if traceDuration < 100*time.Millisecond || traceDuration > 5*time.Minute {
				return fmt.Errorf("duration must be between 100ms and 5m")
			}
			query := url.Values{"duration": {traceDuration.String()}}
			if traceNamespace != "" {
				query.Set("namespace", traceNamespace)
			}
			if tracePod != "" {
				query.Set("pod", tracePod)
			}

			ctx, cancel := context.WithTimeout(context.Background(), traceDuration+15*time.Second)
			defer cancel()
			request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint+"/api/v1/runqlat?"+query.Encode(), nil)
			if err != nil {
				return err
			}
			response, err := http.DefaultClient.Do(request)
			if err != nil {
				return err
			}
			defer response.Body.Close()
			body, err := io.ReadAll(response.Body)
			if err != nil {
				return err
			}
			if response.StatusCode != http.StatusOK {
				return fmt.Errorf("agent returned %s: %s", response.Status, body)
			}
			var formatted bytes.Buffer
			if err := json.Indent(&formatted, body, "", "  "); err != nil {
				return fmt.Errorf("decode run queue latency response: %w", err)
			}
			fmt.Println(formatted.String())
			return nil
		},
	}
	runQLat.Flags().StringVarP(&traceNamespace, "namespace", "n", "", "filter by namespace")
	runQLat.Flags().StringVarP(&tracePod, "pod", "p", "", "filter by pod name")
	runQLat.Flags().DurationVar(&traceDuration, "duration", 10*time.Second, "measurement duration")
	trace.AddCommand(runQLat)
	root.AddCommand(trace)

	if err := root.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
