package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
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

	if err := root.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
