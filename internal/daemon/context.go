package daemon

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"github.com/ndzuki/obsidian-task-runner/pkg/yamlfrontmatter"
)



// contextTerm is a domain term parsed from CONTEXT.md's ## Language section.
type contextTerm struct {
	Name    string // e.g. "Operation"
	Def     string // one-sentence definition
	Avoids  string // _Avoid_ aliases (comma-joined)
	RawLine string // original CONTEXT.md line for reconstruction
}

// contextADR is a one-sentence summary of an architecture decision.
type contextADR struct {
	ID       string // e.g. "ADR-070-maintenance-cutover-authority-boundary"
	Title    string // human-readable title
	Decision string // one-sentence decision summary
}

// contextCache avoids re-reading CONTEXT.md for multiple tasks in the same project.
var contextCache sync.Map // projectVaultDir → string

// maxContextTokens is the soft ceiling for the injected context bundle.
const maxContextTokens = 300

// BuildProjectContext reads CONTEXT.md and relevant ADR files, extracts the
// most relevant domain terms and architecture decisions for the given REQ,
// and returns a formatted string suitable for injecting into the OMP prompt.
//
// Returns an empty string if no CONTEXT.md exists for the project.
func BuildProjectContext(projectVaultDir, reqPath string) string {
	// Cache hit: skip file IO for tasks in the same project.
	cached, ok := contextCache.Load(projectVaultDir)
	var content string
	if ok {
		content = cached.(string)
	} else {
		contextPath := filepath.Join(projectVaultDir, "Notes", "CONTEXT.md")
		data, err := os.ReadFile(contextPath)
		if err != nil {
			return ""
		}
		content = string(data)
		contextCache.Store(projectVaultDir, content)
	}

	constraints := extractSection(content, "Development Constraints")
	antiPatterns := extractSection(content, "Anti-patterns")
	terms := parseLanguageTerms(content)

	var parts []string
	if constraints != "" {
		parts = append(parts, "## Constraints\n"+compactConstraints(constraints))
	}
	// Anti-patterns: keep first sentence of each only.
	if antiPatterns != "" {
		parts = append(parts, "## Anti-patterns\n"+compactAntiPatterns(antiPatterns))
	}

	// Read the REQ document to extract keywords for relevance scoring.
	reqKeywords := extractReqKeywords(reqPath)

	// Read ADR files early — their count affects the term budget.
	adrDir := filepath.Join(projectVaultDir, "Notes", "adr")
	adrs := readADRs(adrDir)

	// Select domain terms, allocating slots based on remaining token budget.
	maxTerms := dynamicTermCount(parts, len(adrs))
	selectedTerms := selectTerms(terms, reqKeywords, maxTerms)
	if len(selectedTerms) > 0 {
		var sb strings.Builder
		sb.WriteString("## Domain Terms\n")
		for _, t := range selectedTerms {
			sb.WriteString(fmt.Sprintf("- **%s**: %s", t.Name, truncateDef(t.Def, 80)))
			if t.Avoids != "" {
				sb.WriteString(fmt.Sprintf(" _Avoid_: %s", t.Avoids))
			}
			sb.WriteString("\n")
		}
		parts = append(parts, strings.TrimSuffix(sb.String(), "\n"))
	}

	// Select the top-N most relevant ADR decisions.
	selectedADRs := selectADRs(adrs, reqKeywords, 2)
	if len(selectedADRs) > 0 {
		var sb strings.Builder
		sb.WriteString("## Architecture Decisions\n")
		for _, a := range selectedADRs {
			sb.WriteString(fmt.Sprintf("- **%s**: %s — %s\n", a.ID, a.Title, a.Decision))
		}
		parts = append(parts, strings.TrimSuffix(sb.String(), "\n"))
	}

	if len(parts) == 0 {
		return ""
	}

	return "[Project Context]\n" + strings.Join(parts, "\n\n") + "\n"
}

// ── CONTEXT.md parsing ────────────────────────────────────────────────────

// extractSection extracts a named ## section body from a CONTEXT.md document.
// Returns the section text without the heading, or empty string if not found.
func extractSection(content, heading string) string {
	idx := strings.Index(content, "## "+heading)
	if idx == -1 {
		return ""
	}
	body := content[idx+len("## "+heading):]

	// Trim to the next ## heading or end of document.
	next := strings.Index(body, "\n## ")
	if next != -1 {
		body = body[:next]
	}
	return strings.TrimSpace(body)
}

// termLineRE matches a CONTEXT.md Language entry: **Term**: definition...
var termLineRE = regexp.MustCompile(`^\*\*([^*]+)\*\*:\s*(.+)$`)

// avoidRe matches the _Avoid_ suffix on Language entries.
var avoidRe = regexp.MustCompile(`_Avoid_:\s*(.+?)(?:\n|$)`)

// parseLanguageTerms extracts domain terms from the ## Language section.
func parseLanguageTerms(content string) []contextTerm {
	section := extractSection(content, "Language")
	if section == "" {
		return nil
	}

	var terms []contextTerm
	for _, line := range strings.Split(section, "\n") {
		matches := termLineRE.FindStringSubmatch(strings.TrimSpace(line))
		if matches == nil {
			continue
		}
		name := strings.TrimSpace(matches[1])
		full := strings.TrimSpace(matches[2])

		// Separate definition from _Avoid_ suffix.
		def := full
		var avoids string
		if idx := strings.Index(full, "_Avoid_:"); idx != -1 {
			def = strings.TrimSpace(full[:idx])
			av := full[idx:]
			if m := avoidRe.FindStringSubmatch(av); m != nil {
				avoids = strings.TrimSpace(m[1])
			}
		}
		// Strip trailing _Avoid_ text from def if not already separated.
		if idx := strings.Index(def, "_Avoid_:"); idx != -1 {
			def = strings.TrimSpace(def[:idx])
		}

		terms = append(terms, contextTerm{
			Name:    name,
			Def:     def,
			Avoids:  avoids,
			RawLine: line,
		})
	}
	return terms
}

// ── REQ keyword extraction ────────────────────────────────────────────────

// keywordRe matches CamelCase, snake_case, and capitalized noun phrases.
var keywordRe = regexp.MustCompile(`\b([A-Z][a-zA-Z0-9]+(?:[A-Z][a-zA-Z0-9]+)*)\b`)

// commonWords are generic English words filtered from keyword extraction.
var commonWords = map[string]bool{
	"Given": true, "When": true, "Then": true, "And": true, "The": true,
	"This": true, "That": true, "With": true, "From": true, "Each": true,
	"Should": true, "Will": true, "Must": true, "All": true, "Any": true,
	"Not": true, "Use": true, "For": true, "Has": true, "Are": true,
	"After": true, "Before": true, "During": true, "New": true, "No": true,
	"Yes": true, "Only": true, "Also": true, "Both": true, "Current": true,
	"Note": true, "See": true, "Example": true, "Step": true, "Phase": true,
	"Round": true, "TASK": true, "REQ": true, "AC": true, "Makefile": true,
	"GitHub": true, "Actions": true, "Go": true, "SDK": true, "Helm": true,
	"Install": true, "Test": true, "Pass": true, "Fail": true, "Error": true,
}

// extractReqKeywords reads the REQ document and extracts CamelCase and
// capitalized technical terms for relevance matching.
func extractReqKeywords(reqPath string) []string {
	data, err := os.ReadFile(reqPath)
	if err != nil {
		return nil
	}
	// Strip frontmatter — keywords from markdown body only.
	content := string(data)
	if idx := strings.Index(content, "---"); idx >= 0 {
		rest := content[idx+3:]
		if end := strings.Index(rest, "\n---"); end >= 0 {
			content = rest[end+4:]
		}
	}

	seen := make(map[string]bool)
	var keywords []string
	for _, match := range keywordRe.FindAllString(content, -1) {
		if commonWords[match] || len(match) < 3 {
			continue
		}
		if !seen[match] {
			seen[match] = true
			keywords = append(keywords, match)
		}
	}
	return keywords
}

// ── Term relevance scoring ────────────────────────────────────────────────

type scoredTerm struct {
	term  contextTerm
	score int
}

// selectTerms scores domain terms against REQ keywords and returns the top N.
func selectTerms(terms []contextTerm, keywords []string, maxN int) []contextTerm {
	if len(terms) == 0 || len(keywords) == 0 {
		// No keywords to match — return first maxN terms as fallback.
		if len(terms) > maxN {
			return terms[:maxN]
		}
		return terms
	}

	kwSet := make(map[string]bool, len(keywords))
	for _, k := range keywords {
		kwSet[strings.ToLower(k)] = true
	}

	var scored []scoredTerm
	for _, t := range terms {
		s := scoreTerm(t, kwSet)
		if s > 0 {
			scored = append(scored, scoredTerm{t, s})
		}
	}

	sort.Slice(scored, func(i, j int) bool {
		return scored[i].score > scored[j].score
	})

	result := make([]contextTerm, 0, maxN)
	for i := 0; i < len(scored) && i < maxN; i++ {
		result = append(result, scored[i].term)
	}
	// Fallback: when no keywords match any term, return the most fundamental
	// domain entities (first N terms in the Language section are architectural
	// building blocks every task should know).
	if len(result) == 0 && len(terms) > 0 {
		n := maxN
		if n > len(terms) {
			n = len(terms)
		}
		result = terms[:n]
	}
	return result
}

// scoreTerm computes a relevance score for a domain term against keyword set.
func scoreTerm(t contextTerm, kwSet map[string]bool) int {
	score := 0
	lowerName := strings.ToLower(t.Name)

	// Exact term name match in keywords.
	if kwSet[lowerName] {
		score += 3
	}
	// Term name appears as a whole word in a keyword, or vice versa.
	for kw := range kwSet {
		if wholeWordMatch(lowerName, kw) || wholeWordMatch(kw, lowerName) {
			score++
			break
		}
	}
	// Avoid_ aliases match keywords (individual words).
	for _, a := range strings.Split(t.Avoids, "、") {
		a = strings.TrimSpace(a)
		if a == "" {
			continue
		}
		for _, w := range strings.Fields(strings.ToLower(a)) {
			if kwSet[w] {
				score += 2
				break
			}
		}
	}

	return score
}

// ── ADR loading and matching ──────────────────────────────────────────────

// readADRs scans the Notes/adr/ directory and returns parsed ADR summaries.
func readADRs(adrDir string) []contextADR {
	entries, err := os.ReadDir(adrDir)
	if err != nil {
		return nil
	}

	var adrs []contextADR
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(adrDir, e.Name()))
		if err != nil {
			continue
		}
		fm, err := yamlfrontmatter.Parse(data)
		if err != nil || fm == nil {
			continue
		}
		// Extract one-sentence decision from the ## Decision section.
		decision := extractADRDecision(string(data))
		adrID := ""
		if fm.Extra != nil {
			if v, ok := fm.Extra["adr_id"]; ok {
				adrID = fmt.Sprint(v)
			}
		}
		title := fm.Title
		if title == "" && fm.Extra != nil {
			if v, ok := fm.Extra["title"]; ok {
				title = fmt.Sprint(v)
			}
		}
		adrs = append(adrs, contextADR{
			ID:       adrID,
			Title:    title,
			Decision: decision,
		})
	}
	return adrs
}

// extractADRDecision extracts the first sentence of the ## Decision section.
func extractADRDecision(content string) string {
	idx := strings.Index(content, "## Decision")
	if idx == -1 {
		return ""
	}
	body := content[idx+len("## Decision"):]

	// Find the first non-empty, non-heading line.
	lines := strings.Split(body, "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		// Take first sentence.
		if dot := strings.Index(line, "."); dot > 0 {
			return line[:dot+1]
		}
		return line
	}
	return ""
}

// selectADRs scores ADRs against REQ keywords and returns the top N.
func selectADRs(adrs []contextADR, keywords []string, maxN int) []contextADR {
	if len(adrs) == 0 || len(keywords) == 0 {
		// No keywords means no filtering — we still return ADRs for awareness.
		// But limit to maxN to stay within token budget.
		if len(adrs) > maxN {
			return adrs[:maxN]
		}
		return adrs
	}

	kwSet := make(map[string]bool, len(keywords))
	for _, k := range keywords {
		kwSet[strings.ToLower(k)] = true
	}

	type scoredADR struct {
		adr   contextADR
		score int
	}
	var scored []scoredADR
	for _, a := range adrs {
		s := 0
		text := strings.ToLower(a.Title + " " + a.Decision)
		for kw := range kwSet {
			if strings.Contains(text, kw) {
				s++
			}
		}
		scored = append(scored, scoredADR{a, s})
	}

	sort.Slice(scored, func(i, j int) bool {
		return scored[i].score > scored[j].score
	})

	result := make([]contextADR, 0, maxN)
	for i := 0; i < len(scored) && i < maxN; i++ {
		if scored[i].score > 0 {
			result = append(result, scored[i].adr)
		}
	}
	return result
}

// dynamicTermCount returns the optimal number of domain terms based on
// remaining byte budget after constraints and anti-patterns are accounted for.
// Each term costs ~100 bytes; ADRs consume ~200 bytes each when present.
func dynamicTermCount(parts []string, adrCount int) int {
	baseBytes := 0
	for _, p := range parts {
		baseBytes += len(p)
	}
	// Rough budget: 750 bytes total, minus base sections, minus ADR allocation.
	budget := 750 - baseBytes - adrCount*200
	if budget < 100 {
		return 1 // at least one term for orientation
	}
	n := budget / 100
	if n > 6 {
		return 6
	}
	return n
}
// compactConstraints extracts the core prohibition from each constraint line.
func compactConstraints(raw string) string {
	var lines []string
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "- ")
		line = strings.TrimPrefix(line, "* ")
		// Keep only the core directive (up to first period or 100 chars).
		line = truncateDef(line, 100)
		lines = append(lines, "- "+line)
	}
	return strings.Join(lines, "\n")
}

// compactAntiPatterns keeps only the anti-pattern description, stripping
// the "correct approach" suffix.
func compactAntiPatterns(raw string) string {
	var lines []string
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "- ")
		line = strings.TrimPrefix(line, "* ")
		// Truncate to first sentence end (。or .) or 100 chars max.
		line = truncateDef(line, 100)
		lines = append(lines, "- "+line)
	}
	return strings.Join(lines, "\n")
}

// truncateDef truncates a definition to maxLen characters, breaking at the
// first sentence boundary (period or Chinese period) within the limit.
func truncateDef(def string, maxLen int) string {
	if len(def) <= maxLen {
		return def
	}
	candidates := []string{". ", "。", ".  "}
	for _, sep := range candidates {
		if idx := strings.Index(def[:maxLen], sep); idx > 10 {
			return def[:idx+len(sep)]
		}
	}
	return def[:maxLen]
}




// wholeWordMatch returns true if the keyword appears as a whole word in text.
// A "word" boundary is defined by non-alphanumeric characters. Keywords and
// domain terms are English CamelCase identifiers — byte indexing is safe.
func wholeWordMatch(text, keyword string) bool {
	lower := strings.ToLower(text)
	kw := strings.ToLower(keyword)
	kwLen := len(kw)

	for _, idx := range findAllIndexes(lower, kw) {
		before := idx == 0 || !isWordChar(lower[idx-1])
		after := idx+kwLen >= len(lower) || !isWordChar(lower[idx+kwLen])
		if before && after {
			return true
		}
	}
	return false
}

func isWordChar(c byte) bool {
	return (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '_'
}

func findAllIndexes(s, sub string) []int {
	var idxs []int
	subLen := len(sub)
	for i := 0; i <= len(s)-subLen; i++ {
		if s[i:i+subLen] == sub {
			idxs = append(idxs, i)
			i += subLen - 1
		}
	}
	return idxs
}
