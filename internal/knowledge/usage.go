package knowledge

import (
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/ndzuki/obsidian-task-runner/pkg/yamlfrontmatter"
)

// ProjectUsage aggregates the topic ↔ project reference graph from every
// task's knowledge_refs (planned by Round 1) and knowledge_applied
// (delivery-time hit/total recorded at merge). It answers both directions:
// which projects reference a knowledge document, and which documents a
// project relies on — the "is this topic actually used" view the INDEX
// otherwise cannot show.
type ProjectUsage struct {
	// DocProjects maps normalized ref path → sorted project names.
	DocProjects map[string][]string
	// ProjectRefs maps project → sorted ref paths.
	ProjectRefs map[string][]string
	// ProjectApplied counts tasks per project with a non-empty
	// knowledge_applied marker (delivered application).
	ProjectApplied map[string]int
}

// ScanProjectUsage walks every project's Tasks/ directory, reads the
// knowledge_refs / knowledge_applied frontmatter, and builds the graph.
func ScanProjectUsage(vaultDir string) (*ProjectUsage, error) {
	usage := &ProjectUsage{
		DocProjects:    make(map[string][]string),
		ProjectRefs:    make(map[string][]string),
		ProjectApplied: make(map[string]int),
	}
	projectsDir := filepath.Join(vaultDir, "Projects")
	projects, err := os.ReadDir(projectsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return usage, nil
		}
		return nil, err
	}
	for _, project := range projects {
		if !project.IsDir() {
			continue
		}
		tasksDir := filepath.Join(projectsDir, project.Name(), "Tasks")
		entries, err := os.ReadDir(tasksDir)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".md") {
				continue
			}
			data, err := os.ReadFile(filepath.Join(tasksDir, entry.Name()))
			if err != nil {
				continue
			}
			fm, err := yamlfrontmatter.Parse(data)
			if err != nil || fm == nil {
				continue
			}
			for _, ref := range fm.KnowledgeRefs {
				clean := strings.TrimPrefix(strings.TrimSpace(ref), "References/")
				clean = strings.TrimPrefix(clean, "/")
				if clean == "" {
					continue
				}
				if !containsStr(usage.DocProjects[clean], project.Name()) {
					usage.DocProjects[clean] = append(usage.DocProjects[clean], project.Name())
				}
				if !containsStr(usage.ProjectRefs[project.Name()], clean) {
					usage.ProjectRefs[project.Name()] = append(usage.ProjectRefs[project.Name()], clean)
				}
			}
			if strings.TrimSpace(fm.KnowledgeApplied) != "" {
				usage.ProjectApplied[project.Name()]++
			}
		}
	}
	for k := range usage.DocProjects {
		sort.Strings(usage.DocProjects[k])
	}
	for k := range usage.ProjectRefs {
		sort.Strings(usage.ProjectRefs[k])
	}
	return usage, nil
}

func containsStr(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}
