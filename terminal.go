package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

func projectIDToPath(id string) string {
	return "/" + strings.ReplaceAll(strings.TrimPrefix(id, "-"), "-", "/")
}

// BuildClaudeCommand returns the shell command string for launching claude.
func BuildClaudeCommand(projectID, sessionID string, skipPermissions bool) string {
	dir := projectIDToPath(projectID)
	claudeCmd := "claude"
	if skipPermissions {
		claudeCmd = "claude --dangerously-skip-permissions"
	}
	if sessionID != "" {
		claudeCmd += " --resume " + sessionID
	}
	return fmt.Sprintf("cd %s && %s", dir, claudeCmd)
}

// GetClaudeCommand returns the command string so the frontend can display/copy it.
func (a *App) GetClaudeCommand(projectID, sessionID string, skipPermissions bool) string {
	return BuildClaudeCommand(projectID, sessionID, skipPermissions)
}

type terminalLauncher func(script string) error

var terminalLaunchers = map[string]terminalLauncher{
	"Terminal": func(script string) error {
		return exec.Command("osascript",
			"-e", `tell application "Terminal" to activate`,
			"-e", fmt.Sprintf(`tell application "Terminal" to do script %q`, script),
		).Run()
	},
	"iTerm2": func(script string) error {
		apple := fmt.Sprintf(`tell application "iTerm"
	activate
	set newWindow to (create window with default profile)
	tell current session of newWindow
		write text %q
	end tell
end tell`, script)
		return exec.Command("osascript", "-e", apple).Run()
	},
	"Ghostty": func(script string) error {
		return exec.Command("open", "-na", "Ghostty", "--args", "-e", "sh", "-c", script).Run()
	},
	"Alacritty": func(script string) error {
		return exec.Command("alacritty", "-e", "sh", "-c", script).Start()
	},
	"Kitty": func(script string) error {
		return exec.Command("kitty", "sh", "-c", script).Start()
	},
	"WezTerm": func(script string) error {
		return exec.Command("wezterm", "start", "--", "sh", "-c", script).Start()
	},
}

// GetAvailableTerminals returns terminal names that are installed.
func (a *App) GetAvailableTerminals() []string {
	appBundles := map[string]string{
		"iTerm2":    "iTerm.app",
		"Ghostty":   "Ghostty.app",
		"Alacritty": "Alacritty.app",
		"Kitty":     "kitty.app",
		"WezTerm":   "WezTerm.app",
	}
	cliNames := map[string]string{
		"Ghostty":   "ghostty",
		"Alacritty": "alacritty",
		"Kitty":     "kitty",
		"WezTerm":   "wezterm",
	}

	available := []string{"Terminal"}
	seen := map[string]bool{"Terminal": true}

	for name, bundle := range appBundles {
		if seen[name] {
			continue
		}
		if _, err := os.Stat(filepath.Join("/Applications", bundle)); err == nil {
			available = append(available, name)
			seen[name] = true
		}
	}
	for name, cli := range cliNames {
		if seen[name] {
			continue
		}
		if _, err := exec.LookPath(cli); err == nil {
			available = append(available, name)
			seen[name] = true
		}
	}
	sort.Strings(available)
	return available
}

func (a *App) ExecClaude(projectID string, sessionID string, skipPermissions bool, terminal string) error {
	script := BuildClaudeCommand(projectID, sessionID, skipPermissions)
	launcher, ok := terminalLaunchers[terminal]
	if !ok {
		launcher = terminalLaunchers["Terminal"]
	}
	return launcher(script)
}
