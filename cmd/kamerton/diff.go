// Copyright 2026 The Kamerton Authors
// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"errors"
	"flag"
	"fmt"
	"os"

	"github.com/kamerton-labs/kamerton/api/v1alpha1"
	"github.com/kamerton-labs/kamerton/internal/description"
	"github.com/kamerton-labs/kamerton/internal/rulediff"
)

// diff prints which alerting rules a change of a description changes, for a review.
func diff(args []string) error {
	fs := flag.NewFlagSet("diff", flag.ExitOnError)
	file := fs.String("f", "", "the description after the change")
	against := fs.String("against", "", "the description before the change; empty or missing file: the system is new")
	exitCode := fs.Bool("exit-code", false, "exit with status 2 when the rules differ")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *file == "" {
		return errors.New("diff: -f is required")
	}
	after, _, err := description.LoadFile(*file)
	if err != nil {
		return err
	}
	var before *v1alpha1.SystemDescription
	if *against != "" {
		if _, statErr := os.Stat(*against); statErr == nil {
			if before, _, err = description.LoadFile(*against); err != nil {
				return fmt.Errorf("the description before the change: %w", err)
			}
		}
	}
	res, err := rulediff.Compare(before, after)
	if err != nil {
		return err
	}
	res.Print(os.Stdout)
	if res.Any() && *exitCode {
		os.Exit(2)
	}
	return nil
}
