package knowledge

import (
	"os"
	"path/filepath"
	"sort"
)

// Gap is an architecture decision with no matching knowledge-base document —
// its technical choices are not retrievable by future agents.
type Gap struct {
	ADR     string // ADR file name, e.g. "ADR-008-ordered-fail-closed-preflight.md"
	Title   string // ADR title from frontmatter
	Matched string // empty = no match; set when the ADR matched only weakly
}

// ScanKnowledgeGaps reports every accepted/superseded ADR of a project whose
// decision has no knowledge-base target (classifyADR returned nothing).
// These are the "知识缺口" of the Step -1 project knowledge graph, now
// automated: the user can extract the missing knowledge or accept the gap.
func ScanKnowledgeGaps(vaultDir, projectName string) ([]Gap, error) {
	adrDir := filepath.Join(vaultDir, "Projects", projectName, "Notes", "adr")
	adrs, err := scanADRs(adrDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	refsDir := filepath.Join(vaultDir, "References")
	var gaps []Gap
	for _, adr := range adrs {
		if target := classifyADR(adr, refsDir); target == "" {
			gaps = append(gaps, Gap{ADR: filepath.Base(adr.FilePath), Title: adr.Title})
		}
	}
	sort.Slice(gaps, func(i, j int) bool { return gaps[i].ADR < gaps[j].ADR })
	return gaps, nil
}
