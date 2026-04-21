// Package skills discovers workspace skill markdown files.
package skills

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

type Skill struct {
	Name        string
	Path        string
	Description string
}

// List returns every <skillsDir>/*/SKILL.md as a Skill, sorted by name.
func List(skillsDir string) ([]Skill, error) {
	entries, err := os.ReadDir(skillsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []Skill
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		p := filepath.Join(skillsDir, e.Name(), "SKILL.md")
		data, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		out = append(out, Skill{
			Name:        e.Name(),
			Path:        p,
			Description: extractDescription(string(data)),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// LoadCombined returns every SKILL.md concatenated with the header rex injects
// into the Claude system prompt. Returns "" when no skills exist.
func LoadCombined(skillsDir string) (string, error) {
	skills, err := List(skillsDir)
	if err != nil {
		return "", err
	}
	var parts []string
	for _, s := range skills {
		data, err := os.ReadFile(s.Path)
		if err != nil {
			continue
		}
		t := strings.TrimSpace(string(data))
		if t != "" {
			parts = append(parts, t)
		}
	}
	if len(parts) == 0 {
		return "", nil
	}
	return "## Workspace Skills\n\n" + strings.Join(parts, "\n\n---\n\n"), nil
}

var headingRE = regexp.MustCompile(`^#+\s*`)

// extractDescription picks the first non-heading, non-empty line outside
// YAML frontmatter. Falls back to the first heading text if nothing else fits.
func extractDescription(content string) string {
	var heading string
	inFrontmatter := false
	for _, line := range strings.Split(content, "\n") {
		stripped := strings.TrimSpace(line)
		if stripped == "---" {
			inFrontmatter = !inFrontmatter
			continue
		}
		if inFrontmatter || stripped == "" {
			continue
		}
		if strings.HasPrefix(stripped, "#") {
			if heading == "" {
				heading = headingRE.ReplaceAllString(stripped, "")
			}
			continue
		}
		return stripped
	}
	if heading != "" {
		return heading
	}
	return "(no description)"
}
