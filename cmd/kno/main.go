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

	if err := root.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
