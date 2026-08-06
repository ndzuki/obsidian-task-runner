package knowledge

import (
	"os"
	"path/filepath"
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
