package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/spf13/cobra"
)

func newClockCommand(endpoint *string) *cobra.Command {
	var sandbox string
	var samples int
	cmd := &cobra.Command{Use: "clock", Short: "Estimate exchange-local Guest minus Host clock bounds (not event alignment)", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		if samples < 3 || samples > 15 {
			return fmt.Errorf("samples must be 3..15")
		}
		ctx, cancel := context.WithTimeout(cmd.Context(), 10*time.Second)
		defer cancel()
		req, err := http.NewRequestWithContext(ctx, "GET", *endpoint+"/api/v1/guest/clock?"+url.Values{"sandbox": {sandbox}, "samples": {strconv.Itoa(samples)}}.Encode(), nil)
		if err != nil {
			return err
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			return err
		}
		defer res.Body.Close()
		body, err := io.ReadAll(io.LimitReader(res.Body, (64<<10)+1))
		if err != nil {
			return err
		}
		if len(body) > 64<<10 {
			return fmt.Errorf("clock response exceeds 64KiB")
		}
		if res.StatusCode != 200 {
			return fmt.Errorf("agent returned %s: %s", res.Status, body)
		}
		var out bytes.Buffer
		if err = json.Indent(&out, body, "", "  "); err != nil {
			return err
		}
		_, err = fmt.Fprintln(cmd.OutOrStdout(), out.String())
		return err
	}}
	cmd.Flags().StringVar(&sandbox, "sandbox", "", "full current Kata Sandbox ID (required)")
	cmd.Flags().IntVar(&samples, "samples", 7, "number of sequential clock exchanges (3..15)")
	_ = cmd.MarkFlagRequired("sandbox")
	return cmd
}
