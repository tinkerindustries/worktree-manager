package spec

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestInvalidSpecsRejected exercises exit criterion 4 and the required
// validation refusals: one invalid spec per required rejection, committed
// under testdata/specs/ (plan.md §4.1's testdata/specs/ holds only invalid
// specs, so no spec document exists in two places to drift apart).
func TestInvalidSpecsRejected(t *testing.T) {
	tests := []struct {
		file        string
		fieldPrefix string
		reasonSub   string
	}{
		{"cycle.yaml", "resources[0].template", "template cycle: compose → compose_test → compose"},
		{"unknown-resource.yaml", "resources[0].template", `unknown resource "db"`},
		{"unknown-variable.yaml", "resources[0].template", "unknown template variable {DB_PATH}"},
		{"overlong-name.yaml", "resources[0].name", "resolved namespace name"},
		{"stride-over-99.yaml", "slots.max", "stride-form ceiling of 99"},
		{"unknown-format.yaml", "emit.descriptor.format", `unknown format "xml"`},
		{"unknown-language.yaml", "emit.reader.language", `unsupported language "python"`},
		{"unknown-version.yaml", "version", "unsupported version 2"},
		{"ipv6-pool.yaml", "resources[0].pool", "is not an IPv4 CIDR"},
		{"cycle-with-referenced-leaf.yaml", "resources[1].template", "template cycle: aa → bb → aa"},
		{"unknown-removal-policy.yaml", "removal.open_pr", `"ignore" is not a removal policy`},
		{"worktree-path-without-slug.yaml", "worktrees.path", "must reference {slug}"},
	}
	for _, tt := range tests {
		t.Run(tt.file, func(t *testing.T) {
			data, err := os.ReadFile(filepath.Join("..", "..", "testdata", "specs", tt.file))
			if err != nil {
				t.Fatal(err)
			}
			parsed, parseErr := Parse(data)
			err = parseErr
			if err == nil {
				err = Validate(parsed)
			}
			if err == nil {
				t.Fatal("spec validated, want refusal")
			}
			var fe *FieldError
			if !asFieldError(err, &fe) {
				t.Fatalf("error = %v, want a FieldError", err)
			}
			if !strings.HasPrefix(fe.Field, tt.fieldPrefix) {
				t.Errorf("field = %q, want prefix %q", fe.Field, tt.fieldPrefix)
			}
			if !strings.Contains(fe.Reason, tt.reasonSub) {
				t.Errorf("reason = %q, want substring %q", fe.Reason, tt.reasonSub)
			}
		})
	}
}

// TestValidateRefusals is the table of inline validation refusals: every
// refusal names the field and the reason.
func TestValidateRefusals(t *testing.T) {
	tests := []struct {
		name        string
		doc         string
		fieldPrefix string
		reasonSub   string
	}{
		{
			"missing app", "version: 1\n", "app", "required",
		},
		{
			"slots.max below 1",
			"version: 1\napp: x\nslots:\n  max: 0\n",
			"slots.max", "below 1",
		},
		{
			"duplicate resource name",
			"version: 1\napp: x\nresources:\n  - type: port\n    name: api\n  - type: port\n    name: api\n",
			"resources[1].name", "duplicate resource name",
		},
		{
			"resource name collides with builtin",
			"version: 1\napp: x\nresources:\n  - type: port\n    name: slug\n",
			"resources[0].name", "reserved template variable",
		},
		{
			"port group with size on stride",
			"version: 1\napp: x\nresources:\n  - type: port\n    name: api\n    size: 2\n",
			"resources[0].size", "only valid for group-form",
		},
		{
			"port group size below 1",
			"version: 1\napp: x\nresources:\n  - type: port\n    name: api\n    form: group\n    size: 0\n",
			"resources[0].size", "below 1",
		},
		{
			"unknown port form",
			"version: 1\napp: x\nresources:\n  - type: port\n    name: api\n    form: blitz\n",
			"resources[0].form", `unknown port form "blitz"`,
		},
		{
			"namespace without template",
			"version: 1\napp: x\nresources:\n  - type: namespace\n    name: compose\n    kind: compose\n",
			"resources[0].template", "required",
		},
		{
			"plain namespace with files",
			"version: 1\napp: x\nresources:\n  - type: namespace\n    name: compose\n    kind: plain\n    template: \"{app}-{slug}\"\n    files: [compose.yaml]\n",
			"resources[0].files", "only valid for kind: compose",
		},
		{
			"cidr without pool",
			"version: 1\napp: x\nresources:\n  - type: cidr\n    name: egress\n    size: 22\n",
			"resources[0].pool", "required",
		},
		{
			"cidr invalid pool",
			"version: 1\napp: x\nresources:\n  - type: cidr\n    name: egress\n    pool: 999.1.1.1/16\n    size: 22\n",
			"resources[0].pool", "not a valid CIDR",
		},
		{
			"cidr size below pool mask",
			"version: 1\napp: x\nresources:\n  - type: cidr\n    name: egress\n    pool: 172.30.0.0/16\n    size: 8\n",
			"resources[0].size", "outside 16..32",
		},
		{
			"state-path with pool",
			"version: 1\napp: x\nresources:\n  - type: state-path\n    name: db\n    template: \"{home}/db.sqlite\"\n    pool: 172.30.0.0/16\n",
			"resources[0].pool", "not valid for this resource type",
		},
		{
			"state-path unknown default",
			"version: 1\napp: x\nresources:\n  - type: state-path\n    name: db\n    template: \"{home}/db.sqlite\"\n    default: everyone\n",
			"resources[0].default", `unknown default "everyone"`,
		},
		{
			"seed without from",
			"version: 1\napp: x\nresources:\n  - type: state-path\n    name: db\n    template: \"{home}/db.sqlite\"\n    seed:\n      modes: [seeded]\n",
			"resources[0].seed.from", "required",
		},
		{
			"seed default outside modes",
			"version: 1\napp: x\nresources:\n  - type: state-path\n    name: db\n    template: \"{home}/db.sqlite\"\n    seed:\n      from: \"{home}/src.sqlite\"\n      modes: [empty]\n      default: seeded\n",
			"resources[0].seed.default", "not one of the declared modes",
		},
		{
			"machine unknown driver",
			"version: 1\napp: x\nresources:\n  - type: machine\n    name: vm\n    template: \"{app}-{slug}\"\n    driver: qemu\n",
			"resources[0].driver", `unknown machine driver "qemu"`,
		},
		{
			"machine max_concurrent below 1",
			"version: 1\napp: x\nresources:\n  - type: machine\n    name: vm\n    template: \"{app}-{slug}\"\n    max_concurrent: 0\n",
			"resources[0].max_concurrent", "below 1",
		},
		{
			"unclosed brace",
			"version: 1\napp: x\nresources:\n  - type: namespace\n    name: compose\n    kind: compose\n    template: \"{app\"\n",
			"resources[0].template", "unclosed",
		},
		{
			"reserved port out of range",
			"version: 1\napp: x\nreserved:\n  ports: [70000]\n",
			"reserved.ports", "not a port number",
		},
		{
			"shared without impact",
			"version: 1\napp: x\nshared:\n  - name: \"{home}/db.sqlite\"\n",
			"shared[0].impact", "required",
		},
		{
			"hook without run",
			"version: 1\napp: x\nhooks:\n  install:\n    timeout: 5s\n",
			"hooks.install.run", "required",
		},
		{
			"hook bad duration",
			"version: 1\napp: x\nhooks:\n  build:\n    run: go build\n    timeout: soon\n",
			"hooks.build.timeout", "not a Go duration string",
		},
		{
			"hook unknown variable",
			"version: 1\napp: x\nhooks:\n  build:\n    run: \"echo {NOPE}\"\n",
			"hooks.build.run", "unknown template variable {NOPE}",
		},
		{
			"descriptor filename dotted",
			"version: 1\napp: x\nemit:\n  descriptor:\n    filename: .wt-env.yaml\n    format: yaml\n",
			"emit.descriptor.filename", "must not start with a dot",
		},
		{
			"descriptor filename shadows spec",
			"version: 1\napp: x\nemit:\n  descriptor:\n    filename: wt.yaml\n    format: yaml\n",
			"emit.descriptor.filename", "must not be wt.yaml",
		},
		{
			"env key invalid name",
			"version: 1\napp: x\nemit:\n  descriptor:\n    filename: wt-env.json\n    format: json\n  env:\n    path: .env\n    keys:\n      \"1BAD\": \"{slot}\"\n",
			"emit.env.keys.1BAD", "not a valid environment variable name",
		},
		{
			"reader env var invalid name",
			"version: 1\napp: x\nemit:\n  descriptor:\n    filename: wt-env.yaml\n    format: yaml\n  reader:\n    language: go\n    path: internal/env/env.go\n    package: env\n    env_var: \"1BAD\"\n",
			"emit.reader.env_var", "not a valid environment variable name",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			parsed, parseErr := Parse([]byte(tt.doc))
			err := parseErr
			if err == nil {
				err = Validate(parsed)
			}
			if err == nil {
				t.Fatal("validated, want refusal")
			}
			var fe *FieldError
			if !asFieldError(err, &fe) {
				t.Fatalf("error = %v, want a FieldError", err)
			}
			if !strings.HasPrefix(fe.Field, tt.fieldPrefix) {
				t.Errorf("field = %q, want prefix %q (reason %q)", fe.Field, tt.fieldPrefix, fe.Reason)
			}
			if !strings.Contains(fe.Reason, tt.reasonSub) {
				t.Errorf("reason = %q, want substring %q", fe.Reason, tt.reasonSub)
			}
		})
	}
}

// TestValidSlug covers the slug rule's accepted and refused forms. The rule
// is the design's own, verbatim: ^[a-z0-9][a-z0-9-]*$ (M1 §4.1) — which, as
// stated, admits a trailing hyphen.
func TestValidSlug(t *testing.T) {
	valid := []string{"no", "on", "off", "yes", "y", "brisk-otter", "a", "0", "0-a", "x-"}
	for _, s := range valid {
		if !ValidSlug(s) {
			t.Errorf("ValidSlug(%q) = false, want true", s)
		}
	}
	invalid := []string{"", "-x", "X", "x y", "a_b", strings.Repeat("a", SlugMaxLen+1), "a.", ".a"}
	for _, s := range invalid {
		if ValidSlug(s) {
			t.Errorf("ValidSlug(%q) = true, want false", s)
		}
	}
}
