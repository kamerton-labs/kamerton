// Copyright 2026 The Kamerton Authors
// SPDX-License-Identifier: AGPL-3.0-only

package config

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"regexp"
	"strings"
)

// DefaultEnvFile is read by `kamerton serve` from the working directory when present.
const DefaultEnvFile = ".env"

var envKey = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// ParseEnv parses KEY=VALUE lines: blank lines and lines starting with # are
// skipped, an optional "export " prefix is allowed, values may be in double
// quotes (with \n, \t, \" and \\ escapes) or single quotes (literal), and an
// unquoted value ends at " #". Errors name the line, never the value.
func ParseEnv(data []byte) ([][2]string, error) {
	var out [][2]string
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	n := 0
	for sc.Scan() {
		n++
		line := strings.TrimSpace(sc.Text())
		if n == 1 {
			line = strings.TrimPrefix(line, "\uFEFF")
		}
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimSpace(strings.TrimPrefix(line, "export "))
		key, val, ok := strings.Cut(line, "=")
		key = strings.TrimSpace(key)
		if !ok || !envKey.MatchString(key) {
			return nil, fmt.Errorf("line %d: expected KEY=VALUE", n)
		}
		v, err := parseEnvValue(strings.TrimSpace(val))
		if err != nil {
			return nil, fmt.Errorf("line %d (%s): %w", n, key, err)
		}
		out = append(out, [2]string{key, v})
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

func parseEnvValue(v string) (string, error) {
	if v == "" {
		return "", nil
	}
	switch v[0] {
	case '"':
		var b strings.Builder
		for i := 1; i < len(v); i++ {
			c := v[i]
			switch {
			case c == '\\' && i+1 < len(v):
				i++
				switch v[i] {
				case 'n':
					b.WriteByte('\n')
				case 't':
					b.WriteByte('\t')
				case 'r':
					b.WriteByte('\r')
				default: // \" \\ \$ and anything else: the character itself
					b.WriteByte(v[i])
				}
			case c == '"':
				if rest := strings.TrimSpace(v[i+1:]); rest != "" && !strings.HasPrefix(rest, "#") {
					return "", errors.New("unexpected text after the closing quote")
				}
				return b.String(), nil
			default:
				b.WriteByte(c)
			}
		}
		return "", errors.New("missing closing double quote")
	case '\'':
		end := strings.IndexByte(v[1:], '\'')
		if end < 0 {
			return "", errors.New("missing closing single quote")
		}
		if rest := strings.TrimSpace(v[end+2:]); rest != "" && !strings.HasPrefix(rest, "#") {
			return "", errors.New("unexpected text after the closing quote")
		}
		return v[1 : end+1], nil
	}
	if i := strings.Index(v, " #"); i >= 0 {
		v = v[:i]
	}
	if i := strings.Index(v, "\t#"); i >= 0 {
		v = v[:i]
	}
	return strings.TrimSpace(v), nil
}

// LoadEnvFile sets the variables of an env file that are not already set in
// the environment (the environment wins). A missing file is an error only
// when required. It returns the names of the variables it set.
func LoadEnvFile(path string, required bool) ([]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if !required && errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("env file: %w", err)
	}
	pairs, err := ParseEnv(data)
	if err != nil {
		return nil, fmt.Errorf("env file %s: %w", path, err)
	}
	var set []string
	for _, kv := range pairs {
		if _, exists := os.LookupEnv(kv[0]); exists {
			continue
		}
		if err := os.Setenv(kv[0], kv[1]); err != nil {
			return set, fmt.Errorf("env file %s: %s: %w", path, kv[0], err)
		}
		set = append(set, kv[0])
	}
	return set, nil
}

// EnvFileArg finds -env-file / --env-file in serve's arguments before the
// flags are parsed (flag defaults are read from the environment, so the file
// has to be loaded first). It returns the path and whether it was given.
func EnvFileArg(args []string) (string, bool) {
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			break
		}
		name := strings.TrimLeft(a, "-")
		if name == a || len(a)-len(name) > 2 {
			continue
		}
		if v, ok := strings.CutPrefix(name, "env-file="); ok {
			return v, true
		}
		if name == "env-file" && i+1 < len(args) {
			return args[i+1], true
		}
	}
	return DefaultEnvFile, false
}

// Auth is the login configuration, read from the environment only (a
// password in a flag would be visible in the process list).
type Auth struct {
	User     string
	Password string
	// Secret is the session signing key; empty derives it from the password.
	Secret string
}

// Auth environment variables.
const (
	EnvAuthUser     = "KAMERTON_AUTH_USER"
	EnvAuthPassword = "KAMERTON_AUTH_PASSWORD"
	EnvAuthSecret   = "KAMERTON_AUTH_SECRET"
)

// Enabled reports whether login is configured.
func (a Auth) Enabled() bool { return a.User != "" && a.Password != "" }

// AuthFromEnv reads the login settings. Both user and password, or neither,
// must be set; an empty value counts as not set.
func AuthFromEnv() (Auth, error) {
	a := Auth{User: os.Getenv(EnvAuthUser), Password: os.Getenv(EnvAuthPassword), Secret: os.Getenv(EnvAuthSecret)}
	switch {
	case a.User != "" && a.Password == "":
		return a, fmt.Errorf("%s is set but %s is not: set both to enable login, or neither to keep the UI open", EnvAuthUser, EnvAuthPassword)
	case a.User == "" && a.Password != "":
		return a, fmt.Errorf("%s is set but %s is not: set both to enable login, or neither to keep the UI open", EnvAuthPassword, EnvAuthUser)
	}
	return a, nil
}
