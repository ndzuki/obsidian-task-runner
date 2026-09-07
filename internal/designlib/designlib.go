// Package designlib implements the persistent one-shot global design library
// (docs/archive/refactor-architecture.md §3.3): Projects/{project}/Design/ holding
// contracts/, decisions/, waves/ and glossary.md. It is the single source of
// truth for interface contracts, ADRs, delivery waves and domain vocabulary;
// per-task sessions read only their relevant slice instead of re-deriving
// global understanding (the release-manager lesson: per-task grilling
// exploded cost, one-shot global design + persistent library + batch
// execution keeps each task cheap).
package designlib

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/ndzuki/obsidian-task-runner/pkg/yamlfrontmatter"
	"gopkg.in/yaml.v3"
)

const (
	// DirName is the design library directory inside a project directory.
	DirName = "Design"
	// ContractsDir holds interface/contract documents (single source of truth).
	ContractsDir = "contracts"
	// DecisionsDir holds ADRs (with status/superseded relations).
	DecisionsDir = "decisions"
	// WavesDir holds the delivery-wave plan and dependency graph.
	WavesDir = "waves"
	// GlossaryFile is the shared domain vocabulary.
	GlossaryFile = "glossary.md"
	// RevisionFile records the library revision (bumped per design session).
	RevisionFile = "REVISION.md"
)

// ErrEmpty reports a design library that exists but has no artifacts yet —
// the caller should schedule a global design session.
var ErrEmpty = errors.New("design library empty: run a global design session first")

// Layout is one project's on-disk design library.
type Layout struct {
	// Root is the absolute design library directory
	// (Projects/{project}/Design).
	Root string
}

// ForProject returns the design library layout for a project directory
// without touching the filesystem.
func ForProject(projectDir string) *Layout {
	return &Layout{Root: filepath.Join(projectDir, DirName)}
}

// Ensure idempotently creates the design library skeleton (directories +
// glossary placeholder) and returns its layout.
func Ensure(projectDir string) (*Layout, error) {
	l := ForProject(projectDir)
	for _, d := range []string{ContractsDir, DecisionsDir, WavesDir} {
		if err := os.MkdirAll(filepath.Join(l.Root, d), 0o755); err != nil {
			return nil, fmt.Errorf("designlib: create %s: %w", d, err)
		}
	}
	if _, err := os.Stat(l.GlossaryPath()); errors.Is(err, os.ErrNotExist) {
		if err := os.WriteFile(l.GlossaryPath(), []byte(defaultGlossary), 0o644); err != nil {
			return nil, fmt.Errorf("designlib: write glossary: %w", err)
		}
	}
	return l, nil
}

const defaultGlossary = `---
schema: glossary-v1
---

# 领域词汇表

> 全局设计会话填充：为每个关键领域术语建立统一定义，任务会话不得重复解释。

| 术语 | 定义 | 出处 |
| --- | --- | --- |
|  |  |  |
`

// ContractsPath / DecisionsPath / WavesPath / GlossaryPath / RevisionPath
// return the canonical child paths of the library.
func (l *Layout) ContractsPath() string { return filepath.Join(l.Root, ContractsDir) }
func (l *Layout) DecisionsPath() string { return filepath.Join(l.Root, DecisionsDir) }
func (l *Layout) WavesPath() string     { return filepath.Join(l.Root, WavesDir) }
func (l *Layout) GlossaryPath() string  { return filepath.Join(l.Root, GlossaryFile) }
func (l *Layout) RevisionPath() string  { return filepath.Join(l.Root, RevisionFile) }

// Revision is the design library version metadata persisted in REVISION.md.
type Revision struct {
	Number    int    `yaml:"revision"`
	UpdatedAt string `yaml:"updated_at"`
	SessionID string `yaml:"session_id"`
}

// ReadRevision reads the current revision. A library with no REVISION.md yet
// yields Revision{Number: 0} with nil error.
func (l *Layout) ReadRevision() (Revision, error) {
	data, err := os.ReadFile(l.RevisionPath())
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Revision{}, nil
		}
		return Revision{}, fmt.Errorf("designlib: read revision: %w", err)
	}
	var rev Revision
	if err := yaml.Unmarshal(data, &rev); err != nil {
		return Revision{}, fmt.Errorf("designlib: parse revision: %w", err)
	}
	return rev, nil
}

// BumpRevision increments the library revision under a per-library flock and
// records the design session id (the durable identity of the global design
// session that produced the new artifacts). Returns the new revision number.
func (l *Layout) BumpRevision(sessionID string) (int, error) {
	unlock, err := acquireLock(l.RevisionPath())
	if err != nil {
		return 0, err
	}
	defer unlock()

	rev, err := l.ReadRevision()
	if err != nil {
		return 0, err
	}
	rev.Number++
	rev.UpdatedAt = time.Now().Format(time.RFC3339)
	rev.SessionID = sessionID
	body, err := yaml.Marshal(rev)
	if err != nil {
		return 0, fmt.Errorf("designlib: marshal revision: %w", err)
	}
	content := "---\n" + string(body) + "---\n"
	if err := yamlfrontmatter.AtomicWrite(l.RevisionPath(), []byte(content)); err != nil {
		return 0, fmt.Errorf("designlib: write revision: %w", err)
	}
	return rev.Number, nil
}

// Summary is the lightweight library inventory (no bodies loaded).
type Summary struct {
	Revision    int
	Contracts   []string // filenames, sorted
	Decisions   []string // filenames, sorted
	Waves       []string // filenames, sorted
	HasGlossary bool
}

// Validate verifies the global design-session output contract. A complete
// library must contain a glossary plus at least one contract, decision and
// wave document; every artifact carries a versioned schema and stable id/title
// so later task sessions consume explicit contracts instead of loose prose.
func (l *Layout) Validate() error {
	type artifactSpec struct {
		dir           string
		schema        string
		requireStatus bool
	}
	specs := []artifactSpec{
		{dir: ContractsDir, schema: "contract-v1"},
		{dir: DecisionsDir, schema: "decision-v1", requireStatus: true},
		{dir: WavesDir, schema: "wave-v1"},
	}

	var problems []string
	if err := validateArtifact(l.GlossaryPath(), "glossary-v1", false); err != nil {
		problems = append(problems, err.Error())
	} else if isPlaceholderGlossary(l.GlossaryPath()) {
		// The skill contract requires "a useful domain vocabulary (not the
		// placeholder table)". Ensure() seeds a placeholder glossary before a
		// design session; a session that leaves it untouched must not pass —
		// otherwise the "global design produced" verdict is a lie.
		problems = append(problems, fmt.Sprintf("%s: glossary is still the default placeholder (no domain vocabulary)", l.GlossaryPath()))
	}
	for _, spec := range specs {
		files, err := mdFiles(filepath.Join(l.Root, spec.dir))
		if err != nil {
			problems = append(problems, err.Error())
			continue
		}
		if len(files) == 0 {
			problems = append(problems, fmt.Sprintf("%s: no artifacts", spec.dir))
			continue
		}
		ids := make(map[string]string, len(files))
		for _, name := range files {
			path := filepath.Join(l.Root, spec.dir, name)
			fm, err := readArtifactFrontmatter(path)
			if err != nil {
				problems = append(problems, err.Error())
				continue
			}
			if fm.Schema != spec.schema {
				problems = append(problems, fmt.Sprintf("%s: schema=%q, want %q", path, fm.Schema, spec.schema))
			}
			if fm.ID == "" || fm.Title == "" {
				problems = append(problems, fmt.Sprintf("%s: id and title are required", path))
			}
			if spec.requireStatus && fm.Status == "" {
				problems = append(problems, fmt.Sprintf("%s: status is required", path))
			}
			if previous, exists := ids[fm.ID]; fm.ID != "" && exists {
				problems = append(problems, fmt.Sprintf("%s: duplicate id %q (already used by %s)", path, fm.ID, previous))
			} else if fm.ID != "" {
				ids[fm.ID] = name
			}
		}
	}
	if len(problems) > 0 {
		return fmt.Errorf("designlib: invalid library: %s", strings.Join(problems, "; "))
	}
	return nil
}

type artifactFrontmatter struct {
	Schema string `yaml:"schema"`
	ID     string `yaml:"id"`
	Title  string `yaml:"title"`
	Status string `yaml:"status"`
}

// isPlaceholderGlossary reports whether the glossary file at path still holds
// the default placeholder body (Ensure()'s seed), i.e. no domain vocabulary
// was written by a design session. Mirrors the HasGlossary comparison in
// ReadSummary so Validate and ReadSummary agree on what "empty glossary" means.
func isPlaceholderGlossary(path string) bool {
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	return bytes.Equal(bytes.TrimSpace(data), bytes.TrimSpace([]byte(defaultGlossary)))
}

func validateArtifact(path, schema string, requireStatus bool) error {
	fm, err := readArtifactFrontmatter(path)
	if err != nil {
		return err
	}
	if fm.Schema != schema {
		return fmt.Errorf("%s: schema=%q, want %q", path, fm.Schema, schema)
	}
	if requireStatus && fm.Status == "" {
		return fmt.Errorf("%s: status is required", path)
	}
	return nil
}

func readArtifactFrontmatter(path string) (artifactFrontmatter, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return artifactFrontmatter{}, fmt.Errorf("%s: read: %w", path, err)
	}
	lines := strings.Split(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n")
	if len(lines) < 3 || strings.TrimSpace(lines[0]) != "---" {
		return artifactFrontmatter{}, fmt.Errorf("%s: missing frontmatter", path)
	}
	end := -1
	for i := 1; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) == "---" {
			end = i
			break
		}
	}
	if end < 0 {
		return artifactFrontmatter{}, fmt.Errorf("%s: unclosed frontmatter", path)
	}
	var fm artifactFrontmatter
	if err := yaml.Unmarshal([]byte(strings.Join(lines[1:end], "\n")), &fm); err != nil {
		return artifactFrontmatter{}, fmt.Errorf("%s: parse frontmatter: %w", path, err)
	}
	return fm, nil
}

// ReadSummary scans the library and returns its inventory. An empty library
// returns ErrEmpty so callers can route to a global design session.
func (l *Layout) ReadSummary() (*Summary, error) {
	rev, err := l.ReadRevision()
	if err != nil {
		return nil, err
	}
	s := &Summary{Revision: rev.Number}
	var errs []string
	s.Contracts, err = mdFiles(l.ContractsPath())
	if err != nil {
		errs = append(errs, err.Error())
	}
	s.Decisions, err = mdFiles(l.DecisionsPath())
	if err != nil {
		errs = append(errs, err.Error())
	}
	s.Waves, err = mdFiles(l.WavesPath())
	if err != nil {
		errs = append(errs, err.Error())
	}
	if data, readErr := os.ReadFile(l.GlossaryPath()); readErr == nil {
		// Ensure creates a placeholder glossary, but that placeholder is not a
		// design artifact. Treat it as empty until the global design session
		// replaces it with project vocabulary.
		s.HasGlossary = !bytes.Equal(bytes.TrimSpace(data), bytes.TrimSpace([]byte(defaultGlossary)))
	} else if !errors.Is(readErr, os.ErrNotExist) {
		errs = append(errs, fmt.Sprintf("read %s: %v", l.GlossaryPath(), readErr))
	}
	if len(errs) > 0 {
		return nil, fmt.Errorf("designlib: %s", strings.Join(errs, "; "))
	}
	if len(s.Contracts) == 0 && len(s.Decisions) == 0 && len(s.Waves) == 0 && !s.HasGlossary {
		return nil, ErrEmpty
	}
	return s, nil
}

func mdFiles(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("read %s: %w", dir, err)
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".md") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	return names, nil
}

// SliceForTask returns the design-library slice relevant to one task: the
// glossary, every wave document, plus contracts/decisions whose filename
// tokens overlap the requirement summary (or whose frontmatter `related`
// field names this task). The total body is capped at maxBytes so a session
// reads only its slice, never the whole library.
//
// Returns ErrEmpty when the library has no artifacts yet.
func (l *Layout) SliceForTask(taskID, reqSummary string, maxBytes int) (string, error) {
	summary, err := l.ReadSummary()
	if err != nil {
		return "", err
	}
	if maxBytes <= 0 {
		maxBytes = 64 << 10 // 64 KiB default cap
	}
	var b strings.Builder
	remaining := maxBytes
	write := func(header, content string) bool {
		if len(content) > remaining {
			content = content[:remaining] + "\n…(截断)\n"
		}
		if len(content) == 0 {
			return false
		}
		if b.Len() > 0 {
			b.WriteString("\n")
		}
		b.WriteString(header)
		b.WriteString(content)
		remaining -= len(content)
		return remaining > 0
	}

	if summary.HasGlossary {
		if data, rerr := os.ReadFile(l.GlossaryPath()); rerr == nil {
			write("## 领域词汇表（glossary）\n\n", string(data))
		}
	}
	for _, w := range summary.Waves {
		if remaining <= 0 {
			break
		}
		if data, rerr := os.ReadFile(filepath.Join(l.WavesPath(), w)); rerr == nil {
			write(fmt.Sprintf("## 交付波次（%s）\n\n", w), string(data))
		}
	}
	related := func(rel string) bool {
		norm := func(id string) string { return strings.TrimPrefix(id, "TASK-") }
		for _, tok := range strings.FieldsFunc(rel, func(r rune) bool { return r == ',' || r == ' ' }) {
			tok = strings.TrimSpace(tok)
			if tok != "" && norm(tok) == norm(taskID) {
				return true
			}
		}
		return false
	}
	overlap := func(name, summary string) bool {
		tokens := strings.FieldsFunc(strings.TrimSuffix(name, ".md"),
			func(r rune) bool { return r == '-' || r == '_' || r == '.' })
		low := strings.ToLower(summary)
		for _, t := range tokens {
			if t == "" || len(t) < 3 {
				continue
			}
			if strings.Contains(low, strings.ToLower(t)) {
				return true
			}
		}
		return false
	}
	for dir, files := range map[string][]string{ContractsDir: summary.Contracts, DecisionsDir: summary.Decisions} {
		for _, f := range files {
			if remaining <= 0 {
				break
			}
			path := filepath.Join(l.Root, dir, f)
			data, rerr := os.ReadFile(path)
			if rerr != nil {
				continue
			}
			rel := frontmatterRelated(string(data))
			if rel != "" && !related(rel) {
				continue
			}
			if rel == "" && !overlap(f, reqSummary) {
				continue
			}
			write(fmt.Sprintf("## %s（%s）\n\n", f, dir), string(data))
		}
	}
	if b.Len() == 0 {
		return "", ErrEmpty
	}
	return b.String(), nil
}

// frontmatterRelated extracts the `related` frontmatter field of a library
// document (list of task ids the document applies to), or "" when absent.
func frontmatterRelated(content string) string {
	if !strings.HasPrefix(content, "---") {
		return ""
	}
	end := strings.Index(content[3:], "---")
	if end < 0 {
		return ""
	}
	block := content[3 : 3+end]
	var fm struct {
		Related []string `yaml:"related"`
	}
	if err := yaml.Unmarshal([]byte(block), &fm); err != nil {
		return ""
	}
	return strings.Join(fm.Related, " ")
}

// acquireLock takes an exclusive flock on a per-path lock file under the
// user cache dir. The otg-task-* prefix intentionally reuses
// yamlfrontmatter.CleanStaleTaskLocks; lock files must never be unlinked while
// waiters exist because unlink + reopen can grant simultaneous ownership.
func acquireLock(path string) (func(), error) {
	sum := sha256.Sum256([]byte("designlib:" + path))
	lockPath := filepath.Join(taskLockDir(), fmt.Sprintf("otg-task-%x.lock", sum[:]))
	file, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("designlib: open lock: %w", err)
	}
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("designlib: flock: %w", err)
	}
	return func() {
		_ = syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
		_ = file.Close()
	}, nil
}

// taskLockDir mirrors pkg/yamlfrontmatter.taskLockDir: lock files live under
// XDG cache (not /tmp — tmpfs residuals accumulated without bound and pushed
// the box into OOM once).
func taskLockDir() string {
	cacheDir, err := os.UserCacheDir()
	if err != nil || cacheDir == "" {
		cacheDir = os.TempDir()
	}
	dir := filepath.Join(cacheDir, "otg", "locks")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return os.TempDir()
	}
	return dir
}
