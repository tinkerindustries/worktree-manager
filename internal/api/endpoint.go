package api

// endpoint.go is the endpoint file: <store>/endpoint.json, the one file
// the client is allowed to read from the store. It carries the
// coordinator's base URL and the host token — the bearer that authenticates
// a host client — written at startup by wtd at 0600 on unix (the
// current-user ACL on Windows, inherited from the store root) and read by
// the client to resolve the coordinator (PLAN-SCOPE.md, "Behaviours": the
// store database is never client-readable; only endpoint.json is).
//
// The token is generated with crypto/rand on first start and reused
// thereafter: a host client's proof of identity is reading this 0600 file
// in the store, which lives in the user's own home (plan.md §7, "Host
// identity is weaker than SO_PEERCRED").
//
// WT_HOME is read by both binaries from this phase on — the client reads
// endpoint.json from it — which is the one deliberate change to the
// environment contract. The database itself stays coordinator-only, and a
// container never mounts the store: it is given WT_ENDPOINT and
// WT_CLIENT_TOKEN explicitly.

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/mrgeoffrich/worktree-manager/internal/platform"
)

// EndpointSchemaVersion is the endpoint file's schema version. A file
// with a newer version is refused naming the upgrade — the same rule the
// store database carries.
const EndpointSchemaVersion = 1

// EndpointFileName is the endpoint file's name inside the store root.
const EndpointFileName = "endpoint.json"

// DefaultAddr is the compiled-in coordinator address: 127.0.0.1:7833.
// The client falls back to it when neither WT_ENDPOINT nor an endpoint
// file is present, and wtd listens on it unless --addr says otherwise.
// The constant itself is platform's, because a supervisor registration
// needs the same default and platform cannot import this package.
const DefaultAddr = platform.DefaultCoordinatorAddr

// DefaultEndpoint is the compiled-in base URL.
const DefaultEndpoint = "http://" + DefaultAddr

// Endpoint is the endpoint file's content.
type Endpoint struct {
	SchemaVersion int    `json:"schema_version"`
	BaseURL       string `json:"base_url"`
	Token         string `json:"token"`
}

// Home resolves the store root the way the coordinator does: WT_HOME when
// set, else $HOME/.wt. The client reads endpoint.json from it; the store
// database itself stays coordinator-only.
func Home() (string, error) {
	if h := os.Getenv("WT_HOME"); h != "" {
		return h, nil
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return "", fmt.Errorf("WT_HOME is not set and the home directory cannot be determined; refusing to invent a location (set WT_HOME or $HOME)")
	}
	return filepath.Join(home, ".wt"), nil
}

// EndpointPath returns the endpoint file's path inside the store root.
func EndpointPath(root string) string {
	return filepath.Join(root, EndpointFileName)
}

// ReadEndpoint reads and validates the endpoint file at path. A malformed
// file is an error naming the field — never a silent fallback to the
// compiled default (PLAN-SCOPE.md, "In scope"): a file that cannot be
// trusted cannot be ignored, because it may be a newer schema the client
// must be told to upgrade for. A missing file is os.ErrNotExist, which the
// caller resolves to the compiled default.
func ReadEndpoint(path string) (*Endpoint, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var ep Endpoint
	if err := json.Unmarshal(data, &ep); err != nil {
		return nil, fmt.Errorf("%s is not one JSON object: %v; fix the file, then re-run", path, err)
	}
	if ep.SchemaVersion == 0 {
		return nil, fmt.Errorf("%s: missing field \"schema_version\"; fix the file, then re-run", path)
	}
	if ep.SchemaVersion != EndpointSchemaVersion {
		return nil, fmt.Errorf("%s carries schema version %d but this build understands %d; upgrade wt, then re-run", path, ep.SchemaVersion, EndpointSchemaVersion)
	}
	if ep.BaseURL == "" {
		return nil, fmt.Errorf("%s: missing field \"base_url\"; fix the file, then re-run", path)
	}
	u, err := url.Parse(ep.BaseURL)
	if err != nil || u.Scheme != "http" || u.Host == "" {
		return nil, fmt.Errorf("%s: field \"base_url\" is not an http URL: %q; fix the file, then re-run", path, ep.BaseURL)
	}
	if ep.Token == "" {
		return nil, fmt.Errorf("%s: missing field \"token\"; fix the file, then re-run", path)
	}
	ep.BaseURL = strings.TrimRight(ep.BaseURL, "/")
	return &ep, nil
}

// WriteEndpoint writes the endpoint file atomically at 0600 on unix. On
// Windows mode bits are meaningless and the privacy comes from the store
// root's current-user ACL, which files created inside it inherit — the
// same model as the store database (08-platform.md §4.6). The mode is
// verified after the write, never assumed.
func WriteEndpoint(path string, ep *Endpoint) error {
	data, err := json.MarshalIndent(ep, "", "  ")
	if err != nil {
		return fmt.Errorf("encoding %s: %w", path, err)
	}
	data = append(data, '\n')
	if err := platform.AtomicWrite(path, data, 0o600); err != nil {
		return err
	}
	return platform.VerifyPrivateFileModes(path)
}
