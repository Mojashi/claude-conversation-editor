package main

import (
	"context"
	"crypto/rand"
	"embed"
	"encoding/hex"
	"fmt"
	"io"
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

	case len(args) >= 1 && args[0] == "update":
		runUpdate()

	case len(args) >= 1 && args[0] == "compact":
		runCompactCLI(args[1:])

	case len(args) >= 1 && args[0] == "--open":
		// Open Wails window, optionally with a specific session
		runGUI(argStr(args, 1), argStr(args, 2))

	case len(args) >= 1 && args[0] == "--watch":
		// Background: grep for token, then open window
		runWatch(argStr(args, 1), argStr(args, 2), argStr(args, 3))

	case len(args) >= 1 && args[0] == "--compact-run":
		// Background: grep for token, then run compaction
		runCompactBackground(args[1:])

	case len(args) >= 1 && args[0] == "--notify":
		// Show a notification popup (title from arg, message from stdin)
		title := argStr(args, 1)
		if title == "" {
			title = "Surgery"
		}
		msg, _ := io.ReadAll(os.Stdin)
		runNotifyWindow(title, string(msg))

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

	projectsBase := filepath.Join(os.Getenv("HOME"), ".claude", "projects")

	exe, _ := os.Executable()
	cmd := exec.Command(exe, "--watch", token, projectsBase)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	cmd.Start()
	cmd.Process.Release()
	os.Exit(0)
}

func runWatch(token, projectsBase, _ string) {
	// Search all project directories for the JSONL containing our token
	var jsonlPath, foundProjectID string
	for i := 0; i < 10; i++ {
		time.Sleep(500 * time.Millisecond)
		jsonlPath, foundProjectID = findJSONLWithTokenAllProjects(token, projectsBase)
		if jsonlPath != "" {
			break
		}
	}
	if jsonlPath == "" {
		return
	}
	sessionID := strings.TrimSuffix(filepath.Base(jsonlPath), ".jsonl")

	exe, _ := os.Executable()
	cmd := exec.Command(exe, "--open", foundProjectID, sessionID)
	cmd.Start()
}

func findJSONLWithTokenAllProjects(token, projectsBase string) (string, string) {
	projects, err := os.ReadDir(projectsBase)
	if err != nil {
		return "", ""
	}
	var bestPath, bestProject string
	var bestTime time.Time
	cutoff := time.Now().Add(-30 * time.Second)
	for _, p := range projects {
		if !p.IsDir() {
			continue
		}
		projectDir := filepath.Join(projectsBase, p.Name())
		entries, err := os.ReadDir(projectDir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if filepath.Ext(e.Name()) != ".jsonl" {
				continue
			}
			info, err := e.Info()
			if err != nil || info.ModTime().Before(cutoff) {
				continue
			}
			path := filepath.Join(projectDir, e.Name())
			f, err := os.Open(path)
			if err != nil {
				continue
			}
			const tailSize = 64 * 1024
			fi, _ := f.Stat()
			offset := fi.Size() - tailSize
			if offset < 0 {
				offset = 0
			}
			buf := make([]byte, tailSize)
			n, _ := f.ReadAt(buf, offset)
			f.Close()
			if strings.Contains(string(buf[:n]), token) && info.ModTime().After(bestTime) {
				bestPath = path
				bestProject = p.Name()
				bestTime = info.ModTime()
			}
		}
	}
	return bestPath, bestProject
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

// mostRecentJSONLAllProjects finds the most recently modified JSONL across all project directories.
// Returns (path, projectID).
func mostRecentJSONLAllProjects(projectsBase string) (string, string) {
	projects, err := os.ReadDir(projectsBase)
	if err != nil {
		return "", ""
	}
	var bestPath, bestProject string
	var bestTime time.Time
	for _, p := range projects {
		if !p.IsDir() {
			continue
		}
		projectDir := filepath.Join(projectsBase, p.Name())
		entries, err := os.ReadDir(projectDir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if filepath.Ext(e.Name()) != ".jsonl" {
				continue
			}
			info, err := e.Info()
			if err != nil {
				continue
			}
			if info.ModTime().After(bestTime) {
				bestTime = info.ModTime()
				bestPath = filepath.Join(projectDir, e.Name())
				bestProject = p.Name()
			}
		}
	}
	return bestPath, bestProject
}

func runUpdate() {
	fmt.Printf("surgery %s\n", Version)
	fmt.Println("Checking for updates...")

	app := &App{}
	info, err := app.CheckUpdate()
	if err != nil {
		fmt.Fprintln(os.Stderr, "Error:", err)
		os.Exit(1)
	}

	fmt.Printf("Current: v%s  Latest: v%s\n", info.CurrentVersion, info.LatestVersion)

	if !info.HasUpdate {
		fmt.Println("Already up to date.")
		return
	}
	if info.DownloadURL == "" {
		fmt.Fprintln(os.Stderr, "No download URL found for this platform.")
		os.Exit(1)
	}

	fmt.Printf("Downloading v%s...\n", info.LatestVersion)
	if err := cliUpdate(info.DownloadURL, info.LatestVersion); err != nil {
		fmt.Fprintln(os.Stderr, "Update failed:", err)
		os.Exit(1)
	}
}

func cliUpdate(downloadURL, newVersion string) error {
	tmpZip := filepath.Join(os.TempDir(), "surgery-update.zip")
	if err := downloadFile(downloadURL, tmpZip); err != nil {
		return fmt.Errorf("download: %w", err)
	}

	tmpDir := filepath.Join(os.TempDir(), "surgery-update")
	os.RemoveAll(tmpDir)
	os.MkdirAll(tmpDir, 0755)
	if err := unzip(tmpZip, tmpDir); err != nil {
		return fmt.Errorf("unzip: %w", err)
	}

	matches, _ := filepath.Glob(filepath.Join(tmpDir, "*.app"))
	if len(matches) == 0 {
		return fmt.Errorf("no .app found in zip")
	}
	newApp := matches[0]

	// Resolve current exe (follow symlink)
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}

	// Walk up to find .app bundle
	appBundle := exe
	for !strings.HasSuffix(appBundle, ".app") {
		parent := filepath.Dir(appBundle)
		if parent == appBundle {
			// Not inside a .app — just replace the binary directly
			appBundle = exe
			newBin := filepath.Join(newApp, "Contents", "MacOS", "surgery")
			if err := exec.Command("cp", newBin, appBundle).Run(); err != nil {
				return fmt.Errorf("replace binary: %w", err)
			}
			fmt.Printf("Updated to v%s. Run surgery again.\n", newVersion)
			return nil
		}
		appBundle = parent
	}

	// Replace entire .app bundle
	os.RemoveAll(appBundle)
	if err := exec.Command("cp", "-r", newApp, appBundle).Run(); err != nil {
		return fmt.Errorf("replace .app: %w", err)
	}
	fmt.Printf("Updated to v%s. Run surgery again.\n", newVersion)
	return nil
}

func runCompactCLI(args []string) {
	dryRun := false
	var ruleNames []string
	var sessionID string

	// Parse flags
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--dry-run":
			dryRun = true
		case "--rules":
			if i+1 < len(args) {
				i++
				ruleNames = strings.Split(args[i], ",")
			}
		case "--help", "-h":
			printCompactUsage()
			os.Exit(0)
		default:
			if !strings.HasPrefix(args[i], "-") && sessionID == "" {
				sessionID = args[i]
			}
		}
	}

	// If explicit session ID given, run directly
	if sessionID != "" {
		projectsBase := filepath.Join(os.Getenv("HOME"), ".claude", "projects")
		jsonlPath := findSessionByID(projectsBase, sessionID)
		if jsonlPath == "" {
			fmt.Fprintln(os.Stderr, "Error: session not found:", sessionID)
			os.Exit(1)
		}
		runCompactOnFile(jsonlPath, ruleNames, dryRun)
		return
	}

	// Inside Claude Code: token + background process, notify on completion
	if os.Getenv("CLAUDECODE") == "1" {
		b := make([]byte, 8)
		rand.Read(b)
		token := "SURGERY_COMPACT_" + strings.ToUpper(hex.EncodeToString(b))

		bgArgs := []string{"--compact-run", token}
		if dryRun {
			bgArgs = append(bgArgs, "--dry-run")
		}
		if len(ruleNames) > 0 {
			bgArgs = append(bgArgs, "--rules", strings.Join(ruleNames, ","))
		}

		exe, _ := os.Executable()
		cmd := exec.Command(exe, bgArgs...)
		cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
		cmd.Start()
		cmd.Process.Release()

		fmt.Println(token)
		fmt.Println("Compaction running in background. You'll get a notification when done.")
		os.Exit(0)
	}

	// Standalone: most recent session in cwd-derived project
	projectID := deriveProjectID()
	projectDir := filepath.Join(filepath.Join(os.Getenv("HOME"), ".claude", "projects"), projectID)
	jsonlPath := mostRecentJSONL(projectDir)
	if jsonlPath == "" {
		fmt.Fprintln(os.Stderr, "Error: no session found for current directory.")
		os.Exit(1)
	}
	runCompactOnFile(jsonlPath, ruleNames, dryRun)
}

// runCompactBackground is the background process spawned by "compact" under Claude Code.
// Args: token [--dry-run] [--rules rule1,rule2,...]
func runCompactBackground(args []string) {
	if len(args) < 1 {
		os.Exit(1)
	}
	token := args[0]
	dryRun := false
	var ruleNames []string
	for i := 1; i < len(args); i++ {
		switch args[i] {
		case "--dry-run":
			dryRun = true
		case "--rules":
			if i+1 < len(args) {
				i++
				ruleNames = strings.Split(args[i], ",")
			}
		}
	}

	projectsBase := filepath.Join(os.Getenv("HOME"), ".claude", "projects")

	// Wait for token to appear in a JSONL file
	var jsonlPath string
	for i := 0; i < 20; i++ {
		time.Sleep(500 * time.Millisecond)
		jsonlPath, _ = findJSONLWithTokenAllProjects(token, projectsBase)
		if jsonlPath != "" {
			break
		}
	}
	if jsonlPath == "" {
		spawnNotify("Surgery Compact", "Error: could not find session JSONL file.")
		return
	}

	// Run compaction
	conv, err := LoadConversation(jsonlPath)
	if err != nil {
		spawnNotify("Surgery Compact", fmt.Sprintf("Error reading file: %v", err))
		return
	}
	beforeCount := conv.EntryCount()
	sessionID := strings.TrimSuffix(filepath.Base(jsonlPath), ".jsonl")

	rules := selectRules(ruleNames)
	report := RunCompaction(conv, rules)

	var sb strings.Builder
	formatCompactReport(&sb, filepath.Base(jsonlPath), beforeCount, conv.EntryCount(), report)

	if dryRun {
		fmt.Fprintln(&sb, "\n(dry run — no changes written)")
		fmt.Fprintf(&sb, "\nResume command:\n/resume %s", sessionID)
	} else {
		newID := generateUUID()
		conv.SessionID = newID
		for _, e := range conv.Entries {
			e.SessionID = newID
		}
		dir := filepath.Dir(jsonlPath)
		newPath := filepath.Join(dir, newID+".jsonl")
		if err := conv.WriteToFile(newPath); err != nil {
			fmt.Fprintf(&sb, "\nError writing: %v\n", err)
		} else {
			fmt.Fprintf(&sb, "\nNew session: %s\n", newID)
			fmt.Fprintf(&sb, "Original untouched: %s\n", sessionID)
		}
		fmt.Fprintf(&sb, "\nResume command:\n/resume %s", newID)
	}

	spawnNotify("Surgery Compact", sb.String())
}

// runCompactOnFile runs compaction on a JSONL file, writing to a new session file.
// The original file is left untouched.
func runCompactOnFile(jsonlPath string, ruleNames []string, dryRun bool) {
	conv, err := LoadConversation(jsonlPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "Error reading file:", err)
		os.Exit(1)
	}
	beforeCount := conv.EntryCount()

	rules := selectRules(ruleNames)
	report := RunCompaction(conv, rules)

	sessionID := strings.TrimSuffix(filepath.Base(jsonlPath), ".jsonl")
	printCompactReport(filepath.Base(jsonlPath), beforeCount, conv.EntryCount(), report)

	if dryRun {
		fmt.Println("\n(dry run — no changes written)")
		return
	}

	// Write to new session file in the same directory
	newID := generateUUID()
	conv.SessionID = newID
	// Update sessionId in all entries
	for _, e := range conv.Entries {
		e.SessionID = newID
	}
	dir := filepath.Dir(jsonlPath)
	newPath := filepath.Join(dir, newID+".jsonl")
	if err := conv.WriteToFile(newPath); err != nil {
		fmt.Fprintln(os.Stderr, "Error writing file:", err)
		os.Exit(1)
	}
	fmt.Printf("\nNew session: %s\n", newID)
	fmt.Printf("Original untouched: %s\n", sessionID)
}

func formatCompactReport(w *strings.Builder, filename string, beforeCount, afterCount int, report CompactReport) {
	fmt.Fprintf(w, "Compaction report for %s\n", filename)
	fmt.Fprintf(w, "  Before: %s (%d entries)\n", humanBytes(report.TotalBefore), beforeCount)
	fmt.Fprintf(w, "  After:  %s (%d entries)\n", humanBytes(report.TotalAfter), afterCount)
	fmt.Fprintf(w, "  Saved:  %s (%.1f%%)\n", humanBytes(report.TotalSaved),
		float64(report.TotalSaved)*100/float64(report.TotalBefore))
	fmt.Fprintln(w)
	for _, rr := range report.Rules {
		if rr.Report.BytesSaved > 0 || rr.Report.EntriesRemoved > 0 || len(rr.Report.Details) > 0 {
			fmt.Fprintf(w, "  [%s] saved %s, removed %d entries\n",
				rr.Name, humanBytes(rr.Report.BytesSaved), rr.Report.EntriesRemoved)
			for _, d := range rr.Report.Details {
				fmt.Fprintf(w, "    - %s\n", d)
			}
		} else {
			fmt.Fprintf(w, "  [%s] no changes\n", rr.Name)
		}
	}
}

func printCompactReport(filename string, beforeCount, afterCount int, report CompactReport) {
	var sb strings.Builder
	formatCompactReport(&sb, filename, beforeCount, afterCount, report)
	fmt.Print(sb.String())
}

func printCompactUsage() {
	fmt.Fprintln(os.Stderr, "Usage: surgery compact [session-id] [--dry-run] [--rules rule1,rule2,...]")
	fmt.Fprintln(os.Stderr)
	fmt.Fprintln(os.Stderr, "Session resolution (in priority order):")
	fmt.Fprintln(os.Stderr, "  1. Explicit session ID argument")
	fmt.Fprintln(os.Stderr, "  2. Auto-detect via token when run as Claude Code subprocess (CLAUDECODE=1)")
	fmt.Fprintln(os.Stderr, "  3. Most recent session in the cwd-derived project")
	fmt.Fprintln(os.Stderr)
	fmt.Fprintln(os.Stderr, "Available rules:")
	for _, r := range allRulesIncludingExtras() {
		fmt.Fprintf(os.Stderr, "  %-25s %s\n", r.Name(), r.Description())
	}
}

func allRulesIncludingExtras() []CompactRule {
	seen := map[string]bool{}
	var all []CompactRule
	for _, r := range AllRules() {
		all = append(all, r)
		seen[r.Name()] = true
	}
	extras := []CompactRule{&StripFileHistoryRule{}, &StripErrorRetriesRule{}, &TruncateLargeBashRule{}}
	for _, r := range extras {
		if !seen[r.Name()] {
			all = append(all, r)
		}
	}
	return all
}

// findSessionByID searches all projects for a JSONL file matching the session ID.
func findSessionByID(projectsBase, sessionID string) string {
	// Strip .jsonl extension if provided
	sessionID = strings.TrimSuffix(sessionID, ".jsonl")

	projects, err := os.ReadDir(projectsBase)
	if err != nil {
		return ""
	}
	for _, p := range projects {
		if !p.IsDir() {
			continue
		}
		candidate := filepath.Join(projectsBase, p.Name(), sessionID+".jsonl")
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
	}
	return ""
}

// spawnNotify launches a new process with --notify to show a Wails popup.
func spawnNotify(title, message string) {
	exe, _ := os.Executable()
	cmd := exec.Command(exe, "--notify", title)
	cmd.Stdin = strings.NewReader(message)
	cmd.Start()
}

func runNotifyWindow(title, message string) {
	notifyApp := &NotifyApp{title: title, message: message}
	wails.Run(&options.App{
		Title:            title,
		Width:            500,
		Height:           400,
		DisableResize:    false,
		BackgroundColour: &options.RGBA{R: 13, G: 17, B: 23, A: 1},
		AssetServer: &assetserver.Options{
			Assets: assets,
		},
		OnStartup: notifyApp.startup,
		Bind:      []interface{}{notifyApp},
	})
}

type NotifyApp struct {
	ctx     context.Context
	title   string
	message string
}

func (n *NotifyApp) startup(ctx context.Context) {
	n.ctx = ctx
}

func (n *NotifyApp) GetNotification() map[string]string {
	return map[string]string{"title": n.title, "message": n.message}
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

func selectRules(names []string) []CompactRule {
	if len(names) == 0 {
		return AllRules()
	}
	all := map[string]CompactRule{}
	for _, r := range AllRules() {
		all[r.Name()] = r
	}
	// Also register non-default rules
	extras := []CompactRule{&StripFileHistoryRule{}, &StripErrorRetriesRule{}, &TruncateLargeBashRule{}}
	for _, r := range extras {
		all[r.Name()] = r
	}
	var rules []CompactRule
	for _, n := range names {
		n = strings.TrimSpace(n)
		if r, ok := all[n]; ok {
			rules = append(rules, r)
		} else {
			fmt.Fprintf(os.Stderr, "Warning: unknown rule %q, skipping\n", n)
		}
	}
	return rules
}

