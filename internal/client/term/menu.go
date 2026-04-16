package term

import (
	"fmt"
	"io"
	"path/filepath"
	"strings"

	clienttarget "ion/internal/client/target"
	"ion/internal/proto/wire"
)

type menuItemKind int

const (
	menuWrite menuItemKind = iota
	menuSplit
	menuFile
	menuCommand
	menuCut
	menuSnarf
	menuPaste
	menuTmux
	menuSend
	menuLook
	menuRegexp
	menuPlumb
	menuHistoryPop
)

type menuItem struct {
	label    string
	shortcut string
	keyRune  rune
	kind     menuItemKind
	fileID   int
	command  string
	sepAfter bool
	current  bool
}

type menuState struct {
	visible    bool
	x          int
	y          int
	width      int
	height     int
	hover      int
	running    bool
	runningIdx int
	title      string
	titleBase  string
	items      []menuItem
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
		hover:      -1,
		runningIdx: -1,
	}
}

func (m *menuState) dismiss() {
	m.visible = false
	m.hover = -1
	m.running = false
	m.runningIdx = -1
}

func buildContextMenu(buffer *bufferState, files []wire.MenuFile, commands []wire.MenuCommand, latestCommand string, nav wire.NavigationStack, clickX, clickY int, sticky menuStickyState) *menuState {
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
		menuItem{label: " send", shortcut: "(s)", kind: menuSend},
		menuItem{label: " /regexp", shortcut: "(/)", kind: menuRegexp},
		menuItem{label: " split", shortcut: "(n)", kind: menuSplit},
		menuItem{label: " <tmux>", shortcut: "(t)", kind: menuTmux, sepAfter: true},
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
		if shortcut, ok := menuCommandShortcutRune(cmd.Shortcut); ok {
			item.keyRune = shortcut
			item.shortcut = menuCommandShortcutLabel(shortcut)
		}
		if i == len(commands)-1 {
			item.sepAfter = true
		}
		menu.items = append(menu.items, item)
	}
	if item, ok := latestMenuCommandItem(commands, latestCommand, strings.TrimSpace(buffer.name) != "", popNavigationAvailable(nav, len(files) == 0)); ok {
		if len(menu.items) > 0 {
			menu.items[len(menu.items)-1].sepAfter = false
		}
		item.sepAfter = true
		menu.items = append(menu.items, item)
	}
	if pop, ok := popNavigationMenuItem(nav, len(files) == 0); ok {
		menu.items = append(menu.items, pop)
	} else if len(menu.items) > 0 && len(files) > 0 {
		menu.items[len(menu.items)-1].sepAfter = true
	}
	fileShortcutIndex := 0
	for _, f := range files {
		label := menuDisplayFileName(f.Name)
		menu.items = append(menu.items, menuItem{
			label:    fmt.Sprintf(" %c%c %s", dirtyMark(f.Dirty, f.Changed), currentMark(f.Current), label),
			shortcut: menuFileShortcutLabel(fileShortcutIndex),
			kind:     menuFile,
			fileID:   f.ID,
			current:  f.Current,
		})
		fileShortcutIndex++
	}
	if len(menu.items) == 0 {
		return menu
	}

	return finalizeMenuLayout(menu, menuTitleForBuffer(buffer), clickX, clickY, sticky)
}

func finalizeMenuLayout(menu *menuState, title string, clickX, clickY int, sticky menuStickyState) *menuState {
	if menu == nil || len(menu.items) == 0 {
		return menu
	}
	menu.titleBase = title
	menu.title = menu.titleBase

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

func menuTitleForBuffer(buffer *bufferState) string {
	if buffer == nil {
		return " (unnamed) "
	}
	pct := 0
	if len(buffer.text) > 0 {
		pct = buffer.origin * 100 / len(buffer.text)
	}
	titleName := buffer.name
	if titleName == "" {
		titleName = "(unnamed)"
	}
	return fmt.Sprintf(" %s (%d%%) ", titleName, pct)
}

func currentBufferDirectory(buffer *bufferState) (string, bool) {
	if buffer == nil {
		return "", false
	}
	path := strings.TrimSpace(buffer.path)
	if path == "" {
		path = strings.TrimSpace(buffer.name)
	}
	if path == "" {
		return "", false
	}
	if !filepath.IsAbs(path) {
		abs, err := filepath.Abs(path)
		if err != nil {
			return "", false
		}
		path = abs
	}
	return filepath.Dir(path), true
}

func sameMenuPath(left, right string) bool {
	left = filepath.Clean(strings.TrimSpace(left))
	right = filepath.Clean(strings.TrimSpace(right))
	return left != "" && right != "" && left == right
}

func latestMenuCommandItem(commands []wire.MenuCommand, latestCommand string, hasWrite bool, hasPop bool) (menuItem, bool) {
	latestCommand = strings.TrimSpace(latestCommand)
	if latestCommand == "" {
		return menuItem{}, false
	}
	if builtInMenuCommandPresent(latestCommand, hasWrite, hasPop) {
		return menuItem{}, false
	}
	for _, cmd := range commands {
		if strings.TrimSpace(cmd.Command) == latestCommand {
			return menuItem{}, false
		}
	}
	return menuItem{
		label:   " " + latestCommand,
		kind:    menuCommand,
		command: latestCommand,
	}, true
}

func builtInMenuCommandPresent(command string, hasWrite bool, hasPop bool) bool {
	switch command {
	case ":term:split", ":term:cut", ":term:snarf", ":term:paste", ":term:tmux", ":term:send", ":term:look", ":term:regexp", ":term:plumb":
		return true
	case ":term:write":
		return hasWrite
	case ":ion:pop":
		return hasPop
	default:
		return false
	}
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
	visible := menu.visibleItemIndices()
	for i, idx := range visible {
		item := menu.items[idx]
		if err := writeMenuItem(stdout, row, menu.x, inner, item, menu.hover == idx, menu.running && menu.runningIdx == idx, theme); err != nil {
			return err
		}
		row++
		if item.sepAfter && i < len(visible)-1 {
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

func writeMenuItem(stdout io.Writer, row, col, inner int, item menuItem, hover, running bool, theme *uiTheme) error {
	line := formatMenuItemLine(item, inner)
	if running {
		return writeShimmerMenuLine(stdout, row, col, line, item, hover, theme)
	}
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

func writeShimmerMenuLine(stdout io.Writer, row, col int, line string, item menuItem, hover bool, theme *uiTheme) error {
	if _, err := fmt.Fprintf(stdout, "\x1b[%d;%dH", row+1, col+1); err != nil {
		return err
	}
	runes := []rune(line)
	currentPrefix := ""
	for i, r := range runes {
		if nextPrefix := menuShimmerPrefix(theme, item.current, hover, i, len(runes)); nextPrefix != currentPrefix {
			transition := nextPrefix
			if transition == "" {
				transition = styleReset()
			}
			if _, err := io.WriteString(stdout, transition); err != nil {
				return err
			}
			currentPrefix = nextPrefix
		}
		if _, err := io.WriteString(stdout, string(r)); err != nil {
			return err
		}
	}
	if currentPrefix != "" {
		_, err := io.WriteString(stdout, styleReset())
		return err
	}
	return nil
}

func menuDisplayFileName(name string) string {
	if strings.TrimSpace(name) == "" {
		return "(unnamed)"
	}
	// File names stay canonical in shared state; the menu shortens them only here.
	const maxName = 38 - 5
	label := name
	if len([]rune(label)) > maxName {
		runes := []rune(label)
		label = string(runes[len(runes)-maxName:])
	}
	return label
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

func menuShimmerPrefix(theme *uiTheme, current, hover bool, index, length int) string {
	if theme == nil {
		return shimmerPrefix(nil, index, length)
	}
	bg := theme.subtleBG
	switch {
	case hover:
		bg = theme.cursorBG
	}
	return shimmerPrefixFor(theme, bg, true, index, length)
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
	label := fitMenuItemLabel(item, inner)
	content := label
	if item.shortcut != "" {
		padding := inner - len([]rune(label)) - len([]rune(item.shortcut))
		if padding < 1 {
			padding = 1
		}
		content += strings.Repeat(" ", padding) + item.shortcut
	}
	if pad := inner - len([]rune(content)); pad > 0 {
		content += strings.Repeat(" ", pad)
	}
	return "│" + content + "│"
}

func fitMenuItemLabel(item menuItem, inner int) string {
	label := []rune(item.label)
	limit := inner
	if item.shortcut != "" {
		limit -= len([]rune(item.shortcut)) + 1
	}
	if limit <= 0 || len(label) <= limit {
		return string(label)
	}
	if item.kind == menuFile {
		const filePrefixRunes = 4
		if limit <= filePrefixRunes {
			return string(label[:limit])
		}
		prefix := label[:filePrefixRunes]
		suffix := label[len(label)-(limit-filePrefixRunes):]
		return string(prefix) + string(suffix)
	}
	return string(label[:limit])
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
	visible := m.visibleItemIndices()
	for i, idx := range visible {
		item := m.items[idx]
		if r == row {
			return idx
		}
		r++
		if item.sepAfter && i < len(visible)-1 {
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

func popNavigationAvailable(nav wire.NavigationStack, lastSection bool) bool {
	_, ok := popNavigationMenuItem(nav, lastSection)
	return ok
}

func popNavigationMenuItem(nav wire.NavigationStack, lastSection bool) (menuItem, bool) {
	if nav.Current <= 0 || nav.Current > len(nav.Entries)-1 {
		return menuItem{}, false
	}
	return menuItem{
		label:    " " + nav.Entries[nav.Current-1].Label,
		shortcut: "(P)",
		kind:     menuHistoryPop,
		sepAfter: !lastSection,
	}, true
}

func (m *menuState) visibleItemIndices() []int {
	if m == nil {
		return nil
	}
	all := make([]int, len(m.items))
	for i := range m.items {
		all[i] = i
	}
	return all
}

func (m *menuState) selectedItem() (menuItem, int, bool) {
	if m == nil {
		return menuItem{}, -1, false
	}
	for _, idx := range m.visibleItemIndices() {
		if idx == m.hover && idx >= 0 && idx < len(m.items) {
			return m.items[idx], idx, true
		}
	}
	visible := m.visibleItemIndices()
	if len(visible) == 0 {
		return menuItem{}, -1, false
	}
	idx := visible[0]
	return m.items[idx], idx, true
}

func (m *menuState) move(delta int) bool {
	if m == nil || delta == 0 {
		return false
	}
	if len(m.items) == 0 {
		return false
	}
	if m.hover < 0 || m.hover >= len(m.items) {
		m.hover = 0
		return true
	}
	next := m.hover + delta
	if next < 0 {
		next = 0
	}
	if next >= len(m.items) {
		next = len(m.items) - 1
	}
	if next == m.hover {
		return false
	}
	m.hover = next
	return true
}

func (m *menuState) clampToScreen() {
	if m == nil {
		return
	}
	if m.x < 0 {
		m.x = 0
	}
	if m.y < 0 {
		m.y = 0
	}
	if m.x+m.width > termCols {
		m.x = termCols - m.width
	}
	if m.y+m.height > termRows {
		m.y = termRows - m.height
	}
	if m.x < 0 {
		m.x = 0
	}
	if m.y < 0 {
		m.y = 0
	}
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

func menuCommandShortcutLabel(r rune) string {
	if r == 0 {
		return ""
	}
	return fmt.Sprintf("(M-%c)", r)
}

func menuCommandShortcutRune(shortcut string) (rune, bool) {
	runes := []rune(strings.TrimSpace(shortcut))
	if len(runes) != 1 {
		return 0, false
	}
	r := runes[0]
	if r >= 'A' && r <= 'Z' {
		r = r - 'A' + 'a'
	}
	if r < 'a' || r > 'z' {
		return 0, false
	}
	return r, true
}

func menuCommandByMetaShortcut(commands []wire.MenuCommand, r rune) (wire.MenuCommand, bool) {
	for _, command := range commands {
		shortcut, ok := menuCommandShortcutRune(command.Shortcut)
		if !ok || shortcut != r {
			continue
		}
		return command, true
	}
	return wire.MenuCommand{}, false
}

func menuFileShortcutLabel(index int) string {
	if r, ok := menuFileShortcutRune(index); ok {
		return fmt.Sprintf("(%c)", r)
	}
	return ""
}

func menuFileShortcutRune(index int) (rune, bool) {
	digits := []rune("1234567890")
	if index < 0 || index >= len(digits) {
		return 0, false
	}
	return digits[index], true
}

func menuBuiltinShortcutRune(item menuItem) (rune, bool) {
	switch item.kind {
	case menuWrite:
		return 'w', true
	case menuSplit:
		return 'n', true
	case menuCut:
		return 'x', true
	case menuSnarf:
		return 'c', true
	case menuPaste:
		return 'v', true
	case menuTmux:
		return 't', true
	case menuSend:
		return 's', true
	case menuLook:
		return 'l', true
	case menuPlumb:
		return 'b', true
	case menuRegexp:
		return '/', true
	case menuHistoryPop:
		return 'P', true
	default:
		return 0, false
	}
}

func (m *menuState) itemForShortcut(r rune) (menuItem, int, bool) {
	if m == nil {
		return menuItem{}, -1, false
	}
	for i, item := range m.items {
		if shortcut, ok := menuBuiltinShortcutRune(item); ok && shortcut == r {
			return item, i, true
		}
	}
	fileIndex := 0
	for i, item := range m.items {
		if item.kind != menuFile {
			continue
		}
		if shortcut, ok := menuFileShortcutRune(fileIndex); ok && shortcut == r {
			return item, i, true
		}
		fileIndex++
	}
	return menuItem{}, -1, false
}

func (m *menuState) itemForMetaShortcut(r rune) (menuItem, int, bool) {
	if m == nil {
		return menuItem{}, -1, false
	}
	for i, item := range m.items {
		if item.kind != menuCommand {
			continue
		}
		if item.keyRune == r {
			return item, i, true
		}
	}
	return menuItem{}, -1, false
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

func resolvePlumbTargetToken(state *bufferState, token string) string {
	token = strings.TrimSpace(token)
	if state == nil || token == "" {
		return token
	}
	if _, ok := clienttarget.ParseAddressOnly(token); ok {
		return token
	}
	target := clienttarget.Parse(token)
	if target.Path == "" || filepath.IsAbs(target.Path) {
		return token
	}
	base := strings.TrimSpace(state.path)
	if base == "" {
		base = strings.TrimSpace(state.name)
	}
	if base == "" {
		return token
	}
	if !filepath.IsAbs(base) {
		if abs, err := filepath.Abs(base); err == nil {
			base = abs
		}
	}
	resolved := filepath.Clean(filepath.Join(filepath.Dir(base), target.Path))
	if target.Address == "" {
		return resolved
	}
	return resolved + ":" + target.Address
}

func plumbTargetLineToken(line string) string {
	line = strings.TrimRight(line, "\r\n")
	if strings.TrimSpace(line) == "" {
		return ""
	}
	text := []rune(line)
	cursor := 0
	for cursor < len(text) && text[cursor] <= 0x20 {
		cursor++
	}
	if cursor >= len(text) {
		return ""
	}
	token := plumbToken(&bufferState{
		text:   text,
		cursor: cursor,
	})
	trimmed := strings.TrimRight(token, ":;,.)]!?>-")
	if trimmed != "" {
		token = clienttarget.TrimToken(trimmed)
	}
	return token
}
