package target

import (
	"os"
	"strconv"
	"strings"

	ionaddr "ion/internal/core/addr"
	"ion/internal/core/cmdlang"
	ionregexp "ion/internal/core/regexp"
	"ion/internal/proto/wire"
)

// Target is one client-side open request, optionally followed by a sam address.
type Target struct {
	Path    string
	Address string
}

// Service is the client-side surface needed to open addressed targets.
type Service interface {
	MenuFiles() ([]wire.MenuFile, error)
	FocusFile(id int) (wire.BufferView, error)
	OpenFiles(files []string) (wire.BufferView, error)
	OpenTarget(path, address string) (wire.BufferView, error)
	SetAddress(expr string) (wire.BufferView, error)
}

// AddressService is the subset needed after bootstrap has already opened files.
type AddressService interface {
	SetAddress(expr string) (wire.BufferView, error)
}

// Parse converts one external token like file.go:12 or file.go:/^func into a target.
func Parse(arg string) Target {
	if arg == "" {
		return Target{}
	}
	if literalPathExists(arg) {
		return Target{Path: arg}
	}
	if path, addr, ok := splitAddressSuffix(arg); ok {
		return Target{Path: path, Address: addr}
	}
	return Target{Path: arg}
}

// ParseAll converts a file list into addressed targets.
func ParseAll(args []string) []Target {
	targets := make([]Target, 0, len(args))
	for _, arg := range args {
		targets = append(targets, Parse(arg))
	}
	return targets
}

// Paths returns the literal file paths to open for the target list.
func Paths(targets []Target) []string {
	paths := make([]string, 0, len(targets))
	for _, target := range targets {
		paths = append(paths, target.Path)
	}
	return paths
}

// Open opens all target paths, then applies the final target address if present.
func Open(svc Service, args []string) (wire.BufferView, error) {
	targets := ParseAll(args)
	if len(targets) == 0 {
		return wire.BufferView{}, nil
	}

	menu, err := svc.MenuFiles()
	if err != nil {
		return wire.BufferView{}, err
	}
	loaded := menuIDsByName(menu)
	missing := missingPaths(targets, loaded)
	last := targets[len(targets)-1]

	if last.Address != "" {
		toOpen := missingWithoutLast(missing, last.Path)
		var view wire.BufferView
		if len(toOpen) > 0 {
			var err error
			view, err = svc.OpenFiles(toOpen)
			if err != nil {
				return wire.BufferView{}, err
			}
		}
		_ = view
		return svc.OpenTarget(last.Path, last.Address)
	}

	var view wire.BufferView
	if len(missing) > 0 {
		view, err = svc.OpenFiles(missing)
		if err != nil {
			return wire.BufferView{}, err
		}
		menu, err = svc.MenuFiles()
		if err != nil {
			return wire.BufferView{}, err
		}
	}
	if id, ok := findMenuFileID(menu, targets[len(targets)-1].Path); ok {
		view, err = svc.FocusFile(id)
		if err != nil {
			return wire.BufferView{}, err
		}
	}
	return ApplyLastAddress(svc, targets, view)
}

// ApplyLastAddress updates the current file selection to the final target address.
func ApplyLastAddress(svc AddressService, targets []Target, current wire.BufferView) (wire.BufferView, error) {
	if len(targets) == 0 {
		return current, nil
	}
	last := targets[len(targets)-1]
	if last.Address == "" {
		return current, nil
	}
	return svc.SetAddress(last.Address)
}

// TrimToken returns the longest prefix of one whitespace-free token that parses
// as an addressed file target. When no addressed prefix is found, it returns
// the trimmed token unchanged.
func TrimToken(arg string) string {
	arg = strings.TrimSpace(arg)
	if arg == "" || literalPathExists(arg) {
		return arg
	}
	for end := len(arg); end > 0; end-- {
		prefix := arg[:end]
		if _, _, ok := splitAddressSuffix(prefix); ok {
			return prefix
		}
	}
	return arg
}

func splitAddressSuffix(arg string) (string, string, bool) {
	for i := 0; i < len(arg); i++ {
		if arg[i] != ':' {
			continue
		}
		base := arg[:i]
		suffix := arg[i+1:]
		if base == "" || suffix == "" {
			continue
		}
		addr, ok := normalizeAddressSuffix(suffix)
		if !ok {
			continue
		}
		return base, addr, true
	}
	return "", "", false
}

func normalizeAddressSuffix(suffix string) (string, bool) {
	if suffix == "" || hasWhitespace(suffix) {
		return "", false
	}
	if addr, ok := normalizeLegacyLineColumn(suffix); ok {
		return addr, true
	}
	if isValidAddressExpr(suffix) {
		return suffix, true
	}
	return "", false
}

func normalizeLegacyLineColumn(suffix string) (string, bool) {
	last := strings.LastIndexByte(suffix, ':')
	if last <= 0 || last+1 >= len(suffix) {
		if _, err := strconv.Atoi(suffix); err == nil {
			return suffix, true
		}
		return "", false
	}
	line, err := strconv.Atoi(suffix[:last])
	if err != nil {
		return "", false
	}
	col, err := strconv.Atoi(suffix[last+1:])
	if err != nil {
		return "", false
	}
	addr := strconv.Itoa(line)
	if col > 1 {
		addr += "+#" + strconv.Itoa(col-1)
	}
	return addr, true
}

func isValidAddressExpr(expr string) bool {
	parser := cmdlang.NewParser(expr + "\n")
	cmd, err := parser.Parse()
	if err != nil || cmd == nil {
		return false
	}
	return cmd.Cmdc == '\n' && cmd.Addr != nil && validateAddr(cmd.Addr)
}

func validateAddr(a *ionaddr.Addr) bool {
	for a != nil {
		switch a.Type {
		case '/', '?', '"':
			if a.Re == nil {
				return false
			}
			if _, err := ionregexp.Compile(a.Re); err != nil {
				return false
			}
		case ',', ';':
			if !validateAddr(a.Left) {
				return false
			}
		}
		a = a.Next
	}
	return true
}

func hasWhitespace(s string) bool {
	for _, r := range s {
		switch r {
		case ' ', '\t', '\n', '\r', '\f', '\v':
			return true
		}
	}
	return false
}

func literalPathExists(path string) bool {
	if path == "" {
		return false
	}
	_, err := os.Stat(path)
	return err == nil
}

func menuIDsByName(menu []wire.MenuFile) map[string]int {
	ids := make(map[string]int, len(menu))
	for _, file := range menu {
		if file.Name == "" {
			continue
		}
		if _, ok := ids[file.Name]; !ok || file.Current {
			ids[file.Name] = file.ID
		}
	}
	return ids
}

func missingPaths(targets []Target, loaded map[string]int) []string {
	missing := make([]string, 0, len(targets))
	queued := make(map[string]struct{}, len(targets))
	for _, target := range targets {
		if target.Path == "" {
			continue
		}
		if _, ok := loaded[target.Path]; ok {
			continue
		}
		if _, ok := queued[target.Path]; ok {
			continue
		}
		missing = append(missing, target.Path)
		queued[target.Path] = struct{}{}
	}
	return missing
}

func missingWithoutLast(missing []string, lastPath string) []string {
	if len(missing) == 0 {
		return nil
	}
	out := make([]string, 0, len(missing))
	for _, path := range missing {
		if path == lastPath {
			continue
		}
		out = append(out, path)
	}
	return out
}

func findMenuFileID(menu []wire.MenuFile, path string) (int, bool) {
	var first int
	found := false
	for _, file := range menu {
		if file.Name != path {
			continue
		}
		if file.Current {
			return file.ID, true
		}
		if !found {
			first = file.ID
			found = true
		}
	}
	return first, found
}
