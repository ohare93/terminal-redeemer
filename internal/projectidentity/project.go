package projectidentity

import (
	"io"
	"math"
	"os"
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf16"
)

const maxRepoPointerBytes = 4096

type RGB struct {
	R uint8 `json:"r"`
	G uint8 `json:"g"`
	B uint8 `json:"b"`
}

type Segment struct {
	Label      string `json:"label"`
	Background RGB    `json:"background"`
	Foreground RGB    `json:"foreground"`
}

type identityPart struct {
	label string
	path  string
}

// Resolve derives the same source-host project/workspace identity and stable
// palette used by Mono's project footer. It emits display data only: backing
// paths and .jj pointer contents never cross the snapshot boundary.
func Resolve(cwd string) []Segment {
	cwd = strings.TrimSpace(cwd)
	if cwd == "" {
		return nil
	}
	resolved, err := filepath.Abs(filepath.Clean(cwd))
	if err != nil {
		return nil
	}
	parts := identityParts(resolved)
	segments := make([]Segment, 0, len(parts))
	for _, part := range parts {
		label := sanitizeLabel(part.label)
		if label == "" {
			continue
		}
		background, foreground := Palette(part.path)
		segments = append(segments, Segment{Label: label, Background: background, Foreground: foreground})
	}
	return segments
}

func identityParts(cwd string) []identityPart {
	parts := strings.Split(filepath.Clean(cwd), string(os.PathSeparator))
	workspaceIndex := -1
	developmentIndex := -1
	for i, part := range parts {
		switch part {
		case "workspaces":
			workspaceIndex = i
		case "Development":
			developmentIndex = i
		}
	}
	if workspaceIndex >= 0 && workspaceIndex+2 < len(parts) {
		projectName := parts[workspaceIndex+1]
		workspaceName := parts[workspaceIndex+2]
		if projectName != "" && workspaceName != "" {
			workspacePath := joinPathParts(parts[:workspaceIndex+3], filepath.IsAbs(cwd))
			repositoryPath, ok := readRepositoryRoot(workspacePath)
			if ok {
				return []identityPart{
					{label: developmentPathLabel(repositoryPath), path: repositoryPath},
					{label: workspaceName, path: workspacePath},
				}
			}
			return []identityPart{
				{label: projectName, path: filepath.Dir(workspacePath)},
				{label: workspaceName, path: workspacePath},
			}
		}
	}
	label := filepath.Base(cwd)
	if developmentIndex >= 0 && developmentIndex+1 < len(parts) {
		label = filepath.Join(parts[developmentIndex+1:]...)
	}
	if label == "." || label == string(os.PathSeparator) || label == "" {
		label = cwd
	}
	return []identityPart{{label: label, path: cwd}}
}

func developmentPathLabel(path string) string {
	parts := strings.Split(filepath.Clean(path), string(os.PathSeparator))
	index := -1
	for i, part := range parts {
		if part == "Development" {
			index = i
		}
	}
	if index >= 0 && index+1 < len(parts) {
		return filepath.Join(parts[index+1:]...)
	}
	if base := filepath.Base(path); base != "" && base != "." && base != string(os.PathSeparator) {
		return base
	}
	return path
}

func joinPathParts(parts []string, absolute bool) string {
	joined := filepath.Join(parts...)
	if absolute && !filepath.IsAbs(joined) {
		joined = string(os.PathSeparator) + joined
	}
	return filepath.Clean(joined)
}

func readRepositoryRoot(workspacePath string) (string, bool) {
	jjDir := filepath.Join(workspacePath, ".jj")
	file, err := os.Open(filepath.Join(jjDir, "repo"))
	if err != nil {
		return "", false
	}
	defer file.Close()
	payload, err := io.ReadAll(io.LimitReader(file, maxRepoPointerBytes+1))
	if err != nil || len(payload) > maxRepoPointerBytes {
		return "", false
	}
	pointer := strings.TrimSpace(string(payload))
	if pointer == "" || strings.ContainsRune(pointer, 0) || strings.ContainsAny(pointer, "\r\n") {
		return "", false
	}
	target := pointer
	if !filepath.IsAbs(target) {
		target = filepath.Join(jjDir, target)
	}
	target = filepath.Clean(target)
	info, err := os.Stat(target)
	if err != nil || !info.IsDir() || filepath.Base(target) != "repo" || filepath.Base(filepath.Dir(target)) != ".jj" {
		return "", false
	}
	return filepath.Dir(filepath.Dir(target)), true
}

func sanitizeLabel(value string) string {
	var out strings.Builder
	for _, r := range strings.TrimSpace(value) {
		if unicode.IsControl(r) {
			continue
		}
		out.WriteRune(r)
	}
	return strings.TrimSpace(out.String())
}

// Palette is behavior-compatible with Mono's JavaScript project footer,
// including FNV-1a over UTF-16 code units rather than UTF-8 bytes.
func Palette(key string) (RGB, RGB) {
	resolved, err := filepath.Abs(filepath.Clean(key))
	if err == nil {
		key = resolved
	}
	hash := uint32(0x811c9dc5)
	for _, unit := range utf16.Encode([]rune(key)) {
		hash ^= uint32(unit)
		hash *= 0x01000193
	}
	hue := int(hash % 360)
	saturation := 68 + int((hash>>8)%16)
	lightness := 36 + int((hash>>16)%10)
	background := hslToRGB(hue, saturation, lightness)
	foreground := RGB{R: 255, G: 255, B: 255}
	if relativeLuminance(background) > 0.42 {
		foreground = RGB{}
	}
	return background, foreground
}

func hslToRGB(hue, saturation, lightness int) RGB {
	h := math.Mod(float64(hue), 360)
	if h < 0 {
		h += 360
	}
	s := float64(saturation) / 100
	l := float64(lightness) / 100
	chroma := (1 - math.Abs(2*l-1)) * s
	x := chroma * (1 - math.Abs(math.Mod(h/60, 2)-1))
	match := l - chroma/2
	var r, g, b float64
	switch {
	case h < 60:
		r, g = chroma, x
	case h < 120:
		r, g = x, chroma
	case h < 180:
		g, b = chroma, x
	case h < 240:
		g, b = x, chroma
	case h < 300:
		r, b = x, chroma
	default:
		r, b = chroma, x
	}
	return RGB{R: uint8(math.Round((r + match) * 255)), G: uint8(math.Round((g + match) * 255)), B: uint8(math.Round((b + match) * 255))}
}

func relativeLuminance(color RGB) float64 {
	linear := func(channel uint8) float64 {
		value := float64(channel) / 255
		if value <= 0.03928 {
			return value / 12.92
		}
		return math.Pow((value+0.055)/1.055, 2.4)
	}
	return 0.2126*linear(color.R) + 0.7152*linear(color.G) + 0.0722*linear(color.B)
}
