//go:build ignore

// ziphelper is the distribution's zip builder, run by dist/build.sh as
// `go run ./dist/ziphelper.go <out.zip> <dir> <prefix> <file>...`. It
// exists so the Windows archive does not depend on a `zip` binary being
// installed on the machine cutting the release (the standard library's
// archive/zip is all it uses, and the module's one-dependency rule is
// untouched: this helper is not linked into either binary).
//
// Each named file inside <dir> is stored under <prefix>/<name>, so
// unpacking the archive produces one directory rather than five loose
// files. The separator is written as "/" rather than taken from
// filepath: zip entry names are always slash-separated, whatever the
// machine cutting the release runs.
package main

import (
	"archive/zip"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

func main() {
	if len(os.Args) < 5 {
		fmt.Fprintln(os.Stderr, "usage: ziphelper <out.zip> <dir> <prefix> <file>...")
		os.Exit(2)
	}
	outPath, dir, prefix := os.Args[1], os.Args[2], os.Args[3]
	files := os.Args[4:]

	out, err := os.Create(outPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ziphelper: %v\n", err)
		os.Exit(1)
	}
	zw := zip.NewWriter(out)
	// The directory's own entry. Extraction creates parent directories
	// without it, but a zip that names the directory it unpacks into is
	// what an extractor shows before it unpacks.
	dirHdr := &zip.FileHeader{Name: prefix + "/", Method: zip.Store}
	dirHdr.SetMode(os.ModeDir | 0o755)
	if _, err := zw.CreateHeader(dirHdr); err != nil {
		fmt.Fprintf(os.Stderr, "ziphelper: %v\n", err)
		os.Exit(1)
	}
	for _, name := range files {
		path := filepath.Join(dir, name)
		fi, err := os.Stat(path)
		if err != nil {
			fmt.Fprintf(os.Stderr, "ziphelper: %v\n", err)
			os.Exit(1)
		}
		hdr, err := zip.FileInfoHeader(fi)
		if err != nil {
			fmt.Fprintf(os.Stderr, "ziphelper: %v\n", err)
			os.Exit(1)
		}
		hdr.Name = prefix + "/" + name
		hdr.Method = zip.Deflate
		w, err := zw.CreateHeader(hdr)
		if err != nil {
			fmt.Fprintf(os.Stderr, "ziphelper: %v\n", err)
			os.Exit(1)
		}
		f, err := os.Open(path)
		if err != nil {
			fmt.Fprintf(os.Stderr, "ziphelper: %v\n", err)
			os.Exit(1)
		}
		if _, err := io.Copy(w, f); err != nil {
			fmt.Fprintf(os.Stderr, "ziphelper: %v\n", err)
			os.Exit(1)
		}
		f.Close()
	}
	if err := zw.Close(); err != nil {
		fmt.Fprintf(os.Stderr, "ziphelper: %v\n", err)
		os.Exit(1)
	}
	if err := out.Close(); err != nil {
		fmt.Fprintf(os.Stderr, "ziphelper: %v\n", err)
		os.Exit(1)
	}
}
