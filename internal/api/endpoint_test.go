package api

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestReadEndpointValid parses a well-formed endpoint file and normalises
// the base URL's trailing slash away.
func TestReadEndpointValid(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, EndpointFileName)
	data := `{"schema_version": 1, "base_url": "http://127.0.0.1:7833/", "token": "` + strings.Repeat("ab", 32) + `"}`
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	ep, err := ReadEndpoint(path)
	if err != nil {
		t.Fatalf("ReadEndpoint: %v", err)
	}
	if ep.BaseURL != "http://127.0.0.1:7833" {
		t.Errorf("base_url = %q, want the trailing slash stripped", ep.BaseURL)
	}
	if ep.Token != strings.Repeat("ab", 32) {
		t.Errorf("token = %q", ep.Token)
	}
}

// TestReadEndpointMalformedNamesTheField: a malformed endpoint file is an
// error naming the field — never a silent fallback to the compiled
// default (PLAN-SCOPE.md, "In scope").
func TestReadEndpointMalformedNamesTheField(t *testing.T) {
	dir := t.TempDir()
	cases := []struct {
		name, content, wantField string
	}{
		{"not json", "not json", "not one JSON object"},
		{"missing schema version", `{"base_url": "http://127.0.0.1:7833", "token": "tok"}`, "schema_version"},
		{"newer schema", `{"schema_version": 2, "base_url": "http://127.0.0.1:7833", "token": "tok"}`, "schema version 2"},
		{"missing base url", `{"schema_version": 1, "token": "tok"}`, "base_url"},
		{"bad base url", `{"schema_version": 1, "base_url": "ftp://x", "token": "tok"}`, "base_url"},
		{"missing token", `{"schema_version": 1, "base_url": "http://127.0.0.1:7833"}`, "token"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(dir, tc.name+".json")
			if err := os.WriteFile(path, []byte(tc.content), 0o600); err != nil {
				t.Fatal(err)
			}
			_, err := ReadEndpoint(path)
			if err == nil {
				t.Fatal("a malformed endpoint file was accepted")
			}
			if !strings.Contains(err.Error(), tc.wantField) {
				t.Errorf("the refusal must name the field (%q): %v", tc.wantField, err)
			}
			if !strings.Contains(err.Error(), "re-run") {
				t.Errorf("the refusal must name a fix: %v", err)
			}
		})
	}
}

// TestReadEndpointMissingIsNotExist: a missing file is os.ErrNotExist, so
// the caller can distinguish "no endpoint file" (compiled default) from
// "endpoint file is broken" (refuse, naming the field).
func TestReadEndpointMissingIsNotExist(t *testing.T) {
	_, err := ReadEndpoint(filepath.Join(t.TempDir(), EndpointFileName))
	if !os.IsNotExist(err) {
		t.Fatalf("missing endpoint file = %v, want os.ErrNotExist", err)
	}
}

// TestWriteEndpointIs0600: the endpoint file is born 0600 on unix — the
// host token lives in it, so it must be owner-only. (Windows modes are
// ACL-shaped; the file inherits the store root's current-user ACL.)
func TestWriteEndpointIs0600(t *testing.T) {
	if os.PathSeparator == '\\' {
		t.Skip("mode bits are meaningless on Windows; the store-root ACL covers the file")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, EndpointFileName)
	if err := WriteEndpoint(path, &Endpoint{SchemaVersion: 1, BaseURL: DefaultEndpoint, Token: strings.Repeat("cd", 32)}); err != nil {
		t.Fatalf("WriteEndpoint: %v", err)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm()&0o077 != 0 {
		t.Errorf("endpoint.json is %o, want 0600", fi.Mode().Perm())
	}
	ep, err := ReadEndpoint(path)
	if err != nil {
		t.Fatalf("read-back: %v", err)
	}
	if ep.Token != strings.Repeat("cd", 32) || ep.BaseURL != DefaultEndpoint {
		t.Errorf("read-back = %+v", ep)
	}
}

// TestHome: WT_HOME wins; otherwise $HOME/.wt — the same rule the store
// root follows, now read by both binaries.
func TestHome(t *testing.T) {
	t.Setenv("WT_HOME", "/tmp/custom-wt-home")
	got, err := Home()
	if err != nil || got != "/tmp/custom-wt-home" {
		t.Fatalf("Home with WT_HOME = %q, %v", got, err)
	}
	t.Setenv("WT_HOME", "")
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		t.Skip("no home directory in this environment")
	}
	got, err = Home()
	if err != nil || got != filepath.Join(home, ".wt") {
		t.Fatalf("Home without WT_HOME = %q, %v; want %q", got, err, filepath.Join(home, ".wt"))
	}
}
