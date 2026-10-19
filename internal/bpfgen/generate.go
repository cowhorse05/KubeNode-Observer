// Package bpfgen contains code generated from the eBPF C sources.
package bpfgen

//go:generate go run github.com/cilium/ebpf/cmd/bpf2go@v0.17.3 -target bpfel -cc clang -cflags "-O2 -g -Wall -Werror" Exec ../../bpf/exec.bpf.c -- -I/usr/include -I/usr/include/x86_64-linux-gnu
//go:generate go run github.com/cilium/ebpf/cmd/bpf2go@v0.17.3 -target bpfel -cc clang -cflags "-O2 -g -Wall -Werror" CPU ../../bpf/cpu.bpf.c -- -I/usr/include -I/usr/include/x86_64-linux-gnu
//go:generate go run github.com/cilium/ebpf/cmd/bpf2go@v0.17.3 -target bpfel -cc clang -cflags "-O2 -g -Wall -Werror" RunQLat ../../bpf/runqlat.bpf.c -- -I/usr/include -I/usr/include/x86_64-linux-gnu
