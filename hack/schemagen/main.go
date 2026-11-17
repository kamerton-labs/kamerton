// Copyright 2026 The Kamerton Authors
// SPDX-License-Identifier: AGPL-3.0-only

// Command schemagen converts the generated SystemDescription CRD into the
// standalone JSON Schema embedded into the core. Invoked by `make generate`.
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/kamerton-labs/kamerton/internal/schema"
)

func main() {
	crd := flag.String("crd", "", "path to the generated CRD YAML")
	out := flag.String("out", "", "path of the JSON Schema to write")
	flag.Parse()
	if *crd == "" || *out == "" {
		fmt.Fprintln(os.Stderr, "usage: schemagen -crd <crd.yaml> -out <schema.json>")
		os.Exit(2)
	}
	data, err := os.ReadFile(*crd)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	js, err := schema.FromCRD(data)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if err := os.WriteFile(*out, js, 0o644); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
