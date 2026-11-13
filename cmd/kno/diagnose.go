package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/cowhorse05/KubeNode-Observer/internal/diagnosis"
	"github.com/spf13/cobra"
)

const maxDiagnosisInputBytes = 4 << 20

func newDiagnoseCommand() *cobra.Command {
	diagnose := &cobra.Command{
		Use:   "diagnose",
		Short: "Turn controlled experiment evidence into bounded diagnostic signals",
	}
	var summaryPath string
	var output string
	contention := &cobra.Command{
		Use:   "contention",
		Short: "Interpret an M10c Host x Guest four-quadrant summary",
		RunE: func(command *cobra.Command, _ []string) error {
			data, err := readDiagnosisInput(summaryPath, command.InOrStdin())
			if err != nil {
				return err
			}
			summary, err := diagnosis.DecodeM10cSummary(data)
			if err != nil {
				return err
			}
			readout, err := diagnosis.AnalyzeM10c(summary)
			if err != nil {
				return err
			}
			switch output {
			case "text":
				_, err = fmt.Fprint(command.OutOrStdout(), formatDiagnosisText(readout))
			case "json":
				var encoded []byte
				encoded, err = json.MarshalIndent(readout, "", "  ")
				if err == nil {
					_, err = fmt.Fprintf(command.OutOrStdout(), "%s\n", encoded)
				}
			default:
				return fmt.Errorf("output must be text or json")
			}
			return err
		},
	}
	contention.Flags().StringVar(&summaryPath, "summary", "", "M10c summary.json path, or - for stdin")
	contention.Flags().StringVarP(&output, "output", "o", "text", "output format: text or json")
	_ = contention.MarkFlagRequired("summary")
	diagnose.AddCommand(contention)
	return diagnose
}

func readDiagnosisInput(path string, stdin io.Reader) ([]byte, error) {
	var reader io.Reader = stdin
	var file *os.File
	var err error
	if path != "-" {
		file, err = os.Open(path)
		if err != nil {
			return nil, fmt.Errorf("open summary: %w", err)
		}
		defer file.Close()
		reader = file
	}
	data, err := io.ReadAll(io.LimitReader(reader, maxDiagnosisInputBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read summary: %w", err)
	}
	if len(data) > maxDiagnosisInputBytes {
		return nil, fmt.Errorf("summary exceeds 4 MiB")
	}
	return data, nil
}

func formatDiagnosisText(readout diagnosis.Readout) string {
	var output strings.Builder
	fmt.Fprintf(&output, "Evidence assessment: %s (%d paired rounds)\n", readout.Assessment, readout.Rounds)
	for _, contrast := range readout.Contrasts {
		fmt.Fprintf(&output, "\n%s [%s -> %s]: %s\n", contrast.Scenario, contrast.Reference, contrast.Target, contrast.Verdict)
		formatChange(&output, "Guest P95", contrast.GuestP95, readout.Rounds)
		formatChange(&output, "Host vCPU 0 P95", contrast.HostVcpu0P95, readout.Rounds)
	}
	output.WriteString("\nBoundaries:\n")
	for _, limitation := range readout.Limitations {
		fmt.Fprintf(&output, "- %s\n", limitation)
	}
	return output.String()
}

func formatChange(output *strings.Builder, label string, change diagnosis.Change, rounds int) {
	fmt.Fprintf(output, "  %s: %s; higher %d/%d, lower %d/%d, tied %d/%d; median %.0f -> %.0f us\n",
		label, change.Direction, change.IncreasedRounds, rounds, change.DecreasedRounds, rounds,
		change.TiedRounds, rounds, change.ReferenceMedian, change.TargetMedian)
}
