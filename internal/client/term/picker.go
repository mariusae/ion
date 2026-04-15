package term

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"

	"ion/internal/proto/wire"
)

type overlayMode int

const (
	overlayModeCommand overlayMode = iota
	overlayModeCommandPicker
	overlayModeFilePicker
	overlayModeDirectoryPicker
)

type overlayPickerItem struct {
	key     string
	label   string
	value   string
	search  string
	fileID  int
	path    string
	current bool
}

type overlayPicker struct {
	mode      overlayMode
	items     []overlayPickerItem
	filtered  []int
	selected  int
	preferred string
}

func localTermNamespaceDoc() wire.NamespaceProviderDoc {
	return wire.NamespaceProviderDoc{
		Namespace: "term",
		Summary:   "terminal HUD commands",
		Help:      "Commands implemented locally by the interactive terminal HUD. These commands depend on terminal state such as the current selection, token under the cursor, snarf buffer, or tmux pane context.",
		Commands: []wire.NamespaceCommandDoc{
			{
				Name:    "write",
				Summary: "save the current buffer",
				Help:    "Saves the current buffer using the terminal UI save path. If the buffer is unnamed, opens a prefilled write command so you can provide a file name.",
			},
			{
				Name:    "cut",
				Summary: "cut the current selection",
				Help:    "Copies the current selection into the terminal snarf buffer and clipboard, then deletes it from the buffer.",
			},
			{
				Name:    "snarf",
				Summary: "copy the current selection",
				Help:    "Copies the current selection into the terminal snarf buffer and clipboard without modifying the buffer.",
			},
			{
				Name:    "paste",
				Summary: "paste the current snarf buffer",
				Help:    "Pastes the terminal snarf buffer at the current selection or cursor position.",
			},
			{
				Name:    "tmux",
				Summary: "exchange the snarf buffer with tmux",
				Help:    "Exchanges the terminal snarf buffer with the current tmux paste buffer. If tmux has no paste buffer yet, treats it as empty.",
			},
			{
				Name:    "send",
				Summary: "send dot or snarf to the command window",
				Help:    "Sends the current selection to the command window as if typed there, or uses the snarf buffer if the selection is empty. The sent text becomes the new snarf buffer.",
			},
			{
				Name:    "look",
				Summary: "find the current selection or token",
				Help:    "Searches forward for the current selection, or the token under the cursor if there is no selection.",
			},
			{
				Name:    "regexp",
				Summary: "repeat the previous regexp search",
				Help:    "Re-runs the most recently used sam regexp search pattern. If no regexp has been used yet, reports a warning.",
			},
			{
				Name:    "plumb",
				Summary: "open the current token as a target",
				Help:    "Opens the current selection or token under the cursor using B-style target plumbing and pushes the destination onto the navigation stack.",
			},
			{
				Name:    "plumb2",
				Summary: "open the current token in another session",
				Help:    "Opens the current selection or token under the cursor in the next-most-recent resident session. If no other session is available, opens a new attached pane as in ion -N.",
			},
			{
				Name:    "split",
				Summary: "open a new attached pane for the current file",
				Help:    "Opens a new attached pane as in ion -N. If the current buffer names a file, the new pane opens that file.",
			},
		},
	}
}

func augmentNamespaceDocs(docs []wire.NamespaceProviderDoc) []wire.NamespaceProviderDoc {
	out := append([]wire.NamespaceProviderDoc(nil), docs...)
	local := localTermNamespaceDoc()
	termIdx := -1
	for i := range out {
		if strings.TrimSpace(out[i].Namespace) == local.Namespace {
			termIdx = i
			break
		}
	}
	if termIdx < 0 {
		out = append(out, local)
		return out
	}
	existing := make(map[string]int, len(out[termIdx].Commands))
	for i := range out[termIdx].Commands {
		existing[strings.TrimSpace(out[termIdx].Commands[i].Name)] = i
	}
	for _, command := range local.Commands {
		name := strings.TrimSpace(command.Name)
		if idx, ok := existing[name]; ok {
			if strings.TrimSpace(out[termIdx].Commands[idx].Summary) == "" {
				out[termIdx].Commands[idx].Summary = command.Summary
			}
			if strings.TrimSpace(out[termIdx].Commands[idx].Help) == "" {
				out[termIdx].Commands[idx].Help = command.Help
			}
			if strings.TrimSpace(out[termIdx].Commands[idx].Args) == "" {
				out[termIdx].Commands[idx].Args = command.Args
			}
			continue
		}
		out[termIdx].Commands = append(out[termIdx].Commands, command)
	}
	return out
}

func buildCommandPickerItems(docs []wire.NamespaceProviderDoc, menu []wire.MenuCommand, history []string) ([]overlayPickerItem, string) {
	summaries := commandSummaryIndex(docs, menu)
	dedupedHistory := dedupeCommandPickerHistory(history)
	items := make([]overlayPickerItem, 0, len(dedupedHistory)+len(summaries)+1)
	preferred := ""
	seenCatalog := make(map[string]struct{}, len(dedupedHistory))
	historyCount := 0
	for _, value := range dedupedHistory {
		label := value
		if summary := strings.TrimSpace(summaries[value]); summary != "" {
			label += " - " + summary
		}
		key := fmt.Sprintf("history:%06d", historyCount)
		items = append(items, overlayPickerItem{
			key:    key,
			label:  label,
			value:  value,
			search: strings.ToLower(label),
		})
		preferred = key
		seenCatalog[value] = struct{}{}
		historyCount++
	}
	docs = augmentNamespaceDocs(docs)
	seen := make(map[string]overlayPickerItem)
	seen[":help"] = overlayPickerItem{
		key:    "catalog::help",
		label:  ":help - show detailed help for a command",
		value:  ":help",
		search: ":help show detailed help for a command",
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
			value := ":" + namespace + ":" + name
			summary := strings.TrimSpace(command.Summary)
			label := value
			if summary != "" {
				label += " - " + summary
			}
			if _, ok := seenCatalog[value]; ok {
				continue
			}
			seen[value] = overlayPickerItem{
				key:    "catalog:" + value,
				label:  label,
				value:  value,
				search: strings.ToLower(value + " " + summary),
			}
		}
	}
	for _, item := range menu {
		command := strings.TrimSpace(item.Command)
		if command == "" {
			continue
		}
		if _, ok := seenCatalog[command]; ok {
			continue
		}
		if _, ok := seen[command]; ok {
			continue
		}
		label := command
		menuLabel := strings.TrimSpace(item.Label)
		if menuLabel != "" && menuLabel != command {
			label += " - " + menuLabel
		}
		seen[command] = overlayPickerItem{
			key:    "catalog:" + command,
			label:  label,
			value:  command,
			search: strings.ToLower(command + " " + menuLabel),
		}
	}
	for _, item := range seen {
		items = append(items, item)
	}
	if historyCount < len(items) {
		sort.Slice(items[historyCount:], func(i, j int) bool {
			left := items[historyCount+i]
			right := items[historyCount+j]
			return left.value < right.value
		})
	}
	return items, preferred
}

func dedupeCommandPickerHistory(history []string) []string {
	if len(history) == 0 {
		return nil
	}
	trimmed := make([]string, len(history))
	lastIndex := make(map[string]int, len(history))
	for i, command := range history {
		value := strings.TrimSpace(command)
		trimmed[i] = value
		if value == "" {
			continue
		}
		lastIndex[value] = i
	}
	out := make([]string, 0, len(lastIndex))
	for i, value := range trimmed {
		if value == "" {
			continue
		}
		if lastIndex[value] != i {
			continue
		}
		out = append(out, value)
	}
	return out
}

func buildFilePickerItems(files []wire.MenuFile, preferredFileID int) ([]overlayPickerItem, string) {
	items := make([]overlayPickerItem, 0, len(files))
	preferred := ""
	for _, file := range files {
		name := strings.TrimSpace(file.Name)
		if name == "" {
			name = "(unnamed)"
		}
		items = append(items, overlayPickerItem{
			key:     fmt.Sprintf("file:%d", file.ID),
			label:   fmt.Sprintf("%c%c %s", dirtyMark(file.Dirty, file.Changed), currentMark(file.Current), name),
			value:   name,
			search:  strings.ToLower(fmt.Sprintf("%c%c %s", dirtyMark(file.Dirty, file.Changed), currentMark(file.Current), name)),
			fileID:  file.ID,
			current: file.Current,
		})
		if preferredFileID != 0 && file.ID == preferredFileID {
			preferred = fmt.Sprintf("file:%d", file.ID)
		} else if preferred == "" && file.Current {
			preferred = fmt.Sprintf("file:%d", file.ID)
		}
	}
	return items, preferred
}

func buildDirectoryPickerItems(buffer *bufferState, files []wire.MenuFile) ([]overlayPickerItem, string, error) {
	dir, ok := currentBufferDirectory(buffer)
	if !ok {
		return nil, "", nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, "", err
	}
	items := make([]overlayPickerItem, 0, len(entries))
	preferred := ""
	currentPath := strings.TrimSpace(buffer.path)
	loadedByPath := make(map[string]wire.MenuFile, len(files))
	for _, file := range files {
		path := strings.TrimSpace(file.Path)
		if path == "" {
			continue
		}
		loadedByPath[filepath.Clean(path)] = file
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		path := filepath.Join(dir, name)
		current := sameMenuPath(path, currentPath)
		item := overlayPickerItem{
			key:     "path:" + path,
			value:   name,
			path:    path,
			current: current,
		}
		if loaded, ok := loadedByPath[filepath.Clean(path)]; ok {
			item.fileID = loaded.ID
			item.current = loaded.Current
			item.label = fmt.Sprintf("%c-%c %s", dirtyMark(loaded.Dirty, loaded.Changed), currentMark(loaded.Current), name)
		} else {
			item.label = "    " + name
		}
		item.search = strings.ToLower(item.label)
		items = append(items, item)
		if current {
			preferred = "path:" + path
		}
	}
	sort.Slice(items, func(i, j int) bool {
		return items[i].value < items[j].value
	})
	return items, preferred, nil
}

func shouldPreviewDirectoryFile(path string) (bool, error) {
	f, err := os.Open(path)
	if err != nil {
		return false, err
	}
	defer f.Close()

	buf := make([]byte, 8192)
	n, err := f.Read(buf)
	if err != nil && err != io.EOF {
		return false, err
	}
	buf = buf[:n]
	if len(buf) == 0 {
		return true, nil
	}
	if bytes.IndexByte(buf, 0) >= 0 {
		return false, nil
	}
	return utf8.Valid(buf), nil
}

func commandSummaryIndex(docs []wire.NamespaceProviderDoc, menu []wire.MenuCommand) map[string]string {
	docs = augmentNamespaceDocs(docs)
	summaries := map[string]string{
		":help": "show detailed help for a command",
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
			summaries[":"+namespace+":"+name] = strings.TrimSpace(command.Summary)
		}
	}
	for _, item := range menu {
		command := strings.TrimSpace(item.Command)
		if command == "" {
			continue
		}
		if _, ok := summaries[command]; ok {
			continue
		}
		summaries[command] = strings.TrimSpace(item.Label)
	}
	return summaries
}

func (o *overlayState) pickerActive() bool {
	return o != nil && o.picker != nil
}

func (o *overlayState) pickerMode() overlayMode {
	if o == nil || o.picker == nil {
		return overlayModeCommand
	}
	return o.picker.mode
}

func (o *overlayState) openPicker(mode overlayMode, items []overlayPickerItem, preferred string) {
	o.visible = true
	o.mode = mode
	o.running = false
	o.selecting = false
	o.selectBtn2 = false
	o.selectStart = overlaySelectionPos{line: -1}
	o.selectEnd = overlaySelectionPos{line: -1}
	o.input = o.input[:0]
	o.cursor = 0
	o.scroll = 0
	o.recallIdx = -1
	o.savedInput = o.savedInput[:0]
	o.picker = &overlayPicker{
		mode:      mode,
		items:     append([]overlayPickerItem(nil), items...),
		selected:  -1,
		preferred: strings.TrimSpace(preferred),
	}
	o.refreshPicker()
}

func (o *overlayState) closePicker() {
	if o == nil {
		return
	}
	o.mode = overlayModeCommand
	o.picker = nil
	o.resetInput()
}

func (o *overlayState) pickerMove(delta int) bool {
	if o == nil || o.picker == nil || len(o.picker.filtered) == 0 || delta == 0 {
		return false
	}
	next := o.picker.selected + delta
	if next < 0 {
		next = 0
	}
	if next >= len(o.picker.filtered) {
		next = len(o.picker.filtered) - 1
	}
	if next == o.picker.selected {
		return false
	}
	o.picker.selected = next
	return true
}

func (o *overlayState) pickerSelected() (overlayPickerItem, bool) {
	if o == nil || o.picker == nil || o.picker.selected < 0 || o.picker.selected >= len(o.picker.filtered) {
		return overlayPickerItem{}, false
	}
	return o.picker.items[o.picker.filtered[o.picker.selected]], true
}

func (o *overlayState) refreshPicker() {
	if o == nil || o.picker == nil {
		return
	}
	previousKey := ""
	if selected, ok := o.pickerSelected(); ok {
		previousKey = selected.key
	}
	query := strings.ToLower(strings.TrimSpace(string(o.input)))
	filtered := make([]int, 0, len(o.picker.items))
	for i, item := range o.picker.items {
		if query == "" || strings.Contains(item.search, query) {
			filtered = append(filtered, i)
		}
	}
	o.picker.filtered = filtered
	o.picker.selected = -1
	if len(filtered) == 0 {
		return
	}
	preferred := previousKey
	if query == "" && strings.TrimSpace(o.picker.preferred) != "" {
		preferred = o.picker.preferred
	}
	if preferred != "" {
		for i, idx := range filtered {
			if o.picker.items[idx].key == preferred {
				o.picker.selected = i
				return
			}
		}
	}
	o.picker.selected = 0
}
