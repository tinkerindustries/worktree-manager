//go:build ignore

// ziphelper is the distribution's zip builder, run by dist/build.sh as
// `go run ./dist/ziphelper.go <out.zip> <dir> <file>...`. It exists so the
// Windows archive does not depend on a `zip` binary being installed on the
// machine cutting the release (the standard library's archive/zip is all
// it uses, and the module's one-dependency rule is untouched: this helper
// is not linked into either binary). Each named file inside <dir> is
// stored under its own name at the archive root.
package main

import (
	"archive/zip"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

func main() {
	if len(os.Args) < 4 {
		fmt.Fprintln(os.Stderr, "usage: ziphelper <out.zip> <dir> <file>...")
		os.Exit(2)
	}
	outPath, dir := os.Args[1], os.Args[2]
	files := os.Args[3:]

	out, err := os.Create(outPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ziphelper: %v\n", err)
		os.Exit(1)
	}
	zw := zip.NewWriter(out)
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
		hdr.Name = name
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
