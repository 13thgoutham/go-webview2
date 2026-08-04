// Command regen runs the generator against a local IDL and writes the result to a chosen
// directory. update_version_mapping.go can only regenerate as a side effect of checking
// Microsoft's release-notes page for a NEW version, which needs the network and rewrites the
// tree in place -- neither of which suits comparing generator output against the committed
// files.
//
// Usage: go run ./regen -idl WebView2.1.0.2903.40.idl -out /tmp/baseline
package main

import (
	"flag"
	"log"
	"os"
	"path/filepath"

	"updater/generator"
)

func main() {
	idl := flag.String("idl", "WebView2.1.0.2903.40.idl", "IDL file to generate from")
	out := flag.String("out", "", "directory to write the generated files to (required)")
	flag.Parse()
	if *out == "" {
		log.Fatal("-out is required")
	}

	data, err := os.ReadFile(*idl)
	if err != nil {
		log.Fatal(err)
	}
	files, err := generator.ParseIDL(data)
	if err != nil {
		log.Fatal(err)
	}
	// Clear the directory first. Reusing one across two IDL versions otherwise leaves the files
	// only the earlier version produced, and `diff -r` then reads as though they were still being
	// generated -- which defeats the one thing this command exists for.
	if err := os.RemoveAll(*out); err != nil {
		log.Fatal(err)
	}
	if err := os.MkdirAll(*out, 0o755); err != nil {
		log.Fatal(err)
	}
	for _, f := range files {
		if err := os.WriteFile(filepath.Join(*out, f.FileName), f.Content.Bytes(), 0o644); err != nil {
			log.Fatal(err)
		}
	}
	log.Printf("wrote %d files to %s", len(files), *out)
}
