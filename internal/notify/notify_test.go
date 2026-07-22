package notify

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestKittyTabExists(t *testing.T) {
	tests := []struct {
		name     string
		output   []byte
		taskID   string
		tabTitle string
		want     bool
		wantErr  bool
	}{
		{
			name: "unicode tab title",
			output: kittyLSOutput(t, []kittyOSWindow{
				{
					Tabs: []kittyTab{
						{Title: "Grilling 066 — 分阶段端到端测试"},
					},
				},
			}),
			taskID:   "066",
			tabTitle: "Grilling 066 — 分阶段端到端测试",
			want:     true,
		},
		{
			name: "raw escaped unicode title",
			output: []byte(`[
				{
					"tabs": [
						{
							"title": "Grilling 066 \u2014 \u5206\u9636\u6bb5\u7aef\u5230\u7aef\u6d4b\u8bd5",
							"windows": []
						}
					]
				}
			]`),
			taskID:   "066",
			tabTitle: "Grilling 066 — 分阶段端到端测试",
			want:     true,
		},
		{
			name: "unicode window title",
			output: kittyLSOutput(t, []kittyOSWindow{
				{
					Tabs: []kittyTab{
						{
							Title: "shell",
							Windows: []kittyWindow{
								{Title: "Grilling 068 — ValuesRevision 审批工作流"},
							},
						},
					},
				},
			}),
			taskID:   "068",
			tabTitle: "Grilling 068 — ValuesRevision 审批工作流",
			want:     true,
		},
		{
			name: "different task",
			output: kittyLSOutput(t, []kittyOSWindow{
				{
					Tabs: []kittyTab{
						{Title: "Grilling 068 — ValuesRevision 审批工作流"},
					},
				},
			}),
			taskID:   "066",
			tabTitle: "Grilling 066 — 分阶段端到端测试",
			want:     false,
		},
		{
			name: "same task with changed title",
			output: kittyLSOutput(t, []kittyOSWindow{
				{
					Tabs: []kittyTab{
						{Title: "Grilling 066 — old title"},
					},
				},
			}),
			taskID:   "066",
			tabTitle: "Grilling 066 — new title",
			want:     true,
		},
		{
			name:     "invalid JSON",
			output:   []byte("not-json"),
			taskID:   "066",
			tabTitle: "Grilling 066 — 分阶段端到端测试",
			wantErr:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := kittyTabExists(tt.output, tt.taskID, tt.tabTitle)
			if (err != nil) != tt.wantErr {
				t.Fatalf("kittyTabExists() error = %v, wantErr %v", err, tt.wantErr)
			}
			if got != tt.want {
				t.Fatalf("kittyTabExists() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestShouldLaunchKittyTab_FailsClosed(t *testing.T) {
	shouldLaunch, err := shouldLaunchKittyTab(
		[]byte("not-json"),
		"066",
		"Grilling 066 — 分阶段端到端测试",
	)
	if err == nil {
		t.Fatal("shouldLaunchKittyTab() error = nil, want parse error")
	}
	if shouldLaunch {
		t.Fatal("shouldLaunchKittyTab() = true, want false when existing tabs cannot be inspected")
	}
}

func TestTryKittyTab_ParseFailureDoesNotLaunch(t *testing.T) {
	binDir := t.TempDir()
	marker := filepath.Join(t.TempDir(), "launch-called")
	kittyPath := filepath.Join(binDir, "kitty")
	script := `#!/bin/sh
if [ "$2" = "ls" ]; then
	printf 'not-json'
	exit 0
fi
if [ "$2" = "launch" ]; then
	: > "$KITTY_LAUNCH_MARKER"
	exit 0
fi
exit 1
`
	if err := os.WriteFile(kittyPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake kitty: %v", err)
	}

	t.Setenv("PATH", binDir)
	t.Setenv("TMPDIR", t.TempDir())
	t.Setenv("USER", "notify-parse-failure-test")
	t.Setenv("KITTY_LISTEN_ON", "unix:test")
	t.Setenv("KITTY_LAUNCH_MARKER", marker)

	if handled := tryKittyTab("066", "分阶段端到端测试", "", ""); handled {
		t.Fatal("tryKittyTab() = true, want false so desktop fallback remains available")
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("kitty launch marker exists or stat failed unexpectedly: %v", err)
	}
}

func kittyLSOutput(t *testing.T, osWindows []kittyOSWindow) []byte {
	t.Helper()
	output, err := json.Marshal(osWindows)
	if err != nil {
		t.Fatalf("marshal kitty output: %v", err)
	}
	return output
}
