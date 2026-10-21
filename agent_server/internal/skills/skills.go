// Package skills holds the playbooks a Session created with skills_enabled reads on demand.
// Adding a skill is adding a folder with a SKILL.md; no Go change is needed.
package skills

import (
	"embed"
	"fmt"
	"io/fs"
	"path"
	"strings"
)

//go:embed */SKILL.md
var files embed.FS

const Instruction = `

Skills: skill_index in your context lists playbooks by name and description. Before doing a task a skill describes, read it once with skill_get and follow it.`

type Entry struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

type skill struct {
	Entry
	content string
}

// The files are embedded at build time, so a malformed SKILL.md is a build defect and fails at startup.
var library = mustLoad()

func Index() []Entry {
	entries := make([]Entry, 0, len(library))
	for _, item := range library {
		entries = append(entries, item.Entry)
	}
	return entries
}

func Get(name string) (string, bool) {
	for _, item := range library {
		if item.Name == name {
			return item.content, true
		}
	}
	return "", false
}

func mustLoad() []skill {
	paths, err := fs.Glob(files, "*/SKILL.md")
	if err != nil {
		panic(err)
	}
	loaded := make([]skill, 0, len(paths))
	for _, file := range paths {
		raw, err := files.ReadFile(file)
		if err != nil {
			panic(err)
		}
		item, err := parse(string(raw))
		if err != nil {
			panic(fmt.Sprintf("skill %s: %v", file, err))
		}
		if item.Name != path.Dir(file) {
			panic(fmt.Sprintf("skill %s: name %q must match its folder", file, item.Name))
		}
		loaded = append(loaded, item)
	}
	return loaded
}

// parse reads a "---" frontmatter of single-line `key: value` pairs; only name and description are used.
func parse(raw string) (skill, error) {
	raw = strings.ReplaceAll(raw, "\r\n", "\n")
	if !strings.HasPrefix(raw, "---\n") {
		return skill{}, fmt.Errorf("missing frontmatter")
	}
	header, content, found := strings.Cut(strings.TrimPrefix(raw, "---\n"), "\n---\n")
	if !found {
		return skill{}, fmt.Errorf("unterminated frontmatter")
	}
	var item skill
	for _, line := range strings.Split(header, "\n") {
		key, value, _ := strings.Cut(line, ":")
		switch strings.TrimSpace(key) {
		case "name":
			item.Name = strings.TrimSpace(value)
		case "description":
			item.Description = strings.TrimSpace(value)
		}
	}
	item.content = strings.TrimSpace(content)
	if item.Name == "" || item.Description == "" || item.content == "" {
		return skill{}, fmt.Errorf("name, description and content are required")
	}
	return item, nil
}
