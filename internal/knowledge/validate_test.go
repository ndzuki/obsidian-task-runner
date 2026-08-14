package knowledge

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeRefFile(t *testing.T, dir, name, content string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestValidateRefFile(t *testing.T) {
	dir := t.TempDir()
	valid := `---
topics: [go, connect]
level: reference
updated: "2026-08-03"
source: "local"
verified: false
aliases: [Connect-Go]
---
# Title

> summary
`
	if err := ValidateRefFile(writeRefFile(t, dir, "valid.md", valid)); err != nil {
		t.Fatalf("valid doc rejected: %v", err)
	}

	cases := map[string]string{
		"empty-topics.md": `---
topics: []
level: reference
updated: "2026-08-03"
source: "local"
verified: false
aliases: []
---
# T
`,
		"bad-level.md": `---
topics: [go]
level: expert
updated: "2026-08-03"
source: "local"
verified: false
aliases: []
---
# T
`,
		"bad-date.md": `---
topics: [go]
level: reference
updated: "2026/08/03"
source: "local"
verified: false
aliases: []
---
# T
`,
		"empty-source.md": `---
topics: [go]
level: reference
updated: "2026-08-03"
source: ""
verified: false
aliases: []
---
# T
`,
		"missing-verified.md": `---
topics: [go]
level: reference
updated: "2026-08-03"
source: "local"
aliases: []
---
# T
`,
		"missing-aliases.md": `---
topics: [go]
level: reference
updated: "2026-08-03"
source: "local"
verified: false
---
# T
`,
		"no-frontmatter.md": `# T
`,
	}
	for name, content := range cases {
		if err := ValidateRefFile(writeRefFile(t, dir, name, content)); err == nil {
			t.Errorf("%s: invalid doc accepted", name)
		}
	}
	if err := ValidateRefFile(filepath.Join(dir, "missing.md")); err == nil {
		t.Error("missing file accepted")
	}
}

// TestNormalizeRefFile guards the intake self-heal: RFC3339 timestamps and
// an empty source (agent-interactive writes that skip the KB skill's checks)
// are rewritten in place to the KB v2 schema — updated/created become
// YYYY-MM-DD, source becomes "local" — while every other field keeps its
// exact value, order, and quoting. Already-valid documents are untouched;
// unfixable violations (bad level) are not rewritten so the alert path
// still fires.
func TestNormalizeRefFile(t *testing.T) {
	dir := t.TempDir()
	broken := `---
topics: [go, connect]
level: reference
updated: "2026-08-11T19:18:46+08:00"
source: ""
verified: true
aliases: [Connect-Go]
hits: 7
created: "2026-08-11T19:18:46+08:00"
---
# Title

> summary
`
	p := writeRefFile(t, dir, "broken.md", broken)
	fixed, err := NormalizeRefFile(p)
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	if !fixed {
		t.Fatal("broken document must be rewritten")
	}
	if err := ValidateRefFile(p); err != nil {
		t.Fatalf("still invalid after normalize: %v", err)
	}
	raw, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	got := string(raw)
	for _, want := range []string{
		`updated: "2026-08-11"`,
		`created: "2026-08-11"`,
		`source: "local"`,
		`hits: 7`,
		`aliases: [Connect-Go]`,
		`verified: true`,
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("normalized doc missing %q:\n%s", want, got)
		}
	}

	// Already-valid document: no rewrite.
	valid := `---
topics: [go, connect]
level: reference
updated: "2026-08-03"
source: "local"
verified: false
aliases: [Connect-Go]
---
# Title

> summary
`
	p2 := writeRefFile(t, dir, "valid.md", valid)
	fixed2, err := NormalizeRefFile(p2)
	if err != nil {
		t.Fatalf("normalize valid: %v", err)
	}
	if fixed2 {
		t.Fatal("valid document must not be rewritten")
	}
	raw2, _ := os.ReadFile(p2)
	if string(raw2) != valid {
		t.Fatal("valid document content changed")
	}

	// Unfixable violation (bad level) with fixable fields: the fixable
	// fields are normalized, but the document still fails validation —
	// the daemon keeps alerting after the partial fix.
	badLevel := `---
topics: [go]
level: expert
updated: "2026-08-11T19:18:46+08:00"
source: ""
verified: true
aliases: []
---
# Title

> summary
`
	p3 := writeRefFile(t, dir, "bad-level.md", badLevel)
	fixed3, err := NormalizeRefFile(p3)
	if err != nil {
		t.Fatalf("normalize bad-level: %v", err)
	}
	if !fixed3 {
		t.Fatal("fixable fields of bad-level doc must be rewritten")
	}
	if err := ValidateRefFile(p3); err == nil {
		t.Fatal("unfixable document must still fail validation")
	}

	// No fixable violation at all: untouched, still invalid (level).
	levelOnly := `---
topics: [go]
level: expert
updated: "2026-08-03"
source: "local"
verified: true
aliases: []
---
# Title

> summary
`
	p4 := writeRefFile(t, dir, "level-only.md", levelOnly)
	fixed4, err := NormalizeRefFile(p4)
	if err != nil {
		t.Fatalf("normalize level-only: %v", err)
	}
	if fixed4 {
		t.Fatal("doc without fixable violations must not be rewritten")
	}
	if err := ValidateRefFile(p4); err == nil {
		t.Fatal("level-only doc must still fail validation")
	}
}
