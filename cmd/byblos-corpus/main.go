// Command byblos-corpus writes the generated test corpus to a directory. The
// tests build the same documents in memory; this exists so the poppler oracle
// tooling has files to run against. The output directory is gitignored.
package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/dobbo-ca/byblos/internal/corpus"
)

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: byblos-corpus <outdir>")
		os.Exit(2)
	}
	dir := os.Args[1]
	if err := os.MkdirAll(dir, 0o755); err != nil {
		fmt.Fprintln(os.Stderr, "byblos-corpus:", err)
		os.Exit(1)
	}
	for _, d := range corpus.All() {
		path := filepath.Join(dir, d.Name+".pdf")
		if err := os.WriteFile(path, d.Data, 0o644); err != nil {
			fmt.Fprintln(os.Stderr, "byblos-corpus:", err)
			os.Exit(1)
		}
		fmt.Printf("%-16s %7d bytes  %s\n", d.Name, len(d.Data), d.Desc)
	}
}
