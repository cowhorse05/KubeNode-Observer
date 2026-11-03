package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"time"
)

func main() {
	identity := flag.Bool("identity", false, "print PID and kernel release for host/guest boundary tests")
	child := flag.Bool("child", false, "exit immediately after exec")
	childExitCode := flag.Int("child-exit-code", 0, "exit code used by child processes")
	iterations := flag.Int("iterations", 5, "number of child execs")
	interval := flag.Duration("interval", 500*time.Millisecond, "delay between child execs")
	flag.Parse()
	if *identity {
		kernel, err := os.ReadFile("/proc/sys/kernel/osrelease")
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		_ = json.NewEncoder(os.Stdout).Encode(struct {
			PID    int    `json:"pid"`
			Kernel string `json:"kernel"`
		}{os.Getpid(), string(kernel)})
		return
	}

	if *child {
		os.Exit(*childExitCode)
	}

	for i := 0; i < *iterations; i++ {
		command := exec.Command("/workload", "--child", fmt.Sprintf("--child-exit-code=%d", *childExitCode))
		if err := command.Run(); err != nil {
			if command.ProcessState == nil || command.ProcessState.ExitCode() != *childExitCode {
				fmt.Fprintf(os.Stderr, "run child %d: %v\n", i, err)
				os.Exit(1)
			}
		}
		fmt.Printf("generated exec event %d\n", i)
		time.Sleep(*interval)
	}
}
