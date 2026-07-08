package term

import (
	"bufio"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
)

var recursivePickerIgnoreFiles = [...]string{".gitignore", ".hgignore", ".ignore"}

type recursiveIgnoreMatcher struct {
	root  string
	rules []recursiveIgnoreRule
}

type recursiveIgnoreRule struct {
	base     string
	pattern  string
	regexp   bool
	negated  bool
	dirOnly  bool
	anchored bool
	hasSlash bool
}

func newRecursiveIgnoreMatcher(root string) *recursiveIgnoreMatcher {
	return &recursiveIgnoreMatcher{root: filepath.Clean(root)}
}

func (m *recursiveIgnoreMatcher) loadDir(dir string) {
	if m == nil {
		return
	}
	base := m.relSlash(dir)
	for _, name := range recursivePickerIgnoreFiles {
		m.loadFile(filepath.Join(dir, name), base)
	}
}

func (m *recursiveIgnoreMatcher) loadFile(file, base string) {
	f, err := os.Open(file)
	if err != nil {
		return
	}
	defer f.Close()

	syntax := "glob"
	if filepath.Base(file) == ".hgignore" {
		syntax = "regexp"
	}
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if strings.HasPrefix(line, "syntax:") {
			syntax = strings.TrimSpace(strings.TrimPrefix(line, "syntax:"))
			continue
		}
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		rule := recursiveIgnoreRule{base: base, regexp: syntax == "regexp"}
		if strings.HasPrefix(line, "!") {
			rule.negated = true
			line = strings.TrimSpace(strings.TrimPrefix(line, "!"))
		}
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, `\#`) || strings.HasPrefix(line, `\!`) {
			line = line[1:]
		}
		if !rule.regexp {
			line = filepath.ToSlash(line)
			if strings.HasSuffix(line, "/") {
				rule.dirOnly = true
				line = strings.TrimRight(line, "/")
			}
			if strings.HasPrefix(line, "/") {
				rule.anchored = true
				line = strings.TrimLeft(line, "/")
			}
			line = path.Clean(line)
			if line == "." || line == "" {
				continue
			}
			rule.hasSlash = strings.Contains(line, "/")
		}
		rule.pattern = line
		m.rules = append(m.rules, rule)
	}
}

func (m *recursiveIgnoreMatcher) ignored(file string, dir bool) bool {
	if m == nil {
		return false
	}
	rel := m.relSlash(file)
	if rel == "" {
		return false
	}
	ignored := false
	for _, rule := range m.rules {
		if rule.match(rel, dir) {
			ignored = !rule.negated
		}
	}
	return ignored
}

func (m *recursiveIgnoreMatcher) relSlash(file string) string {
	rel, err := filepath.Rel(m.root, file)
	if err != nil || rel == "." {
		return ""
	}
	return filepath.ToSlash(filepath.Clean(rel))
}

func (r recursiveIgnoreRule) match(rel string, dir bool) bool {
	if r.dirOnly && !dir {
		return false
	}
	sub := rel
	if r.base != "" {
		prefix := r.base + "/"
		if !strings.HasPrefix(rel, prefix) {
			return false
		}
		sub = strings.TrimPrefix(rel, prefix)
	}
	if r.regexp {
		ok, err := regexp.MatchString(r.pattern, sub)
		return err == nil && ok
	}
	if r.anchored || r.hasSlash {
		return ignoreGlobMatch(r.pattern, sub) || (r.dirOnly && strings.HasPrefix(sub, r.pattern+"/"))
	}
	for _, part := range strings.Split(sub, "/") {
		if ignoreGlobMatch(r.pattern, part) {
			return true
		}
	}
	return false
}

func ignoreGlobMatch(pattern, candidate string) bool {
	if pattern == candidate {
		return true
	}
	if !strings.Contains(pattern, "**") {
		ok, err := path.Match(pattern, candidate)
		return err == nil && ok
	}
	re := strings.Builder{}
	re.WriteString("^")
	for i := 0; i < len(pattern); i++ {
		switch pattern[i] {
		case '*':
			if i+1 < len(pattern) && pattern[i+1] == '*' {
				re.WriteString(".*")
				i++
			} else {
				re.WriteString("[^/]*")
			}
		case '?':
			re.WriteString("[^/]")
		default:
			re.WriteString(regexp.QuoteMeta(string(pattern[i])))
		}
	}
	re.WriteString("$")
	ok, err := regexp.MatchString(re.String(), candidate)
	return err == nil && ok
}

func recursivePickerAlwaysSkipDir(name string) bool {
	switch name {
	case ".git", ".hg", ".sl":
		return true
	default:
		return false
	}
}
