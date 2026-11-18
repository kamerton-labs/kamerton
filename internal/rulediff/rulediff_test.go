// Copyright 2026 The Kamerton Authors
// SPDX-License-Identifier: AGPL-3.0-only

package rulediff

import (
	"strings"
	"testing"

	"github.com/kamerton-labs/kamerton/api/v1alpha1"
	"github.com/kamerton-labs/kamerton/internal/description"
)

func load(t *testing.T, mutate func(*v1alpha1.SystemDescription)) *v1alpha1.SystemDescription {
	t.Helper()
	sd, _, err := description.LoadFile("../../testdata/otel-demo/system.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if mutate != nil {
		mutate(sd)
	}
	return sd
}

func TestCompareNamesWhatAChangeDoes(t *testing.T) {
	before := load(t, nil)
	after := load(t, func(sd *v1alpha1.SystemDescription) {
		for i := range sd.Spec.Services {
			if sd.Spec.Services[i].Name == "ad" {
				sd.Spec.Services[i].Critical = !sd.Spec.Services[i].Critical
			}
		}
	})
	same, err := Compare(before, before)
	if err != nil || same.Any() {
		t.Fatalf("unchanged: %+v, %v", same, err)
	}
	res, err := Compare(before, after)
	if err != nil || !res.Any() || len(res.Changed) == 0 || len(res.Removed) != 0 {
		t.Fatalf("a change of severity: %+v, %v", res, err)
	}
	var out strings.Builder
	res.Print(&out)
	if text := out.String(); !strings.Contains(text, "ad · KamertonErrorRateHigh: severity ") || strings.Contains(text, "cart · KamertonErrorRateHigh") {
		t.Errorf("diff:\n%s", text)
	}
	fresh, err := Compare(nil, after)
	if err != nil || !fresh.New || fresh.Total == 0 || len(fresh.Added) != 0 {
		t.Errorf("new system: %+v, %v", fresh, err)
	}
}
