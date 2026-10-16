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

	if err := root.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
