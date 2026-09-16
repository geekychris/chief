package main

import (
	"archive/tar"
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// writeTarball packs the given absolute paths into a gzipped tarball.
// The tar header names are stored relative to the user's HOME so
// restore can round-trip on the same machine (or a differently-named
// HOME with a small tweak).
func writeTarball(outPath string, paths []string) error {
	home, _ := os.UserHomeDir()
	out, err := os.Create(outPath)
	if err != nil {
		return err
	}
	defer out.Close()
	gz := gzip.NewWriter(out)
	defer gz.Close()
	tw := tar.NewWriter(gz)
	defer tw.Close()

	for _, p := range paths {
		fi, err := os.Stat(p)
		if err != nil {
			continue
		}
		hdr, err := tar.FileInfoHeader(fi, "")
		if err != nil {
			return err
		}
		hdr.Name = tarName(p, home)
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		f, err := os.Open(p)
		if err != nil {
			return err
		}
		if _, err := io.Copy(tw, f); err != nil {
			f.Close()
			return err
		}
		f.Close()
	}
	return nil
}

// tarName encodes a path relative to $HOME so restore can rehydrate
// under a new $HOME cleanly. Falls back to the absolute path when
// outside $HOME.
func tarName(p, home string) string {
	if home != "" && strings.HasPrefix(p, home+"/") {
		return "HOME/" + strings.TrimPrefix(p, home+"/")
	}
	return "ABS" + p
}

// extractTarball restores files packed by writeTarball, remapping the
// "HOME/" prefix back to the current $HOME. When dryRun is true only
// prints the target paths — useful before overwriting live state.
func extractTarball(src string, dryRun bool) error {
	home, _ := os.UserHomeDir()
	f, err := os.Open(src)
	if err != nil {
		return err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	restored := 0
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		target := untarName(hdr.Name, home)
		if dryRun {
			fmt.Printf("would restore → %s (%d bytes)\n", target, hdr.Size)
			continue
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		out, err := os.Create(target)
		if err != nil {
			return err
		}
		if _, err := io.Copy(out, tr); err != nil {
			out.Close()
			return err
		}
		out.Close()
		if err := os.Chmod(target, hdr.FileInfo().Mode()); err != nil {
			return err
		}
		restored++
	}
	if dryRun {
		fmt.Println("\n(dry run — pass --dry-run=false to actually restore)")
	} else {
		fmt.Printf("restored %d files\n", restored)
	}
	return nil
}

func untarName(name, home string) string {
	if strings.HasPrefix(name, "HOME/") {
		return filepath.Join(home, strings.TrimPrefix(name, "HOME/"))
	}
	if strings.HasPrefix(name, "ABS") {
		return strings.TrimPrefix(name, "ABS")
	}
	// Legacy / unknown prefix: place under HOME to avoid clobbering /etc.
	return filepath.Join(home, name)
}
