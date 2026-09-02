package spec

// purge_test.go covers the one purge decision the state-path driver
// deletes by and `wt rm` reports by, and the two validation rails around
// the on_teardown: always form: a store deleted by default must be one
// worktree's, and it must leave a way to keep it for a run.

import "testing"

func TestPurgeDecision(t *testing.T) {
	flagOnly := &Resource{Type: "state-path", Name: "db", Purge: &Purge{Flag: "--purge-db"}}
	always := &Resource{Type: "state-path", Name: "userdata",
		Purge: &Purge{OnTeardown: PurgeOnTeardownAlways, KeepFlag: "--keep-userdata"}}
	both := &Resource{Type: "state-path", Name: "cache",
		Purge: &Purge{Flag: "--purge-cache", OnTeardown: PurgeOnTeardownAlways, KeepFlag: "--keep-cache"}}
	none := &Resource{Type: "state-path", Name: "logs"}

	tests := []struct {
		name       string
		res        *Resource
		purgeFlags []string
		keepFlags  []string
		want       bool
	}{
		{"no purge block survives", none, nil, nil, false},
		{"no purge block survives any flag", none, []string{"--purge-db"}, nil, false},
		{"flag mode survives a teardown that says nothing", flagOnly, nil, nil, false},
		{"flag mode purges when selected", flagOnly, []string{"--purge-db"}, nil, true},
		{"flag mode ignores another resource's flag", flagOnly, []string{"--purge-cache"}, nil, false},
		{"always purges with no flags at all", always, nil, nil, true},
		{"always keeps when the keep flag is passed", always, nil, []string{"--keep-userdata"}, false},
		{"always ignores another resource's keep flag", always, nil, []string{"--keep-vm"}, true},
		{"always purges when also selected", both, []string{"--purge-cache"}, nil, true},
		{"selecting beats keeping", both, []string{"--purge-cache"}, []string{"--keep-cache"}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Purges(tt.res, tt.purgeFlags, tt.keepFlags); got != tt.want {
				t.Errorf("Purges = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestPurgeOnTeardownDefault: a resource that declares nothing new is in
// flag mode, which is what keeps every repository adopted before this
// field behaving exactly as it did.
func TestPurgeOnTeardownDefault(t *testing.T) {
	for _, r := range []*Resource{
		{Type: "state-path", Name: "db"},
		{Type: "state-path", Name: "db", Purge: &Purge{Flag: "--purge-db"}},
	} {
		if got := PurgeOnTeardown(r); got != PurgeOnTeardownFlag {
			t.Errorf("PurgeOnTeardown = %q, want %q", got, PurgeOnTeardownFlag)
		}
	}
}

// TestPurgeAlwaysAcceptsATemplateIsolatedThroughAReference: the
// per-worktree rail follows the resources a template references, so a
// store named after an isolated namespace is accepted even though its own
// template spells no {slug}.
func TestPurgeAlwaysAcceptsATemplateIsolatedThroughAReference(t *testing.T) {
	doc := "version: 1\napp: x\n" +
		"resources:\n" +
		"  - type: namespace\n    name: ns\n    template: \"{app}-{slug}\"\n" +
		"  - type: state-path\n    name: userdata\n    template: \"{home}/stores/{ns}/\"\n" +
		"    purge:\n      on_teardown: always\n      keep_flag: \"--keep-userdata\"\n" +
		"emit:\n  descriptor:\n    filename: wt-env.json\n    format: json\n"
	s, err := Parse([]byte(doc))
	if err != nil {
		t.Fatalf("parsing: %v", err)
	}
	if err := Validate(s); err != nil {
		t.Fatalf("a store isolated through {ns} must validate: %v", err)
	}
}
