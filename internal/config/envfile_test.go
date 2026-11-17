// Copyright 2026 The Kamerton Authors
// SPDX-License-Identifier: AGPL-3.0-only

package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestParseEnv(t *testing.T) {
	src := `# comment
KAMERTON_AUTH_USER=admin
export KAMERTON_LOG_LEVEL = debug
PLAIN=value # trailing comment
HASH=a#b
DQ="with spaces # not a comment\n\"quoted\""
SQ='literal \n $x'
EMPTY=

`
	got, err := ParseEnv([]byte(src))
	if err != nil {
		t.Fatal(err)
	}
	want := [][2]string{
		{"KAMERTON_AUTH_USER", "admin"},
		{"KAMERTON_LOG_LEVEL", "debug"},
		{"PLAIN", "value"},
		{"HASH", "a#b"},
		{"DQ", "with spaces # not a comment\n\"quoted\""},
		{"SQ", `literal \n $x`},
		{"EMPTY", ""},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %q\nwant %q", got, want)
	}

	for _, bad := range []string{"NOEQUALS", "1BAD=x", `Q="open`, `Q='open`, `Q="a" b`} {
		_, err := ParseEnv([]byte("OK=1\n" + bad + "\n"))
		if err == nil || !strings.Contains(err.Error(), "line 2") {
			t.Errorf("%q: %v", bad, err)
		}
	}
	// Errors never echo the value.
	if _, err := ParseEnv([]byte(`KAMERTON_AUTH_PASSWORD="hunter2`)); err == nil || strings.Contains(err.Error(), "hunter2") {
		t.Errorf("error must not contain the value: %v", err)
	}
}

func TestLoadEnvFileEnvironmentWins(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, ".env")
	if err := os.WriteFile(p, []byte("KTEST_A=from-file\nKTEST_B=from-file\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("KTEST_A", "from-env")
	t.Setenv("KTEST_B", "")
	_ = os.Unsetenv("KTEST_B")
	set, err := LoadEnvFile(p, true)
	if err != nil {
		t.Fatal(err)
	}
	if os.Getenv("KTEST_A") != "from-env" || os.Getenv("KTEST_B") != "from-file" || !reflect.DeepEqual(set, []string{"KTEST_B"}) {
		t.Fatalf("A=%q B=%q set=%v", os.Getenv("KTEST_A"), os.Getenv("KTEST_B"), set)
	}
	if _, err := LoadEnvFile(filepath.Join(dir, "missing"), false); err != nil {
		t.Errorf("missing optional file: %v", err)
	}
	if _, err := LoadEnvFile(filepath.Join(dir, "missing"), true); err == nil {
		t.Error("missing -env-file must be an error")
	}
}

func TestEnvFileArg(t *testing.T) {
	cases := []struct {
		args  []string
		path  string
		given bool
	}{
		{nil, ".env", false},
		{[]string{"-listen-address", ":1"}, ".env", false},
		{[]string{"-env-file", "a.env"}, "a.env", true},
		{[]string{"--env-file=b.env"}, "b.env", true},
		{[]string{"-x", "--", "-env-file", "c"}, ".env", false},
	}
	for _, c := range cases {
		p, g := EnvFileArg(c.args)
		if p != c.path || g != c.given {
			t.Errorf("%v: %q %v", c.args, p, g)
		}
	}
}

func TestAuthFromEnv(t *testing.T) {
	set := func(u, p string) {
		t.Setenv(EnvAuthUser, u)
		t.Setenv(EnvAuthPassword, p)
	}
	set("", "")
	if a, err := AuthFromEnv(); err != nil || a.Enabled() {
		t.Errorf("neither: %v %v", a.Enabled(), err)
	}
	set("admin", "pw")
	if a, err := AuthFromEnv(); err != nil || !a.Enabled() {
		t.Errorf("both: %v %v", a.Enabled(), err)
	}
	set("admin", "")
	if _, err := AuthFromEnv(); err == nil || !strings.Contains(err.Error(), EnvAuthPassword) {
		t.Errorf("only user: %v", err)
	}
	set("", "secret-pw")
	_, err := AuthFromEnv()
	if err == nil || !strings.Contains(err.Error(), EnvAuthUser) || strings.Contains(err.Error(), "secret-pw") {
		t.Errorf("only password: %v", err)
	}
}
