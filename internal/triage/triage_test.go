// Copyright 2026 The Kamerton Authors
// SPDX-License-Identifier: AGPL-3.0-only

package triage

import (
	"reflect"
	"testing"

	"github.com/kamerton-labs/kamerton/api/v1alpha1"
)

func graph() *Graph {
	return NewGraph(&v1alpha1.SystemDescriptionSpec{Services: []v1alpha1.Service{
		{Name: "frontend-proxy", DependsOn: []string{"frontend"}},
		{Name: "frontend", DependsOn: []string{"cart", "checkout", "product-catalog"}},
		{Name: "checkout", DependsOn: []string{"cart", "payment", "orders"}},
		{Name: "cart", DependsOn: []string{"valkey-cart"}},
		{Name: "payment"},
		{Name: "product-catalog"},
		{Name: "orders"},
		{Name: "valkey-cart"},
		{Name: "loop-a", DependsOn: []string{"loop-b"}},
		{Name: "loop-b", DependsOn: []string{"loop-a"}},
	}})
}

func TestProbableSource(t *testing.T) {
	cases := []struct {
		name      string
		alerting  string
		unhealthy []string
		source    string
		chain     []string
	}{
		{"deep source", "frontend-proxy", []string{"frontend-proxy", "frontend", "checkout", "payment"}, "payment", []string{"frontend-proxy", "frontend", "checkout", "payment"}},
		{"self", "checkout", []string{"checkout"}, "checkout", []string{"checkout"}},
		{"database behind cart", "frontend", []string{"frontend", "cart", "valkey-cart"}, "valkey-cart", []string{"frontend", "cart", "valkey-cart"}},
		{"healthy intermediate", "frontend", []string{"frontend", "payment"}, "payment", []string{"frontend", "checkout", "payment"}},
		{"cycle", "loop-a", []string{"loop-a", "loop-b"}, "loop-a", []string{"loop-a"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			u := map[string]bool{}
			for _, n := range tc.unhealthy {
				u[n] = true
			}
			f := graph().ProbableSource(tc.alerting, u)
			if f.ProbableSource != tc.source || !reflect.DeepEqual(f.Chain, tc.chain) {
				t.Fatalf("got %s %v, want %s %v", f.ProbableSource, f.Chain, tc.source, tc.chain)
			}
		})
	}
}
