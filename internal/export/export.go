// Package export packages a machine's config.json + disk.img into a
// portable tar.gz for moving a machine between hosts, and unpacks one back
// out. It knows nothing about the registry root or the daemon — Write/Read
// operate on plain directories, so callers (the CLI for Write, the CLI +
// daemon for Read) control where files live.
package export

import (
	"archive/tar"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/ForceAI-KW/umbra/internal/registry"
)

// entries are the only files Write ever produces and Read ever accepts.
// Read rejecting any tar entry whose name isn't exactly one of these is
// what blocks path traversal (e.g. "../evil") from a hand-crafted or
// corrupted tarball — there's a fixed, exact allowlist, not a prefix or
// "no .." check that could be bypassed.
var entries = []string{"config.json", "disk.img"}

// Write streams machineDir's config.json and disk.img into a gzip'd tar at
// outFile, using flat entry names (no directory components).
func Write(machineDir, outFile string) error {
	out, err := os.Create(outFile)
	if err != nil {
		return err
	}
	defer out.Close()

	gw := gzip.NewWriter(out)
	tw := tar.NewWriter(gw)

	for _, name := range entries {
		if err := addFile(tw, filepath.Join(machineDir, name), name); err != nil {
			return fmt.Errorf("export: write %s: %w", name, err)
		}
	}
	if err := tw.Close(); err != nil {
		return err
	}
	return gw.Close()
}

func addFile(tw *tar.Writer, srcPath, name string) error {
	f, err := os.Open(srcPath)
	if err != nil {
		return err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return err
	}
	if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o600, Size: info.Size()}); err != nil {
		return err
	}
	_, err = io.Copy(tw, f)
	return err
}

// Read extracts inFile into destDir, accepting ONLY tar entries named
// exactly "config.json" or "disk.img" (regular files) — any other entry
// name (path traversal like "../evil", an absolute path, a symlink, or a
// directory) is rejected before anything is written to destDir. It parses
// the extracted config.json into a registry.Machine and returns it.
func Read(inFile, destDir string) (*registry.Machine, error) {
	f, err := os.Open(inFile)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	gr, err := gzip.NewReader(f)
	if err != nil {
		return nil, err
	}
	defer gr.Close()

	tr := tar.NewReader(gr)
	seen := map[string]bool{}
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		if !allowed(hdr.Name) {
			return nil, fmt.Errorf("export: rejected tar entry %q (only config.json and disk.img are allowed)", hdr.Name)
		}
		if hdr.Typeflag != tar.TypeReg {
			return nil, fmt.Errorf("export: rejected non-regular tar entry %q", hdr.Name)
		}
		dst := filepath.Join(destDir, hdr.Name)
		out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
		if err != nil {
			return nil, err
		}
		_, copyErr := io.Copy(out, tr)
		closeErr := out.Close()
		if copyErr != nil {
			return nil, copyErr
		}
		if closeErr != nil {
			return nil, closeErr
		}
		seen[hdr.Name] = true
	}
	if !seen["config.json"] {
		return nil, fmt.Errorf("export: tarball missing config.json")
	}
	if !seen["disk.img"] {
		return nil, fmt.Errorf("export: tarball missing disk.img")
	}

	b, err := os.ReadFile(filepath.Join(destDir, "config.json"))
	if err != nil {
		return nil, err
	}
	var m registry.Machine
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, err
	}
	return &m, nil
}

func allowed(name string) bool {
	for _, e := range entries {
		if name == e {
			return true
		}
	}
	return false
}
