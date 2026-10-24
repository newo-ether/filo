package service

import (
	"errors"
	"strings"
)

// Retain validation for legacy owner configuration without creating new wrappers.
func validateSharedCLIArguments(arguments []string) error {
	_, err := parseSharedCLIArguments(arguments)
	return err
}

// Only the first positional argument selects a native subcommand. Later words
// belong to a prompt or a resume/fork identity, even when they name a command.
func parseSharedCLIArguments(arguments []string) ([]string, error) {
	values := map[string]bool{"-c": true, "--config": true, "--enable": true, "--disable": true,
		"-m": true, "--model": true, "-C": true, "--cd": true, "-i": true, "--image": true,
		"-s": true, "--sandbox": true, "-a": true, "--ask-for-approval": true,
		"--local-provider": true, "--add-dir": true}
	commands := map[string]bool{"exec": true, "e": true, "review": true, "login": true,
		"logout": true, "mcp": true, "plugin": true, "mcp-server": true, "app-server": true,
		"remote-control": true, "app": true, "completion": true, "update": true, "doctor": true,
		"sandbox": true, "debug": true, "apply": true, "a": true, "archive": true,
		"delete": true, "unarchive": true, "cloud": true, "exec-server": true, "features": true,
		"help": true}
	result := []string{}
	positional := false
	for i := 0; i < len(arguments); i++ {
		argument := arguments[i]
		if argument == "--" {
			break
		}
		key, _, inline := strings.Cut(argument, "=")
		if key == "--remote" || key == "--remote-auth-token-env" {
			return nil, errors.New("legacy shared owner manages the native endpoint")
		}
		if key == "--profile" || strings.HasPrefix(argument, "-p") && !strings.HasPrefix(argument, "--") {
			return nil, errors.New("legacy shared owner does not support native profile overrides")
		}
		config := key == "-c" || key == "--config" || key == "--enable" || key == "--disable"
		if values[key] {
			if config {
				result = append(result, argument)
			}
			if !inline {
				if i+1 >= len(arguments) {
					return nil, errors.New("Missing native CLI option value")
				}
				i++
				if config {
					result = append(result, arguments[i])
				}
			}
			continue
		}
		if argument == "--strict-config" || strings.HasPrefix(argument, "-c") && !strings.HasPrefix(argument, "--") {
			result = append(result, argument)
		}
		if !strings.HasPrefix(argument, "-") && !positional {
			positional = true
			if commands[argument] {
				return nil, errors.New("legacy shared owner supports interactive sessions, resume and fork only")
			}
		}
	}
	return result, nil
}

func sharedCLIConfigArguments(arguments []string) []string {
	result, _ := parseSharedCLIArguments(arguments)
	return result
}
