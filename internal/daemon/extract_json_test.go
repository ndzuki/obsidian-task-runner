package daemon

import (
	"strings"
	"testing"
)

func TestExtractJSON(t *testing.T) {
	tests := []struct {
		name   string
		input  string
		want   string
		wantOK bool
	}{
		{
			name:   "fenced json block",
			input:  "```json\n{\"a\": 1}\n```\n",
			want:   `{"a": 1}`,
			wantOK: true,
		},
		{
			name:   "fenced with prose before",
			input:  "结果如下：\n```json\n{\"priority\": \"P1\"}\n```\n完成",
			want:   `{"priority": "P1"}`,
			wantOK: true,
		},
		{
			name:   "balanced braces with nested string braces",
			input:  `前缀 {"a": "x{y}z", "b": [1,2]} 后缀`,
			want:   `{"a": "x{y}z", "b": [1,2]}`,
			wantOK: true,
		},
		{
			name:   "whole text is json",
			input:  `{"impact": "high"}`,
			want:   `{"impact": "high"}`,
			wantOK: true,
		},
		{
			name:   "no json",
			input:  "plain text without braces",
			want:   "",
			wantOK: false,
		},
		{
			name:   "unbalanced braces",
			input:  "text {\"a\": 1",
			want:   "",
			wantOK: false,
		},
		{
			name:   "empty",
			input:  "",
			want:   "",
			wantOK: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := extractJSON(tt.input)
			if tt.wantOK {
				if err != nil {
					t.Fatalf("extractJSON(%q) err=%v, want success", tt.input, err)
				}
				if strings.TrimSpace(string(got)) != tt.want {
					t.Fatalf("extractJSON(%q) = %q, want %q", tt.input, got, tt.want)
				}
			} else {
				if err == nil {
					t.Fatalf("extractJSON(%q) = %q, want error", tt.input, got)
				}
			}
		})
	}
}

func TestExtractJSONEscapedQuotesInString(t *testing.T) {
	input := `{"reason": "he said \"hi\"", "ok": true}`
	got, err := extractJSON(input)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != input {
		t.Fatalf("escaped quote handling wrong: %q", got)
	}
}
