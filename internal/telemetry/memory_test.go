package telemetry

import (
	"os"
	"path/filepath"
	"testing"
)

func TestMemoryReader(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "pod", "agent")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	for name, content := range map[string]string{"memory.current": "12345\n", "memory.max": "134217728\n", "memory.stat": "anon 100\nfile 20\nkernel 30\nslab 10\n", "memory.events": "oom 0\noom_kill 0\nmax 7\n"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}
	m, err := readMemory(root, "/pod/agent")
	if err != nil {
		t.Fatal(err)
	}
	if m.Current != 12345 || m.Max != "134217728" || m.Peak != nil || m.Stat["kernel"] != 30 || m.Events["max"] != 7 {
		t.Fatalf("memory: %+v", m)
	}
	if err := os.WriteFile(filepath.Join(dir, "memory.peak"), []byte("15000\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "memory.max"), []byte("max\n"), 0644); err != nil {
		t.Fatal(err)
	}
	m, err = readMemory(root, "/pod/agent")
	if err != nil || m.Peak == nil || *m.Peak != 15000 || m.Max != "max" {
		t.Fatalf("peak/unlimited: %+v %v", m, err)
	}
	if _, err := readMemory(root, "/../../outside"); err == nil {
		t.Fatal("accepted traversal")
	}
	if err := os.WriteFile(filepath.Join(dir, "memory.stat"), []byte("anon invalid\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := readMemory(root, "/pod/agent"); err == nil {
		t.Fatal("invalid stat accepted")
	}
}

func TestMemoryReaderRejectsIncompleteCounters(t *testing.T) {
	for _, tc := range []struct{ name, file, content string }{
		{"current", "memory.current", "not-a-number"},
		{"maximum", "memory.max", ""},
		{"peak", "memory.peak", "invalid"},
		{"stat-fields", "memory.stat", "anon 1\nfile 2\n"},
		{"event-fields", "memory.events", "oom 0\n"},
		{"negative", "memory.events", "oom -1\noom_kill 0\n"},
		{"extra-column", "memory.events", "oom 0 extra\noom_kill 0\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			files := map[string]string{"memory.current": "1", "memory.max": "max", "memory.stat": "anon 1\nfile 2\nkernel 3\n", "memory.events": "oom 0\noom_kill 0\n"}
			files[tc.file] = tc.content
			for name, content := range files {
				if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0644); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := readMemory(root, "/"); err == nil {
				t.Fatal("incomplete memory data accepted")
			}
		})
	}
}

func TestRSSReader(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "self"), 0755); err != nil {
		t.Fatal(err)
	}
	for _, input := range []string{"", "123", "123 invalid", "123 -1"} {
		if err := os.WriteFile(filepath.Join(root, "self", "statm"), []byte(input), 0644); err != nil {
			t.Fatal(err)
		}
		if _, err := readRSS(root); err == nil {
			t.Fatalf("invalid statm accepted: %q", input)
		}
	}
}
