package target

import (
	"os"
	"regexp"
	"strconv"
	"strings"

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
	if path, addr, ok := splitSearchSuffix(arg); ok {
		return Target{Path: path, Address: addr}
	}
	if path, addr, ok := splitLineSuffix(arg); ok {
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
	if view, ok, err := openAddressedTarget(svc, targets, view); ok || err != nil {
		return view, err
	}
	if id, ok := findMenuFileID(menu, targets[len(targets)-1].Path); ok {
		view, err = svc.FocusFile(id)
		if err != nil {
			return wire.BufferView{}, err
		}
	}
	return ApplyLastAddress(svc, targets, view)
}

func openAddressedTarget(svc AddressService, targets []Target, current wire.BufferView) (wire.BufferView, bool, error) {
	if len(targets) == 0 {
		return current, false, nil
	}
	last := targets[len(targets)-1]
	if last.Path == "" || last.Address == "" {
		return current, false, nil
	}
	view, err := svc.SetAddress(quotedFileAddress(last.Path, last.Address))
	return view, true, err
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

func quotedFileAddress(path, address string) string {
	escaped := regexp.QuoteMeta(path)
	escaped = strings.ReplaceAll(escaped, `"`, `\"`)
	return `"` + escaped + `"` + address
}

func splitSearchSuffix(arg string) (string, string, bool) {
	last := strings.LastIndexByte(arg, ':')
	if last <= 0 || last+1 >= len(arg) {
		return "", "", false
	}
	switch arg[last+1] {
	case '/', '?':
		return arg[:last], arg[last+1:], true
	default:
		return "", "", false
	}
}

func splitLineSuffix(arg string) (string, string, bool) {
	last := strings.LastIndexByte(arg, ':')
	if last <= 0 || last+1 >= len(arg) {
		return "", "", false
	}
	line, err := strconv.Atoi(arg[last+1:])
	if err != nil {
		return "", "", false
	}
	base := arg[:last]
	col := 0
	prev := strings.LastIndexByte(base, ':')
	if prev > 0 {
		n, err := strconv.Atoi(base[prev+1:])
		if err == nil {
			col = line
			line = n
			base = base[:prev]
		}
	}
	if base == "" {
		return "", "", false
	}
	addr := strconv.Itoa(line)
	if col > 1 {
		addr += "+#" + strconv.Itoa(col-1)
	}
	return base, addr, true
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
