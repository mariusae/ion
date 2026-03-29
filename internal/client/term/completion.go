package term

import (
	"os"
	"path/filepath"
	"sort"
	"strings"

	"ion/internal/proto/wire"
)

type namespaceDocProvider interface {
	NamespaceDocs() ([]wire.NamespaceProviderDoc, error)
}

type commandCompletion struct {
	name    string
	summary string
}

func completeOverlayCommandInput(provider namespaceDocProvider, overlay *overlayState) (bool, []string, error) {
	if provider == nil || overlay == nil {
		return false, nil, nil
	}
	start, end, prefix, ok := commandTokenRangeAtCursor(overlay.input, overlay.cursor)
	if !ok {
		return false, nil, nil
	}
	docs, err := provider.NamespaceDocs()
	if err != nil {
		return false, nil, err
	}
	matches := matchingCommandCompletions(commandCompletionsFromDocs(docs), prefix)
	if len(matches) == 0 {
		return true, nil, nil
	}
	if len(matches) == 1 {
		overlay.replaceRange(start, end, []rune(matches[0].name))
		return true, nil, nil
	}
	if common := longestCommonPrefix(commandNames(matches)); len(common) > len(prefix) {
		overlay.replaceRange(start, end, []rune(common))
	}
	lines := make([]string, 0, len(matches))
	for _, match := range matches {
		line := match.name
		if strings.TrimSpace(match.summary) != "" {
			line += " - " + strings.TrimSpace(match.summary)
		}
		lines = append(lines, line)
	}
	return true, lines, nil
}

func completeOverlayFileInput(overlay *overlayState, cwd string) (bool, []string) {
	if overlay == nil {
		return false, nil
	}
	start, end, token, displayDir, ok := fileTokenRangeAtCursor(overlay.input, overlay.cursor)
	if !ok {
		return false, nil
	}
	replacement, matches, ok := completePathToken(token, cwd)
	if !ok {
		return true, nil
	}
	if replacement != token {
		overlay.replaceRange(start, end, []rune(replacement))
		return true, nil
	}
	lines := make([]string, 0, len(matches))
	for _, match := range matches {
		lines = append(lines, strings.TrimPrefix(match, displayDir))
	}
	return true, lines
}

func commandTokenRangeAtCursor(input []rune, cursor int) (int, int, string, bool) {
	start, end, token, ok := tokenRangeAtCursor(input, cursor)
	if !ok || !strings.HasPrefix(token, ":") {
		return 0, 0, "", false
	}
	for _, r := range input[:start] {
		if !isCompletionSpace(r) {
			return 0, 0, "", false
		}
	}
	return start, end, token, true
}

func fileTokenRangeAtCursor(input []rune, cursor int) (int, int, string, string, bool) {
	if cursor < 0 {
		cursor = 0
	}
	if cursor > len(input) {
		cursor = len(input)
	}
	start := cursor
	for start > 0 && !isCompletionSpace(input[start-1]) {
		start--
	}
	end := cursor
	for end < len(input) && !isCompletionSpace(input[end]) {
		end++
	}
	token := string(input[start:end])
	colon := strings.IndexRune(token, ':')
	if colon >= 0 {
		colonPos := start + colon
		if cursor > colonPos {
			return 0, 0, "", "", false
		}
		end = colonPos
		token = string(input[start:end])
	}
	displayDir := ""
	if idx := strings.LastIndexAny(token, `/`+string(filepath.Separator)); idx >= 0 {
		displayDir = token[:idx+1]
	}
	return start, end, token, displayDir, true
}

func tokenRangeAtCursor(input []rune, cursor int) (int, int, string, bool) {
	if cursor < 0 {
		cursor = 0
	}
	if cursor > len(input) {
		cursor = len(input)
	}
	start := cursor
	for start > 0 && !isCompletionSpace(input[start-1]) {
		start--
	}
	end := cursor
	for end < len(input) && !isCompletionSpace(input[end]) {
		end++
	}
	if start == end {
		return 0, 0, "", false
	}
	return start, end, string(input[start:end]), true
}

func isCompletionSpace(r rune) bool {
	switch r {
	case ' ', '\t', '\r', '\n':
		return true
	default:
		return false
	}
}

func commandCompletionsFromDocs(docs []wire.NamespaceProviderDoc) []commandCompletion {
	seen := map[string]commandCompletion{
		":help": {name: ":help", summary: "show detailed help for a command"},
	}
	for _, provider := range docs {
		namespace := strings.TrimSpace(provider.Namespace)
		if namespace == "" {
			continue
		}
		for _, command := range provider.Commands {
			name := strings.TrimSpace(command.Name)
			if name == "" {
				continue
			}
			full := ":" + namespace + ":" + name
			seen[full] = commandCompletion{name: full, summary: strings.TrimSpace(command.Summary)}
		}
	}
	out := make([]commandCompletion, 0, len(seen))
	for _, command := range seen {
		out = append(out, command)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].name < out[j].name
	})
	return out
}

func matchingCommandCompletions(commands []commandCompletion, prefix string) []commandCompletion {
	matches := make([]commandCompletion, 0, len(commands))
	for _, command := range commands {
		if strings.HasPrefix(command.name, prefix) {
			matches = append(matches, command)
		}
	}
	return matches
}

func commandNames(commands []commandCompletion) []string {
	names := make([]string, 0, len(commands))
	for _, command := range commands {
		names = append(names, command.name)
	}
	return names
}

func longestCommonPrefix(values []string) string {
	if len(values) == 0 {
		return ""
	}
	prefix := values[0]
	for _, value := range values[1:] {
		for !strings.HasPrefix(value, prefix) && prefix != "" {
			prefix = prefix[:len(prefix)-1]
		}
		if prefix == "" {
			return ""
		}
	}
	return prefix
}

func completePathToken(token, cwd string) (string, []string, bool) {
	searchDir, typedDir, base := splitCompletionPath(token)
	if cwd == "" {
		cwd = "."
	}
	if !filepath.IsAbs(searchDir) {
		searchDir = filepath.Join(cwd, searchDir)
	}
	entries, err := os.ReadDir(searchDir)
	if err != nil {
		return token, nil, false
	}
	matches := make([]string, 0, len(entries))
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasPrefix(base, ".") && strings.HasPrefix(name, ".") {
			continue
		}
		if !strings.HasPrefix(name, base) {
			continue
		}
		match := typedDir + name
		if entry.IsDir() {
			match += string(filepath.Separator)
		}
		matches = append(matches, match)
	}
	if len(matches) == 0 {
		return token, nil, false
	}
	sort.Strings(matches)
	if len(matches) == 1 {
		return matches[0], matches, true
	}
	if common := longestCommonPrefix(matches); len(common) > len(token) {
		return common, matches, true
	}
	return token, matches, true
}

func splitCompletionPath(token string) (searchDir, typedDir, base string) {
	idx := strings.LastIndexAny(token, `/`+string(filepath.Separator))
	if idx < 0 {
		return ".", "", token
	}
	typedDir = token[:idx+1]
	base = token[idx+1:]
	if typedDir == "" {
		return ".", typedDir, base
	}
	return typedDir, typedDir, base
}
