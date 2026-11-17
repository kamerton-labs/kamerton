// Copyright 2026 The Kamerton Authors
// SPDX-License-Identifier: AGPL-3.0-only

package schema

import (
	"bytes"
	"os"
	"testing"
)

const crdPath = "../../deploy/helm/kamerton/crds/kamerton.dev_systemdescriptions.yaml"

// TestSchemaUpToDate fails when the embedded JSON Schema was not regenerated
// after the CRD (and therefore the Go types) changed. Fix with `make generate`.
func TestSchemaUpToDate(t *testing.T) {
	crd, err := os.ReadFile(crdPath)
	if err != nil {
		t.Fatal(err)
	}
	want, err := FromCRD(crd)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(want, JSON()) {
		t.Fatal("internal/schema/systemdescription.schema.json is stale; run `make generate`")
	}
}

func TestSchemaCompiles(t *testing.T) {
	if _, err := compiledSchema(); err != nil {
		t.Fatal(err)
	}
}

func TestNoOpenAPIBooleansLeft(t *testing.T) {
	for _, bad := range []string{`"exclusiveMinimum": true`, `"exclusiveMaximum": true`, `x-kubernetes-`} {
		if bytes.Contains(JSON(), []byte(bad)) {
			t.Errorf("schema still contains %s", bad)
		}
	}
}
