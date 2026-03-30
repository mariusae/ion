package term

import (
	"fmt"
	"sort"
	"strings"

	"ion/internal/proto/wire"
)

type overlayMode int

const (
	overlayModeCommand overlayMode = iota
	overlayModeCommandPicker
	overlayModeFilePicker
)

type overlayPickerItem struct {
	label   string
	value   string
	search  string
	fileID  int
	current bool
}

type overlayPicker struct {
	mode      overlayMode
	items     []overlayPickerItem
	filtered  []int
	selected  int
	preferred string
}

func localIonNamespaceDoc() wire.NamespaceProviderDoc {
	return wire.NamespaceProviderDoc{
		Namespace: "ion",
		Summary:   "core ion server and terminal commands",
		Help:      "Built-in commands implemented directly by ion. Some commands are terminal-local HUD actions that mirror context-menu behavior.",
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
				Help:    "Opens the current selection or token under the cursor using B-style target plumbing.",
			},
		},
	}
}

func augmentNamespaceDocs(docs []wire.NamespaceProviderDoc) []wire.NamespaceProviderDoc {
	out := append([]wire.NamespaceProviderDoc(nil), docs...)
	local := localIonNamespaceDoc()
	ionIdx := -1
	for i := range out {
		if strings.TrimSpace(out[i].Namespace) == local.Namespace {
			ionIdx = i
			break
		}
	}
	if ionIdx < 0 {
		out = append(out, local)
		return out
	}
	existing := make(map[string]int, len(out[ionIdx].Commands))
	for i := range out[ionIdx].Commands {
		existing[strings.TrimSpace(out[ionIdx].Commands[i].Name)] = i
	}
	for _, command := range local.Commands {
		name := strings.TrimSpace(command.Name)
		if idx, ok := existing[name]; ok {
			if strings.TrimSpace(out[ionIdx].Commands[idx].Summary) == "" {
				out[ionIdx].Commands[idx].Summary = command.Summary
			}
			if strings.TrimSpace(out[ionIdx].Commands[idx].Help) == "" {
				out[ionIdx].Commands[idx].Help = command.Help
			}
			if strings.TrimSpace(out[ionIdx].Commands[idx].Args) == "" {
				out[ionIdx].Commands[idx].Args = command.Args
			}
			continue
		}
		out[ionIdx].Commands = append(out[ionIdx].Commands, command)
	}
	return out
}

func buildCommandPickerItems(docs []wire.NamespaceProviderDoc, menu []wire.MenuCommand, lastCommand string) ([]overlayPickerItem, string) {
	docs = augmentNamespaceDocs(docs)
	seen := make(map[string]overlayPickerItem)
	seen[":help"] = overlayPickerItem{
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
			seen[value] = overlayPickerItem{
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
		if _, ok := seen[command]; ok {
			continue
		}
		label := command
		menuLabel := strings.TrimSpace(item.Label)
		if menuLabel != "" && menuLabel != command {
			label += " - " + menuLabel
		}
		seen[command] = overlayPickerItem{
			label:  label,
			value:  command,
			search: strings.ToLower(command + " " + menuLabel),
		}
	}
	items := make([]overlayPickerItem, 0, len(seen)+1)
	for _, item := range seen {
		items = append(items, item)
	}
	sort.Slice(items, func(i, j int) bool {
		return items[i].value < items[j].value
	})

	preferred := strings.TrimSpace(lastCommand)
	if preferred == "" {
		return items, ""
	}
	for _, item := range items {
		if item.value == preferred {
			return items, preferred
		}
	}
	items = append([]overlayPickerItem{{
		label:  preferred + " - last command",
		value:  preferred,
		search: strings.ToLower(preferred + " last command"),
	}}, items...)
	return items, preferred
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
			label:   fmt.Sprintf("%c%c %s", dirtyMark(file.Dirty, file.Changed), currentMark(file.Current), name),
			value:   name,
			search:  strings.ToLower(name + " " + strings.TrimSpace(file.Path)),
			fileID:  file.ID,
			current: file.Current,
		})
		if preferredFileID != 0 && file.ID == preferredFileID {
			preferred = name
		} else if preferred == "" && file.Current {
			preferred = name
		}
	}
	return items, preferred
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
	previousValue := ""
	if selected, ok := o.pickerSelected(); ok {
		previousValue = selected.value
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
	preferred := previousValue
	if query == "" && strings.TrimSpace(o.picker.preferred) != "" {
		preferred = o.picker.preferred
	}
	if preferred != "" {
		for i, idx := range filtered {
			if o.picker.items[idx].value == preferred {
				o.picker.selected = i
				return
			}
		}
	}
	o.picker.selected = 0
}
