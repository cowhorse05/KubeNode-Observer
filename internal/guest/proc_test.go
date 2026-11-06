package guest

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func write(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0644); err != nil {
		t.Fatal(err)
	}
}
func stat(pid int, start uint64) string {
	fields := []string{"S", "1"}
	for len(fields) < 19 {
		fields = append(fields, "0")
	}
	fields = append(fields, fmt.Sprint(start))
	return fmt.Sprintf("%d (name (with) spaces) %s", pid, strings.Join(fields, " "))
}
func procFixture(t *testing.T) ProcFS {
	t.Helper()
	root := t.TempDir()
	for _, pid := range []int{100, 200} {
		base := filepath.Join(root, fmt.Sprint(pid))
		write(t, filepath.Join(base, "stat"), stat(pid, uint64(pid)))
		write(t, filepath.Join(base, "comm"), "guest-burn\n")
		for _, tid := range []int{pid, pid + 1} {
			write(t, filepath.Join(base, "task", fmt.Sprint(tid), "stat"), stat(tid, uint64(tid)))
			write(t, filepath.Join(base, "task", fmt.Sprint(tid), "comm"), "guest-burn\n")
		}
	}
	return ProcFS{Root: root}
}
func TestGuestProcfsGenerationAndBounds(t *testing.T) {
	p := procFixture(t)
	got, err := p.Snapshot("guest-burn")
	if err != nil || len(got) != 2 || len(got[100].Threads) != 2 || got[100].Start != 100 {
		t.Fatal(got, err)
	}
	got, err = p.Snapshot("absent")
	if err != nil || len(got) != 0 {
		t.Fatal(got, err)
	}
	write(t, filepath.Join(p.Root, "100", "task", "101", "stat"), "invalid")
	if _, err = p.Snapshot(""); err == nil {
		t.Fatal("invalid thread stat accepted")
	}
	p = procFixture(t)
	for i := 0; i < 513; i++ {
		if err := os.MkdirAll(filepath.Join(p.Root, "100", "task", fmt.Sprint(1000+i)), 0755); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = p.Snapshot(""); err == nil {
		t.Fatal("thread bound ignored")
	}
	p = ProcFS{Root: t.TempDir()}
	for i := 1; i <= 257; i++ {
		if err := os.Mkdir(filepath.Join(p.Root, fmt.Sprint(i)), 0755); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = p.Snapshot("absent"); err == nil {
		t.Fatal("process bound bypassed by filter")
	}
}
