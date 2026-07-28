package daemon

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"
)

type phaseEvent struct {
	Timestamp    time.Time `json:"-"`
	VaultID      string
	Project      string
	TaskPathHash string
	TaskID       string
	Phase        string
	Event        string
	Model        string
	PID          int
	DurationS    float64
	ErrorCode    ErrorCode
	Message      string
}

type phaseLogger struct {
	writer io.Writer
}

func newPhaseLogger(writer io.Writer) *phaseLogger {
	return &phaseLogger{writer: writer}
}

func (logger *phaseLogger) Event(event phaseEvent) {
	if logger == nil || logger.writer == nil {
		return
	}
	when := event.Timestamp
	if when.IsZero() {
		when = time.Now()
	}
	payload := map[string]interface{}{
		"ts":             when.Format(time.RFC3339Nano),
		"vault_id":       event.VaultID,
		"project":        event.Project,
		"task_path_hash": event.TaskPathHash,
		"task_id":        event.TaskID,
		"phase":          event.Phase,
		"event":          event.Event,
		"model":          event.Model,
		"pid":            event.PID,
		"duration_s":     event.DurationS,
		"error_code":     string(event.ErrorCode),
		"message":        redactSecrets(event.Message),
	}
	_ = json.NewEncoder(logger.writer).Encode(payload)
}

var secretPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)(authorization\s*[:=]\s*(?:bearer\s+)?)[^\s]+`),
	regexp.MustCompile(`(?i)((?:api[_-]?key|token|credential)\s*[:=]\s*)[^\s]+`),
}

func redactSecrets(message string) string {
	for _, pattern := range secretPatterns {
		message = pattern.ReplaceAllString(message, `${1}<redacted>`)
	}
	return message
}

type auditRecord struct {
	Actor     string
	Event     string
	From      string
	To        string
	Plan      string
	ErrorCode string
	Hash      string
	Timestamp time.Time
}

func AppendAuditRecord(path string, record auditRecord) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read TASK audit log: %w", err)
	}
	content := string(data)
	marker := "## 变更记录"
	index := strings.Index(content, marker)
	if index < 0 {
		if !strings.HasSuffix(content, "\n") {
			content += "\n"
		}
		content += "\n" + marker + "\n"
		index = strings.Index(content, marker)
	}
	section := content[index+len(marker):]
	next := strings.Index(section, "\n## ")
	insertAt := len(content)
	if next >= 0 {
		insertAt = index + len(marker) + next
	}
	sequence := auditSequence(section)
	when := record.Timestamp
	if when.IsZero() {
		when = time.Now()
	}
	line := fmt.Sprintf("%d. `%s` — actor=%s event=%s from=%s to=%s plan=%s error_code=%s hash=%s\n",
		sequence, when.Format(time.RFC3339), valueOrNone(record.Actor), valueOrNone(record.Event), valueOrNone(record.From),
		valueOrNone(record.To), valueOrNone(record.Plan), valueOrNone(record.ErrorCode), valueOrNone(record.Hash))
	if insertAt > 0 && content[insertAt-1] != '\n' {
		line = "\n" + line
	}
	updated := content[:insertAt] + line + content[insertAt:]
	return os.WriteFile(path, []byte(updated), 0o644)
}

func auditSequence(section string) int {
	scanner := bufio.NewScanner(strings.NewReader(section))
	maxSequence := 0
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		dot := strings.IndexByte(line, '.')
		if dot <= 0 {
			continue
		}
		value, err := strconv.Atoi(line[:dot])
		if err == nil && value > maxSequence {
			maxSequence = value
		}
	}
	return maxSequence + 1
}

func valueOrNone(value string) string {
	if strings.TrimSpace(value) == "" {
		return "<none>"
	}
	return strings.ReplaceAll(strings.TrimSpace(value), " ", "_")
}
