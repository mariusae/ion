package term

import (
	"fmt"
	"io"
	"strings"

	clienttarget "ion/internal/client/target"
	"ion/internal/proto/wire"
)

type menuItemKind int

const (
	menuWrite menuItemKind = iota
	menuFile
	menuCommand
	menuCut
	menuSnarf
	menuPaste
	menuLook
	menuRegexp
	menuPlumb
	menuHistoryPop
)

type menuItem struct {
	label    string
	shortcut string
	kind     menuItemKind
	fileID   int
	command  string
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
	preferHistory      bool
	historyKind        menuItemKind
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

func buildContextMenu(buffer *bufferState, files []wire.MenuFile, commands []wire.MenuCommand, nav wire.NavigationStack, clickX, clickY int, sticky menuStickyState) *menuState {
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
	for i, cmd := range commands {
		label := strings.TrimSpace(cmd.Label)
		if label == "" {
			label = strings.TrimSpace(cmd.Command)
		}
		if label == "" {
			continue
		}
		item := menuItem{
			label:   " " + label,
			kind:    menuCommand,
			command: strings.TrimSpace(cmd.Command),
		}
		if i == len(commands)-1 {
			item.sepAfter = true
		}
		menu.items = append(menu.items, item)
	}
	if pop, ok := popNavigationMenuItem(nav, len(files) == 0); ok {
		menu.items = append(menu.items, pop)
	} else if len(menu.items) > 0 && len(files) > 0 {
		menu.items[len(menu.items)-1].sepAfter = true
	}
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
			label:   fmt.Sprintf(" %c%c %s", dirtyMark(f.Dirty, f.Changed), currentMark(f.Current), label),
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
	if err := writeMenuLine(stdout, row, menu.x, formatMenuBorder(menu.title, inner, '╭', '╮', '─'), theme.subtlePrefix(), theme); err != nil {
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
	line := formatMenuItemLine(item, inner)
	if theme == nil {
		if item.current && hover {
			return writeMenuLine(stdout, row, col, "\x1b[1;7m"+line+"\x1b[27;22m", "", nil)
		}
		if hover {
			return writeMenuLine(stdout, row, col, "\x1b[7m"+line+"\x1b[27m", "", nil)
		} else if item.current {
			return writeMenuLine(stdout, row, col, "\x1b[1m"+line+"\x1b[22m", "", nil)
		}
		return writeMenuLine(stdout, row, col, line, "", nil)
	}
	prefix := menuItemPrefix(theme, item.current, hover)
	return writeMenuLine(stdout, row, col, line, prefix, theme)
}

func menuItemPrefix(theme *uiTheme, current, hover bool) string {
	if theme == nil {
		return ""
	}
	switch {
	case hover && current:
		return sgr("1", theme.bgCode(theme.cursorBG))
	case hover:
		return theme.prefixFor(theme.cursorBG)
	case current:
		return sgr("1", theme.bgCode(theme.subtleBG))
	default:
		return theme.subtlePrefix()
	}
}

func menuBorderStyle(theme *uiTheme) string {
	if theme == nil {
		return ""
	}
	return theme.subtlePrefix()
}

func menuItemStyle(theme *uiTheme, current, hover bool) string {
	if theme != nil {
		return menuItemPrefix(theme, current, hover)
	}
	switch {
	case hover && current:
		return "\x1b[1;7m"
	case hover:
		return "\x1b[7m"
	case current:
		return "\x1b[1m"
	default:
		return ""
	}
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

func formatMenuItemLine(item menuItem, inner int) string {
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
	return "│" + content + "│"
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

func (m *menuState) outsideDistance(x, y int) int {
	if m == nil || !m.visible {
		return 0
	}
	left := m.x
	right := m.x + m.width - 1
	top := m.y
	bottom := m.y + m.height - 1
	dx := 0
	switch {
	case x < left:
		dx = left - x
	case x > right:
		dx = x - right
	}
	dy := 0
	switch {
	case y < top:
		dy = top - y
	case y > bottom:
		dy = y - bottom
	}
	if dx > dy {
		return dx
	}
	return dy
}

func dirtyMark(dirty, changed bool) rune {
	if dirty && changed {
		return '"'
	}
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

func popNavigationMenuItem(nav wire.NavigationStack, lastSection bool) (menuItem, bool) {
	if nav.Current <= 0 || nav.Current > len(nav.Entries)-1 {
		return menuItem{}, false
	}
	return menuItem{
		label:    " pop " + nav.Entries[nav.Current-1].Label,
		shortcut: "(P)",
		kind:     menuHistoryPop,
		sepAfter: !lastSection,
	}, true
}

func resolveMenuStickyHover(items []menuItem, sticky menuStickyState) int {
	if sticky.preferHistory {
		if idx := menuItemIndexByKind(items, sticky.historyKind); idx >= 0 {
			return idx
		}
		return -1
	}
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
	switch item.kind {
	case menuHistoryPop:
		next.preferHistory = true
		next.historyKind = item.kind
		return next
	case menuFile:
		if currentID, ok := currentMenuFileID(menu); ok {
			next.preferPreviousFile = true
			next.previousFileID = currentID
		}
		return next
	default:
		return next
	}
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

func menuItemIndexByKind(items []menuItem, kind menuItemKind) int {
	for i, item := range items {
		if item.kind == kind {
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
	return clienttarget.TrimToken(strings.TrimSpace(string(state.text[left:right])))
}
