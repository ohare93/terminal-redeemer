package procmeta

import (
	"regexp"
	"sort"
	"strings"

	"github.com/jmo/terminal-redeemer/internal/model"
)

type Reader interface {
	Inspect(pid int) (ProcessInfo, error)
}

type ProcessInfo struct {
	CWD          string
	ProcessChain []string
	Args         []string
	Env          map[string]string
}

type Config struct {
	Whitelist         []string
	WhitelistExtra    []string
	IncludeSessionTag bool
}

type Enricher struct {
	reader    Reader
	whitelist map[string]struct{}
	config    Config
	verifier  SessionVerifier
}

func NewEnricher(reader Reader, config Config) *Enricher {
	return NewEnricherWithVerifier(reader, config, NewZellijSessionVerifier(nil))
}

func NewEnricherWithVerifier(reader Reader, config Config, verifier SessionVerifier) *Enricher {
	whitelist := map[string]struct{}{
		"opencode": {},
		"claude":   {},
	}
	for _, p := range config.Whitelist {
		whitelist[strings.ToLower(strings.TrimSpace(p))] = struct{}{}
	}
	for _, p := range config.WhitelistExtra {
		whitelist[strings.ToLower(strings.TrimSpace(p))] = struct{}{}
	}

	return &Enricher{reader: reader, whitelist: whitelist, config: config, verifier: verifier}
}

func (e *Enricher) EnrichWindow(window model.Window) (model.Window, error) {
	if !isTerminal(window.AppID) {
		return window, nil
	}
	if window.PID <= 0 {
		return window, nil
	}

	info, err := e.reader.Inspect(window.PID)
	if err != nil {
		return model.Window{}, err
	}

	out := window
	terminal := model.Terminal{}
	if strings.TrimSpace(info.CWD) != "" {
		terminal.CWD = info.CWD
	}
	tags := e.filterTags(info.ProcessChain)
	if len(tags) > 0 {
		terminal.ProcessTags = tags
	}
	if e.config.IncludeSessionTag {
		terminal.SessionTag = e.extractSessionTag(window.Title, info)
	}

	if terminal.CWD != "" || len(terminal.ProcessTags) > 0 || terminal.SessionTag != "" {
		out.Terminal = &terminal
	}

	return out, nil
}

func (e *Enricher) extractSessionTag(windowTitle string, info ProcessInfo) string {
	if session := extractSessionTagFromProcess(info); session != "" {
		return session
	}

	candidate := extractSessionTagFromTitle(windowTitle)
	if candidate == "" || e.verifier == nil {
		return ""
	}

	ok, err := e.verifier.Exists(candidate)
	if err != nil || !ok {
		return ""
	}
	return candidate
}

func (e *Enricher) filterTags(chain []string) []string {
	set := make(map[string]struct{})
	for _, proc := range chain {
		name := strings.ToLower(strings.TrimSpace(proc))
		if _, ok := e.whitelist[name]; ok {
			set[name] = struct{}{}
		}
	}
	out := make([]string, 0, len(set))
	for tag := range set {
		out = append(out, tag)
	}
	sort.Strings(out)
	return out
}

func isTerminal(appID string) bool {
	switch strings.ToLower(strings.TrimSpace(appID)) {
	case "kitty", "alacritty", "foot", "wezterm":
		return true
	default:
		return false
	}
}

var titleSessionPattern = regexp.MustCompile(`\[session:([^\]]+)\]`)
var titlePrefixPattern = regexp.MustCompile(`^([^|]+)\s+\|`)

func extractSessionTagFromProcess(info ProcessInfo) string {
	if session := strings.TrimSpace(info.Env["ZELLIJ_SESSION_NAME"]); session != "" {
		return session
	}

	for i := range info.Args {
		arg := info.Args[i]
		if (arg == "--session" || arg == "-s" || arg == "attach") && i+1 < len(info.Args) {
			next := strings.TrimSpace(info.Args[i+1])
			if next != "" && !strings.HasPrefix(next, "-") {
				return next
			}
		}
	}

	return ""
}

func extractSessionTagFromTitle(windowTitle string) string {
	match := titleSessionPattern.FindStringSubmatch(windowTitle)
	if len(match) > 1 {
		return strings.TrimSpace(match[1])
	}

	prefix := titlePrefixPattern.FindStringSubmatch(windowTitle)
	if len(prefix) > 1 {
		candidate := strings.TrimSpace(prefix[1])
		if looksLikeSessionName(candidate) {
			return candidate
		}
	}

	return ""
}

func looksLikeSessionName(candidate string) bool {
	if candidate == "" {
		return false
	}
	for _, r := range candidate {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' || r == '.' {
			continue
		}
		return false
	}
	return true
}
