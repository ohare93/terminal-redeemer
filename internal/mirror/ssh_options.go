package mirror

import (
	"fmt"
	"strconv"
	"strings"
)

// ValidateSSHOptions accepts only the bounded OpenSSH option grammar used by
// Redeem. It rejects operands and option boundaries so callers can append the
// one authoritative `-- DESTINATION REMOTE_COMMAND` tail themselves.
func ValidateSSHOptions(options []string) error {
	for i := 0; i < len(options); i++ {
		arg := options[i]
		if len(arg) < 2 || arg[0] != '-' || arg == "--" || strings.HasPrefix(arg, "--") {
			return fmt.Errorf("invalid configured SSH option %q", arg)
		}
		body := arg[1:]
		if allSSHFlagOptions(body) {
			continue
		}
		letter := body[0]
		if !sshOptionNeedsArgument(letter) {
			return fmt.Errorf("unsupported configured SSH option %q", arg)
		}
		value := body[1:]
		if value == "" {
			i++
			if i >= len(options) || options[i] == "" || options[i] == "--" {
				return fmt.Errorf("configured SSH option -%c requires an argument", letter)
			}
			value = options[i]
		}
		if err := validateSSHOptionArgument(letter, value); err != nil {
			return err
		}
	}
	return nil
}

func allSSHFlagOptions(body string) bool {
	if body == "" {
		return false
	}
	for _, option := range body {
		if !strings.ContainsRune("46CaqTtvxn", option) {
			return false
		}
	}
	return true
}

func sshOptionNeedsArgument(option byte) bool {
	return strings.ContainsRune("FJilop", rune(option))
}

func validateSSHOptionArgument(option byte, value string) error {
	if value == "" || value == "--" {
		return fmt.Errorf("configured SSH option -%c requires an argument", option)
	}
	if option == 'p' {
		port, err := strconv.Atoi(value)
		if err != nil || port < 1 || port > 65535 {
			return fmt.Errorf("configured SSH option -p has invalid port %q", value)
		}
	}
	return nil
}

func buildSSHArgs(options []string, internalOptions []string, destination string, remoteCommand string) ([]string, error) {
	if err := ValidateSSHOptions(options); err != nil {
		return nil, err
	}
	if err := ValidateDestination(destination); err != nil {
		return nil, err
	}
	if remoteCommand == "" {
		return nil, fmt.Errorf("SSH remote command is empty")
	}
	args := append([]string(nil), options...)
	args = append(args, internalOptions...)
	args = append(args, "--", destination, remoteCommand)
	return args, nil
}
