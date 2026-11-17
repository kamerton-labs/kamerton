// Copyright 2026 The Kamerton Authors
// SPDX-License-Identifier: AGPL-3.0-only

package description

import (
	"fmt"
	"os"
	"strings"
	"testing"
)

const exampleFile = "../../testdata/otel-demo/system.yaml"

func TestExampleIsValid(t *testing.T) {
	data, err := os.ReadFile(exampleFile)
	if err != nil {
		t.Fatal(err)
	}
	res := Validate(data)
	if !res.Valid {
		t.Fatalf("example is invalid: %+v", res.Errors)
	}
	if len(res.Warnings) != 0 {
		t.Errorf("unexpected warnings: %+v", res.Warnings)
	}
	if got := len(res.Document.Spec.Services); got < 10 {
		t.Errorf("expected at least 10 services, got %d", got)
	}
}

const minimal = `apiVersion: kamerton.dev/v1alpha1
kind: SystemDescription
metadata:
  name: shop
spec:
  services:
    - name: api
      type: http
`

func TestMinimalIsValid(t *testing.T) {
	res := Validate([]byte(minimal))
	if !res.Valid {
		t.Fatalf("minimal description is invalid: %+v", res.Errors)
	}
}

func TestValidationErrors(t *testing.T) {
	cases := []struct {
		name     string
		doc      string
		wantPath string
		wantMsg  string
	}{
		{
			name:     "unknown field (typo)",
			doc:      strings.Replace(minimal, "type: http", "type: http\n      critcal: true", 1),
			wantPath: "/spec/services/0",
			wantMsg:  "critcal",
		},
		{
			name:     "bad enum",
			doc:      strings.Replace(minimal, "type: http", "type: soap", 1),
			wantPath: "/spec/services/0/type",
		},
		{
			name:     "unknown dependency",
			doc:      strings.Replace(minimal, "type: http", "type: http\n      dependsOn: [db]", 1),
			wantPath: "/spec/services/0/dependsOn/0",
			wantMsg:  `unknown service "db"`,
		},
		{
			name:     "bad regex",
			doc:      strings.Replace(minimal, "type: http", "type: http\n      excludeOperations: ['(']", 1),
			wantPath: "/spec/services/0/excludeOperations/0",
			wantMsg:  "invalid regular expression",
		},
		{
			name:     "bad duration",
			doc:      strings.Replace(minimal, "type: http", "type: http\n      errorRate: {for: 5 minutes}", 1),
			wantPath: "/spec/services/0/errorRate/for",
		},
		{
			name:     "threshold out of range",
			doc:      strings.Replace(minimal, "type: http", "type: http\n      errorRate: {threshold: 5}", 1),
			wantPath: "/spec/services/0/errorRate/threshold",
		},
		{
			name:     "queue without queue spec",
			doc:      strings.Replace(minimal, "type: http", "type: queue", 1),
			wantPath: "/spec/services/0/queue",
		},
		{
			name:     "wrong apiVersion",
			doc:      strings.Replace(minimal, "kamerton.dev/v1alpha1", "kamerton.dev/v2", 1),
			wantPath: "/apiVersion",
		},
		{
			name:    "yaml syntax",
			doc:     "spec: [",
			wantMsg: "YAML syntax",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res := Validate([]byte(tc.doc))
			if res.Valid {
				t.Fatal("expected invalid document")
			}
			for _, e := range res.Errors {
				if e.Path == tc.wantPath && strings.Contains(e.Message, tc.wantMsg) {
					return
				}
			}
			t.Fatalf("no error with path %q containing %q; got %+v", tc.wantPath, tc.wantMsg, res.Errors)
		})
	}
}

func TestShortRateWindowWarns(t *testing.T) {
	doc := strings.Replace(minimal, "spec:\n", "spec:\n  defaults:\n    rateWindow: 1m\n", 1)
	res := Validate([]byte(doc))
	if !res.Valid {
		t.Fatalf("unexpected errors: %+v", res.Errors)
	}
	if len(res.Warnings) != 1 || res.Warnings[0].Path != "/spec/defaults/rateWindow" {
		t.Fatalf("expected one rateWindow warning, got %+v", res.Warnings)
	}
}

func TestCycleWarns(t *testing.T) {
	doc := `apiVersion: kamerton.dev/v1alpha1
kind: SystemDescription
metadata: {name: x}
spec:
  services:
    - {name: a, type: http, dependsOn: [b]}
    - {name: b, type: http, dependsOn: [a]}
`
	res := Validate([]byte(doc))
	if !res.Valid {
		t.Fatalf("unexpected errors: %+v", res.Errors)
	}
	if len(res.Warnings) != 1 || !strings.Contains(res.Warnings[0].Message, "cycle") {
		t.Fatalf("expected a cycle warning, got %+v", res.Warnings)
	}
}

func TestRoundTripYAML(t *testing.T) {
	res := Validate([]byte(minimal))
	out, err := ToYAML(res.Document)
	if err != nil {
		t.Fatal(err)
	}
	if again := Validate(out); !again.Valid {
		t.Fatalf("serialized description is invalid: %+v\n%s", again.Errors, out)
	}
}

func TestRoutesAndLanguage(t *testing.T) {
	const base = `apiVersion: kamerton.dev/v1alpha1
kind: SystemDescription
metadata: {name: x}
spec:
  services:
    - {name: api, type: http, critical: true}
  notifications:
    language: %s
    receivers:
      - name: chat
        telegram: {chatID: -100, botTokenSecret: {name: kamerton-telegram, key: bot-token}}
    routing:
      critical: [chat]
      routes:
        - receiver: %s
          severities: [critical]
          services: [%s]
`
	res := Validate([]byte(fmt.Sprintf(base, "en", "chat", "api")))
	if !res.Valid {
		t.Fatalf("errors: %+v", res.Errors)
	}
	if len(res.Warnings) == 0 || !strings.Contains(res.Warnings[0].Message, "twice") {
		t.Errorf("expected a duplicate-delivery warning: %+v", res.Warnings)
	}
	for _, tc := range []struct{ lang, recv, svc, want string }{
		{"ru", "nope", "api", `unknown receiver "nope"`},
		{"ru", "chat", "ghost", `unknown service "ghost"`},
		{"de", "chat", "api", "language"},
	} {
		res := Validate([]byte(fmt.Sprintf(base, tc.lang, tc.recv, tc.svc)))
		if res.Valid {
			t.Errorf("%+v: expected invalid", tc)
			continue
		}
		var all []string
		for _, e := range res.Errors {
			all = append(all, e.Path+": "+e.Message)
		}
		if !strings.Contains(strings.Join(all, "\n"), tc.want) {
			t.Errorf("%+v: errors %v", tc, all)
		}
	}
}

func TestCustomRuleValidation(t *testing.T) {
	base := strings.Replace(minimal, "type: http", "type: http\n      customRules:\n        - name: r\n          title: Т\nRULE", 1)
	cases := []struct{ rule, path, msg string }{
		{"          expr: 'sum(rate(x[5m]) >'", "/spec/services/0/customRules/0/expr", "parse error"},
		{"          expr: 'up'\n          condition: {metric: up, operator: gt, threshold: 1}", "/spec/services/0/customRules/0", "both"},
		{"          for: 5m", "/spec/services/0/customRules/0", "needs a condition or an expr"},
		{"          condition: {metric: up, operator: gt, threshold: 1, by: ['bad-label']}", "/spec/services/0/customRules/0/condition/by/0", "invalid label name"},
		{"          condition: {metric: '1up', operator: gt, threshold: 1}", "/spec/services/0/customRules/0/condition/metric", ""},
	}
	for _, tc := range cases {
		res := Validate([]byte(strings.Replace(base, "RULE", tc.rule, 1)))
		found := false
		for _, e := range res.Errors {
			if e.Path == tc.path && strings.Contains(e.Message, tc.msg) {
				found = true
			}
		}
		if res.Valid || !found {
			t.Errorf("%q: want error at %s containing %q, got %+v", tc.rule, tc.path, tc.msg, res.Errors)
		}
	}
	ok := Validate([]byte(strings.Replace(base, "RULE", "          condition: {metric: up, operator: lt, threshold: 1}", 1)))
	if !ok.Valid {
		t.Errorf("valid custom rule rejected: %+v", ok.Errors)
	}
}

func TestTrafficDropValidation(t *testing.T) {
	svc := func(extra string) string {
		return strings.Replace(minimal, "type: http", "type: http\n"+extra, 1)
	}
	issues := func(doc string) Result {
		t.Helper()
		return Validate([]byte(doc))
	}
	ok := issues(svc("      trafficDrop: {enabled: true, threshold: 0, minRequestsPerSecond: 0.1, for: 3m, severity: warning}"))
	if !ok.Valid || len(ok.Warnings) != 0 {
		t.Errorf("valid policy: %+v %+v", ok.Errors, ok.Warnings)
	}
	for _, bad := range []string{"threshold: 1", "threshold: -0.1", "minRequestsPerSecond: -1", "for: soon", "severity: high"} {
		if r := issues(svc("      trafficDrop: {" + bad + "}")); r.Valid {
			t.Errorf("%s must be rejected", bad)
		}
	}
	if r := issues(svc("      trafficDrop:\n        message: {title: 'нет {calls}'}")); r.Valid || r.Errors[0].Path != "/spec/services/0/trafficDrop/message/title" {
		t.Errorf("message checks: %+v", r.Errors)
	}
	short := issues(svc("      trafficDrop: {for: 30s}"))
	if !short.Valid || len(short.Warnings) != 1 || short.Warnings[0].Path != "/spec/services/0/trafficDrop/for" {
		t.Errorf("short for must warn: %+v", short.Warnings)
	}
	db := issues(strings.Replace(minimal, "type: http", "type: database\n      trafficDrop: {}", 1))
	if !db.Valid || len(db.Warnings) != 1 || !strings.Contains(db.Warnings[0].Message, "client spans") {
		t.Errorf("database warning: %+v", db.Warnings)
	}
	defaults := issues(strings.Replace(minimal, "spec:\n", "spec:\n  defaults:\n    trafficDrop: {for: 10s}\n", 1))
	if !defaults.Valid || len(defaults.Warnings) != 1 || defaults.Warnings[0].Path != "/spec/defaults/trafficDrop/for" {
		t.Errorf("defaults: %+v", defaults.Warnings)
	}
}

// spec.stack names a set of programs: an ID, checked by the schema. Whether
// the set exists is known only at runtime (a warning in the API).
func TestStackField(t *testing.T) {
	with := func(v string) []byte {
		return []byte(strings.Replace(minimal, "spec:\n", "spec:\n  stack: "+v+"\n", 1))
	}
	for _, ok := range []string{"main", "team-a", "komanda-b-2"} {
		res := Validate(with(ok))
		if !res.Valid || res.Document.Spec.Stack != ok {
			t.Errorf("stack %q: %+v", ok, res.Errors)
		}
	}
	for _, bad := range []string{`"Team A"`, "team_a", "-a", strings.Repeat("a", 64)} {
		res := Validate(with(bad))
		if res.Valid {
			t.Errorf("stack %s must be invalid", bad)
			continue
		}
		if res.Errors[0].Path != "/spec/stack" {
			t.Errorf("stack %s: error at %s: %s", bad, res.Errors[0].Path, res.Errors[0].Message)
		}
	}
}
