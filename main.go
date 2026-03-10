package main

import (
	"crypto/rand"
	"embed"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
)

//go:embed all:frontend/dist
var assets embed.FS

func main() {
	args := os.Args[1:]

	switch {
	case len(args) >= 1 && (args[0] == "--version" || args[0] == "-v"):
		fmt.Println(Version)
		os.Exit(0)

	case len(args) >= 1 && args[0] == "--open":
		// Open Wails window, optionally with a specific session
		runGUI(argStr(args, 1), argStr(args, 2))

	case len(args) >= 1 && args[0] == "--watch":
		// Background: grep for token, then open window
		runWatch(argStr(args, 1), argStr(args, 2), argStr(args, 3))

	default:
		if os.Getenv("CLAUDECODE") == "1" {
			// Inside Claude Code: token-based session detection
			runSurgery()
		} else {
			// Standalone: open GUI directly (auto-detect project from cwd)
			projectID := deriveProjectID()
			runGUI(projectID, "")
		}
	}
}

func argStr(args []string, i int) string {
	if i < len(args) {
		return args[i]
	}
	return ""
}

func deriveProjectID() string {
	cwd, _ := os.Getwd()
	return "-" + strings.ReplaceAll(strings.TrimPrefix(cwd, "/"), "/", "-")
}

func runSurgery() {
	b := make([]byte, 8)
	rand.Read(b)
	token := "SURGERY_" + strings.ToUpper(hex.EncodeToString(b))
	fmt.Println(token)

	projectID := deriveProjectID()
	projectDir := filepath.Join(os.Getenv("HOME"), ".claude", "projects", projectID)

	exe, _ := os.Executable()
	cmd := exec.Command(exe, "--watch", token, projectDir, projectID)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	cmd.Start()
	cmd.Process.Release()
	os.Exit(0)
}

func runWatch(token, projectDir, projectID string) {
	// Wait for JSONL to be written with our token (after parent bash exits)
	var jsonlPath string
	for i := 0; i < 10; i++ {
		time.Sleep(500 * time.Millisecond)
		jsonlPath = findJSONLWithToken(token, projectDir)
		if jsonlPath != "" {
			break
		}
	}
	if jsonlPath == "" {
		// Fallback: most recently modified JSONL
		jsonlPath = mostRecentJSONL(projectDir)
	}
	if jsonlPath == "" {
		return
	}
	sessionID := strings.TrimSuffix(filepath.Base(jsonlPath), ".jsonl")

	exe, _ := os.Executable()
	cmd := exec.Command(exe, "--open", projectID, sessionID)
	cmd.Start()
}

func findJSONLWithToken(token, projectDir string) string {
	entries, err := os.ReadDir(projectDir)
	if err != nil {
		return ""
	}
	for _, e := range entries {
		if filepath.Ext(e.Name()) != ".jsonl" {
			continue
		}
		path := filepath.Join(projectDir, e.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		if strings.Contains(string(data), token) {
			return path
		}
	}
	return ""
}

func mostRecentJSONL(projectDir string) string {
	entries, err := os.ReadDir(projectDir)
	if err != nil {
		return ""
	}
	var latest string
	var latestTime time.Time
	for _, e := range entries {
		if filepath.Ext(e.Name()) != ".jsonl" {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		if info.ModTime().After(latestTime) {
			latestTime = info.ModTime()
			latest = filepath.Join(projectDir, e.Name())
		}
	}
	return latest
}

func runGUI(startupProject, startupSession string) {
	app := NewApp()
	app.startupProject = startupProject
	app.startupSession = startupSession

	err := wails.Run(&options.App{
		Title:  "Surgery",
		Width:  1400,
		Height: 900,
		AssetServer: &assetserver.Options{
			Assets: assets,
		},
		BackgroundColour: &options.RGBA{R: 13, G: 17, B: 23, A: 1},
		OnStartup:        app.startup,
		Bind: []interface{}{
			app,
		},
	})
	if err != nil {
		println("Error:", err.Error())
	}
}
