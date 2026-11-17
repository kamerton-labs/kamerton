// Copyright 2026 The Kamerton Authors
// SPDX-License-Identifier: AGPL-3.0-only

package description

import (
	"strings"
	"testing"
)

func TestCheckLinkURL(t *testing.T) {
	for _, ok := range []string{
		"https://wiki.example.com/runbooks/checkout",
		"http://kamerton.example:8080",
		"HTTPS://Wiki.Example.com/a?b=c#d",
	} {
		if p := CheckLinkURL(ok); p != "" {
			t.Errorf("%q rejected: %s", ok, p)
		}
	}
	for _, bad := range []string{
		"", "javascript:alert(1)", "data:text/html,<b>x</b>", "/runbooks/checkout", "wiki.example.com/runbooks",
		"ftp://files.example.com/r", "http://", "https:///path", "https://user:pw@wiki.example.com", "https://wiki.example.com@evil.example",
		"https://wiki.example.com/a b", "https://wiki.example.com/\n",
	} {
		if p := CheckLinkURL(bad); p == "" {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestRunbookURLValidation(t *testing.T) {
	doc := func(url string) []byte {
		return []byte(strings.Replace(minimal, "type: http", "type: http\n      runbookURL: '"+url+"'", 1))
	}
	if res := Validate(doc("https://wiki.example.com/runbooks/api")); !res.Valid {
		t.Errorf("https runbook rejected: %+v", res.Errors)
	}
	for _, bad := range []string{"javascript:alert(document.cookie)", "/runbooks/api", "wiki/api"} {
		res := Validate(doc(bad))
		if res.Valid || len(res.Errors) != 1 || res.Errors[0].Path != "/spec/services/0/runbookURL" || !strings.Contains(res.Errors[0].Message, "http") {
			t.Errorf("%q: %+v", bad, res.Errors)
		}
	}
}
