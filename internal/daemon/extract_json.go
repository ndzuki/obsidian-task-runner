package daemon

import (
	"fmt"
	"strings"
)

// extractJSON recovers a JSON object from DSH headless free-text stdout. DSH
// has no strict-JSON output mode, so a skill's "output one JSON
// object" contract typically arrives as a ```json fenced block or prose-
// wrapped object. It tries, in order: (1) a ```json fenced block, (2) the
// first balanced {...} span, (3) the whole trimmed text when it already starts
// with '{'. Returns an error when no JSON object can be isolated.
func extractJSON(text string) ([]byte, error) {
	trimmed := strings.TrimSpace(text)

	// 1. ```json fenced block (```json ... ```).
	if idx := strings.Index(trimmed, "```json"); idx >= 0 {
		rest := trimmed[idx+len("```json"):]
		if end := strings.Index(rest, "```"); end >= 0 {
			return []byte(strings.TrimSpace(rest[:end])), nil
		}
		// Unterminated fence: fall through to the balanced-brace scan.
		trimmed = rest
	}

	// 2. First balanced {...} span.
	if start := strings.IndexByte(trimmed, '{'); start >= 0 {
		depth := 0
		inString := false
		escaped := false
		for i := start; i < len(trimmed); i++ {
			c := trimmed[i]
			if inString {
				if escaped {
					escaped = false
					continue
				}
				if c == '\\' {
					escaped = true
					continue
				}
				if c == '"' {
					inString = false
				}
				continue
			}
			switch c {
			case '"':
				inString = true
			case '{':
				depth++
			case '}':
				depth--
				if depth == 0 {
					return []byte(trimmed[start : i+1]), nil
				}
			}
		}
		return nil, fmt.Errorf("extractJSON: unbalanced JSON object in output")
	}

	// 3. Whole trimmed text already starts with '{'.
	return nil, fmt.Errorf("extractJSON: no JSON object found in output")
}
