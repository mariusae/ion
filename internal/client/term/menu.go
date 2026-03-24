package term

import (
	"fmt"
	"io"
	"strings"

	"ion/internal/proto/wire"
)

type menuItemKind int

const (
	menuWrite menuItemKind = iota
	menuFile
	menuCut
	menuSnarf
	menuPaste
	menuLook
	menuRegexp
	menuPlumb
)

type menuItem struct {
	label    string
	shortcut string
	kind     menuItemKind
	fileID   int
	sepAfter bool
	current  bool
}

type menuState struct {
	visible bool
	x       int
	y       int
	width   int
	height  int
	hover   int
	title   string
	items   []menuItem
}

type menuStickyState struct {
	itemIndex          int
	preferPreviousFile bool
	previousFileID     int
}

func newMenuState() *menuState {
	return &menuState{
		hover: -1,
	}
}

func (m *menuState) dismiss() {
	m.visible = false
	m.hover = -1
}

func buildContextMenu(buffer *bufferState, files []wire.MenuFile, clickX, clickY int, sticky menuStickyState) *menuState {
	menu := newMenuState()
	if buffer == nil {
		return menu
	}

	if strings.TrimSpace(buffer.name) != "" {
		menu.items = append(menu.items, menuItem{
			label:    " write",
			shortcut: "(w)",
			kind:     menuWrite,
			sepAfter: true,
		})
	}
	menu.items = append(menu.items,
		menuItem{label: " cut", shortcut: "(x)", kind: menuCut},
		menuItem{label: " snarf", shortcut: "(c)", kind: menuSnarf},
		menuItem{label: " paste", shortcut: "(v)", kind: menuPaste},
		menuItem{label: " look", shortcut: "(l)", kind: menuLook},
		menuItem{label: " plumb", shortcut: "(b)", kind: menuPlumb},
		menuItem{label: " /regexp", shortcut: "(/)", kind: menuRegexp, sepAfter: true},
	)
	for _, f := range files {
		name := f.Name
		if name == "" {
			name = "(unnamed)"
		}
		maxName := 38 - 5
		label := name
		if len([]rune(label)) > maxName {
			runes := []rune(label)
			label = string(runes[len(runes)-maxName:])
		}
		menu.items = append(menu.items, menuItem{
			label:   fmt.Sprintf(" %c%c %s", dirtyMark(f.Dirty), currentMark(f.Current), label),
			kind:    menuFile,
			fileID:  f.ID,
			current: f.Current,
		})
	}
	if len(menu.items) == 0 {
		return menu
	}

	pct := 0
	if len(buffer.text) > 0 {
		pct = buffer.origin * 100 / len(buffer.text)
	}
	titleName := buffer.name
	if titleName == "" {
		titleName = "(unnamed)"
	}
	menu.title = fmt.Sprintf(" %s (%d%%) ", titleName, pct)

	menu.width = len([]rune(menu.title)) + 2
	for _, item := range menu.items {
		itemWidth := len([]rune(item.label)) + 2
		if item.shortcut != "" {
			itemWidth += len([]rune(item.shortcut)) + 1
		}
		if itemWidth > menu.width {
			menu.width = itemWidth
		}
	}
	if menu.width < 16 {
		menu.width = 16
	}
	if menu.width > 40 {
		menu.width = 40
	}

	menu.height = 2 + len(menu.items)
	for i, item := range menu.items {
		if item.sepAfter && i < len(menu.items)-1 {
			menu.height++
		}
	}

	menu.x = clickX - menu.width/2
	stickyHover := resolveMenuStickyHover(menu.items, sticky)
	if stickyHover >= 0 && stickyHover < len(menu.items) {
		itemRow := 1
		for i := 0; i < stickyHover; i++ {
			itemRow++
			if menu.items[i].sepAfter && i < len(menu.items)-1 {
				itemRow++
			}
		}
		menu.y = clickY - itemRow
		menu.hover = stickyHover
	} else {
		menu.y = clickY - menu.height/2
	}
	if menu.x < 0 {
		menu.x = 0
	}
	if menu.y < 0 {
		menu.y = 0
	}
	if menu.x+menu.width > termCols {
		menu.x = termCols - menu.width
	}
	if menu.y+menu.height > termRows {
		menu.y = termRows - menu.height
	}
	if menu.x < 0 {
		menu.x = 0
	}
	if menu.y < 0 {
		menu.y = 0
	}
	menu.visible = true
	return menu
}

func drawMenu(stdout io.Writer, menu *menuState, theme *uiTheme) error {
	if menu == nil || !menu.visible {
		return nil
	}
	inner := menu.width - 2
	row := menu.y
	if err := writeMenuLine(stdout, row, menu.x, formatMenuBorder(menu.title, inner, '╭', '╮', '─'), theme.titlePrefix(), theme); err != nil {
		return err
	}
	row++
	for i, item := range menu.items {
		if err := writeMenuItem(stdout, row, menu.x, inner, item, menu.hover == i, theme); err != nil {
			return err
		}
		row++
		if item.sepAfter && i < len(menu.items)-1 {
			if err := writeMenuLine(stdout, row, menu.x, formatMenuBorder("", inner, '├', '┤', '─'), theme.subtlePrefix(), theme); err != nil {
				return err
			}
			row++
		}
	}
	return writeMenuLine(stdout, row, menu.x, formatMenuBorder("", inner, '╰', '╯', '─'), theme.subtlePrefix(), theme)
}

func writeMenuLine(stdout io.Writer, row, col int, line, prefix string, theme *uiTheme) error {
	if theme == nil || prefix == "" {
		_, err := fmt.Fprintf(stdout, "\x1b[%d;%dH%s", row+1, col+1, line)
		return err
	}
	_, err := fmt.Fprintf(stdout, "\x1b[%d;%dH%s%s%s", row+1, col+1, prefix, line, styleReset())
	return err
}

func writeMenuItem(stdout io.Writer, row, col, inner int, item menuItem, hover bool, theme *uiTheme) error {
	content := item.label
	if item.shortcut != "" {
		padding := inner - len([]rune(item.label)) - len([]rune(item.shortcut)) - 1
		if padding < 1 {
			padding = 1
		}
		content += strings.Repeat(" ", padding) + item.shortcut
	}
	runes := []rune(content)
	if len(runes) > inner {
		runes = runes[len(runes)-inner:]
	}
	content = string(runes)
	if pad := inner - len([]rune(content)); pad > 0 {
		content += strings.Repeat(" ", pad)
	}
	if theme == nil {
		if hover {
			content = "\x1b[7m" + content + "\x1b[27m"
		}
		return writeMenuLine(stdout, row, col, "│"+content+"│", "", nil)
	}
	prefix := theme.subtlePrefix()
	if hover {
		prefix = theme.hoverPrefix()
	}
	return writeMenuLine(stdout, row, col, "│"+content+"│", prefix, theme)
}

func formatMenuBorder(title string, inner int, leftBorder, rightBorder, fill rune) string {
	runes := []rune(title)
	if len(runes) > inner {
		runes = runes[:inner]
	}
	text := string(runes)
	if pad := inner - len(runes); pad > 0 {
		leftPad := pad / 2
		rightPad := pad - leftPad
		text = strings.Repeat(string(fill), leftPad) + text + strings.Repeat(string(fill), rightPad)
	}
	return string(leftBorder) + text + string(rightBorder)
}

func (m *menuState) itemAt(x, y int) int {
	if m == nil || !m.visible {
		return -1
	}
	row := y - m.y - 1
	if x <= m.x || x >= m.x+m.width-1 || row < 0 {
		return -1
	}
	r := 0
	for i, item := range m.items {
		if r == row {
			return i
		}
		r++
		if item.sepAfter && i < len(m.items)-1 {
			r++
		}
	}
	return -1
}

func dirtyMark(dirty bool) rune {
	if dirty {
		return '\''
	}
	return ' '
}

func currentMark(current bool) rune {
	if current {
		return '.'
	}
	return ' '
}

func resolveMenuStickyHover(items []menuItem, sticky menuStickyState) int {
	if sticky.preferPreviousFile {
		if idx := menuItemIndexByFileID(items, sticky.previousFileID); idx >= 0 {
			return idx
		}
	}
	if sticky.itemIndex >= 0 && sticky.itemIndex < len(items) {
		return sticky.itemIndex
	}
	return -1
}

func nextMenuStickyState(menu *menuState, itemIndex int, item menuItem) menuStickyState {
	next := menuStickyState{itemIndex: itemIndex}
	if item.kind != menuFile {
		return next
	}
	if currentID, ok := currentMenuFileID(menu); ok {
		next.preferPreviousFile = true
		next.previousFileID = currentID
	}
	return next
}

func currentMenuFileID(menu *menuState) (int, bool) {
	if menu == nil {
		return 0, false
	}
	for _, item := range menu.items {
		if item.kind == menuFile && item.current {
			return item.fileID, true
		}
	}
	return 0, false
}

func menuItemIndexByFileID(items []menuItem, fileID int) int {
	for i, item := range items {
		if item.kind == menuFile && item.fileID == fileID {
			return i
		}
	}
	return -1
}

func escapeSearchPattern(pattern string) string {
	pattern = strings.ReplaceAll(pattern, `\`, `\\`)
	pattern = strings.ReplaceAll(pattern, `/`, `\/`)
	return pattern
}

func plumbToken(state *bufferState) string {
	if state == nil {
		return ""
	}
	if state.dotStart != state.dotEnd {
		return strings.TrimSpace(string(state.text[state.dotStart:state.dotEnd]))
	}
	lineLo := lineStart(state.text, state.cursor)
	lineHi := lineEnd(state.text, state.cursor)
	left := state.cursor
	for left > lineLo {
		r := state.text[left-1]
		if r < 0x21 || r == '"' || r == '`' {
			break
		}
		left--
	}
	right := state.cursor
	for right < lineHi {
		r := state.text[right]
		if r < 0x21 || r == '"' || r == '`' {
			break
		}
		right++
	}
	for right > left && state.text[right-1] == ':' {
		right--
	}
	lastNumEnd := -1
	for i := left; i < right; i++ {
		if state.text[i] != ':' || i+1 >= right || state.text[i+1] < '0' || state.text[i+1] > '9' {
			continue
		}
		i++
		for i < right && state.text[i] >= '0' && state.text[i] <= '9' {
			i++
		}
		lastNumEnd = i
		i--
	}
	if lastNumEnd > 0 && lastNumEnd < right {
		right = lastNumEnd
	}
	return strings.TrimSpace(string(state.text[left:right]))
}
