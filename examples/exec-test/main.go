package main

import (
	"fmt"
	"os"
	"os/exec"
	"time"
)

const childArgument = "--child"

func main() {
	if len(os.Args) == 2 && os.Args[1] == childArgument {
		return
	}

	for i := 0; i < 5; i++ {
		command := exec.Command("/workload", childArgument)
		if err := command.Run(); err != nil {
			fmt.Fprintf(os.Stderr, "run child %d: %v\n", i, err)
			os.Exit(1)
		}
		fmt.Printf("generated exec event %d\n", i)
		time.Sleep(500 * time.Millisecond)
	}
}
