// Package image downloads, verifies, and converts guest base images.
package image

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/lima-vm/go-qcow2reader"
)

const DefaultImage = "ubuntu:noble"

const nobleBase = "https://cloud-images.ubuntu.com/releases/noble/release/"

func Resolve(ref string) (url, sumsURL, fileName string, err error) {
	if ref != "ubuntu:noble" {
		return "", "", "", fmt.Errorf("unsupported image ref %q (M1 supports ubuntu:noble only)", ref)
	}
	fileName = "ubuntu-24.04-server-cloudimg-arm64.img"
	return nobleBase + fileName, nobleBase + "SHA256SUMS", fileName, nil
}

func rawCachePath(imagesDir, ref string) string {
	return filepath.Join(imagesDir, strings.ReplaceAll(ref, ":", "-")+"-arm64.raw")
}

func Ensure(ctx context.Context, imagesDir, ref string) (string, error) {
	rawPath := rawCachePath(imagesDir, ref)
	if _, err := os.Stat(rawPath); err == nil {
		return rawPath, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	url, sumsURL, fileName, err := Resolve(ref)
	if err != nil {
		return "", err
	}
	qcowTmp := rawPath + ".qcow2.tmp"
	defer os.Remove(qcowTmp) // cleans up partial downloads too
	sum, err := download(ctx, url, qcowTmp)
	if err != nil {
		return "", err
	}
	sums, err := fetch(ctx, sumsURL)
	if err != nil {
		return "", err
	}
	want, err := parseSHA256SUMS(sums, fileName)
	if err != nil {
		return "", err
	}
	if sum != want {
		return "", fmt.Errorf("sha256 mismatch for %s: got %s want %s", fileName, sum, want)
	}
	if err := convertToRaw(qcowTmp, rawPath+".tmp"); err != nil {
		return "", err
	}
	return rawPath, os.Rename(rawPath+".tmp", rawPath)
}

func download(ctx context.Context, url, dst string) (sha string, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("GET %s: %s", url, resp.Status)
	}
	f, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return "", err
	}
	h := sha256.New()
	if _, err := io.Copy(io.MultiWriter(f, h), resp.Body); err != nil {
		f.Close()
		return "", err
	}
	if err := f.Close(); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func fetch(ctx context.Context, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("GET %s: %s", url, resp.Status)
	}
	return io.ReadAll(resp.Body)
}

func parseSHA256SUMS(sums []byte, fileName string) (string, error) {
	sc := bufio.NewScanner(strings.NewReader(string(sums)))
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) == 2 && strings.TrimPrefix(fields[1], "*") == fileName {
			return fields[0], nil
		}
	}
	return "", fmt.Errorf("%s not found in SHA256SUMS", fileName)
}

func convertToRaw(qcowPath, dst string) error {
	f, err := os.Open(qcowPath)
	if err != nil {
		return err
	}
	defer f.Close()
	img, err := qcow2reader.Open(f)
	if err != nil {
		return err
	}
	defer img.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	// sequential copy of the virtual disk content
	if _, err := io.Copy(out, io.NewSectionReader(img, 0, img.Size())); err != nil {
		out.Close()
		os.Remove(dst)
		return err
	}
	return out.Close()
}

func CloneDisk(rawBase, dst string, sizeGiB uint64) error {
	if err := cloneFile(rawBase, dst); err != nil {
		return err
	}
	return os.Truncate(dst, int64(sizeGiB)<<30)
}

// copyFile is the portable fallback used when an APFS clone isn't possible.
func copyFile(rawBase, dst string) error {
	src, err := os.Open(rawBase)
	if err != nil {
		return err
	}
	defer src.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, src); err != nil {
		out.Close()
		os.Remove(dst)
		return err
	}
	return out.Close()
}
