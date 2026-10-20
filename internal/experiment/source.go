package experiment

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Archive all relevant source, including new files absent from git diff HEAD.
func (r *runner) archiveSource() error {
	files, err := r.command(r.ctx, nil, "git", "ls-files", "-z", "--cached", "--others", "--exclude-standard", "--", "bpf", "cmd", "internal", "examples", "hack", "deploy", "Makefile", "go.mod", "go.sum", "Dockerfile.local")
	if err != nil {
		return err
	}
	archive, err := os.Create(filepath.Join(r.dir, "source.tar.gz"))
	if err != nil {
		return err
	}
	gz := gzip.NewWriter(archive)
	tw := tar.NewWriter(gz)
	hashes := map[string]string{}
	var writeErr error
	for _, name := range strings.Split(string(files), "\x00") {
		if name == "" {
			continue
		}
		content, err := os.ReadFile(name)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			writeErr = err
			break
		}
		stat, err := os.Stat(name)
		if err != nil {
			writeErr = err
			break
		}
		sum := sha256.Sum256(content)
		hashes[name] = hex.EncodeToString(sum[:])
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: int64(stat.Mode().Perm()), Size: int64(len(content))}); err != nil {
			writeErr = err
			break
		}
		if _, err := tw.Write(content); err != nil {
			writeErr = err
			break
		}
	}
	for _, err := range []error{tw.Close(), gz.Close(), archive.Close()} {
		if writeErr == nil {
			writeErr = err
		}
	}
	if writeErr != nil {
		return fmt.Errorf("source archive: %w", writeErr)
	}
	return r.writeJSON("source-sha256.json", hashes)
}
