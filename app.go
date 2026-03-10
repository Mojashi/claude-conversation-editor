package main

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/wailsapp/wails/v2/pkg/runtime"
)

type App struct {
	ctx            context.Context
	startupProject string
	startupSession string
}

func NewApp() *App {
	return &App{}
}

func (a *App) startup(ctx context.Context) {
	a.ctx = ctx
}

type StartupArgs struct {
	Project string `json:"project"`
	Session string `json:"session"`
}

func (a *App) GetStartupArgs() StartupArgs {
	return StartupArgs{Project: a.startupProject, Session: a.startupSession}
}

func claudeDir() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".claude", "projects")
}

func decodeProjectName(id string) string {
	re := regexp.MustCompile(`^-Users-[^-]+`)
	name := re.ReplaceAllString(id, "~")
	return strings.ReplaceAll(name, "-", "/")
}

type Project struct {
	ID           string  `json:"id"`
	Name         string  `json:"name"`
	SessionCount int     `json:"session_count"`
	Mtime        float64 `json:"mtime"`
}

type Session struct {
	ID       string  `json:"id"`
	Preview  string  `json:"preview"`
	MsgCount int     `json:"msg_count"`
	Size     int64   `json:"size"`
	Mtime    float64 `json:"mtime"`
}

type ContentSummary struct {
	Types       []string `json:"types"`
	TextPreview string   `json:"text_preview"`
	Size        int      `json:"size"`
}

type Message struct {
	UUID           string          `json:"uuid"`
	ParentUUID     string          `json:"parentUuid"`
	Type           string          `json:"type"`
	Role           string          `json:"role"`
	Timestamp      string          `json:"timestamp"`
	IsSidechain    bool            `json:"isSidechain"`
	ContentSummary ContentSummary  `json:"content_summary"`
	IsToolOnly     bool            `json:"is_tool_only"`
	IsSystem       bool            `json:"is_system"`
	Model          string          `json:"model"`
	Raw            json.RawMessage `json:"raw"`
}

type Conversation struct {
	Messages  []Message `json:"messages"`
	TotalSize int64     `json:"total_size"`
	SessionID string    `json:"session_id"`
}

type SaveRequest struct {
	KeepUUIDs    []string `json:"keep_uuids"`
	DeletedUUIDs []string `json:"deleted_uuids"` // for parentUuid repair after insertion
	InsertLines  []string `json:"insert_lines"`  // pre-built JSONL lines to insert at deletion gap
}

type SaveResult struct {
	Success   bool   `json:"success"`
	KeptLines int    `json:"kept_lines"`
	NewSize   int64  `json:"new_size"`
	Backup    string `json:"backup"`
}

func parseContentSummary(content json.RawMessage) ContentSummary {
	if content == nil {
		return ContentSummary{}
	}
	var s string
	if err := json.Unmarshal(content, &s); err == nil {
		preview := s
		if len(preview) > 200 {
			preview = preview[:200]
		}
		return ContentSummary{Types: []string{"text"}, TextPreview: preview, Size: len(s)}
	}
	var arr []json.RawMessage
	if err := json.Unmarshal(content, &arr); err == nil {
		var types []string
		var preview string
		size := 0
		for _, item := range arr {
			var block map[string]json.RawMessage
			if err := json.Unmarshal(item, &block); err != nil {
				continue
			}
			var t string
			json.Unmarshal(block["type"], &t)
			types = append(types, t)
			if t == "text" && preview == "" {
				var text string
				json.Unmarshal(block["text"], &text)
				if len(text) > 200 {
					preview = text[:200]
				} else {
					preview = text
				}
			}
			size += len(item)
		}
		return ContentSummary{Types: types, TextPreview: preview, Size: size}
	}
	return ContentSummary{}
}

func mtimeOf(path string) float64 {
	info, err := os.Stat(path)
	if err != nil {
		return 0
	}
	return float64(info.ModTime().UnixMilli()) / 1000.0
}

func (a *App) ListProjects() []Project {
	base := claudeDir()
	entries, err := os.ReadDir(base)
	if err != nil {
		return nil
	}
	var projects []Project
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		id := e.Name()
		jsonls, _ := filepath.Glob(filepath.Join(base, id, "*.jsonl"))
		if len(jsonls) == 0 {
			continue
		}
		var maxMtime float64
		for _, f := range jsonls {
			if m := mtimeOf(f); m > maxMtime {
				maxMtime = m
			}
		}
		projects = append(projects, Project{
			ID:           id,
			Name:         decodeProjectName(id),
			SessionCount: len(jsonls),
			Mtime:        maxMtime,
		})
	}
	sort.Slice(projects, func(i, j int) bool {
		return projects[i].Mtime > projects[j].Mtime
	})
	return projects
}

func (a *App) ListSessions(projectID string) ([]Session, error) {
	dir := filepath.Join(claudeDir(), projectID)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("project not found: %s", projectID)
	}
	var sessions []Session
	for _, e := range entries {
		if filepath.Ext(e.Name()) != ".jsonl" {
			continue
		}
		path := filepath.Join(dir, e.Name())
		info, _ := e.Info()
		id := strings.TrimSuffix(e.Name(), ".jsonl")

		var preview string
		msgCount := 0
		f, err := os.Open(path)
		if err == nil {
			scanner := bufio.NewScanner(f)
			scanner.Buffer(make([]byte, 1024*1024), 1024*1024)
			for scanner.Scan() {
				var d map[string]json.RawMessage
				if err := json.Unmarshal(scanner.Bytes(), &d); err != nil {
					continue
				}
				var t string
				json.Unmarshal(d["type"], &t)
				if t != "user" && t != "assistant" {
					continue
				}
				msgCount++
				if preview == "" && t == "user" {
					var msg map[string]json.RawMessage
					json.Unmarshal(d["message"], &msg)
					cs := parseContentSummary(msg["content"])
					if !strings.HasPrefix(cs.TextPreview, "<") && len(cs.TextPreview) > 5 {
						preview = cs.TextPreview
					}
				}
			}
			f.Close()
		}
		if preview == "" {
			preview = "(no text)"
		}
		sessions = append(sessions, Session{
			ID:       id,
			Preview:  preview,
			MsgCount: msgCount,
			Size:     info.Size(),
			Mtime:    mtimeOf(path),
		})
	}
	sort.Slice(sessions, func(i, j int) bool {
		return sessions[i].Mtime > sessions[j].Mtime
	})
	return sessions, nil
}

func (a *App) GetConversation(projectID, sessionID string) (*Conversation, error) {
	path := filepath.Join(claudeDir(), projectID, sessionID+".jsonl")
	info, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("session not found: %s", sessionID)
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var messages []Message
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 4*1024*1024), 4*1024*1024)
	for scanner.Scan() {
		line := scanner.Bytes()
		var d map[string]json.RawMessage
		if err := json.Unmarshal(line, &d); err != nil {
			continue
		}
		var t string
		json.Unmarshal(d["type"], &t)
		if t != "user" && t != "assistant" {
			continue
		}
		var msg map[string]json.RawMessage
		json.Unmarshal(d["message"], &msg)
		cs := parseContentSummary(msg["content"])

		isToolOnly := len(cs.Types) > 0
		for _, ct := range cs.Types {
			if ct != "tool_result" && ct != "tool_use" {
				isToolOnly = false
				break
			}
		}
		var contentStr string
		json.Unmarshal(msg["content"], &contentStr)
		isSystem := strings.HasPrefix(contentStr, "<")

		var uuid, parentUUID, role, timestamp, model string
		json.Unmarshal(d["uuid"], &uuid)
		json.Unmarshal(d["parentUuid"], &parentUUID)
		json.Unmarshal(d["timestamp"], &timestamp)
		json.Unmarshal(msg["role"], &role)
		json.Unmarshal(msg["model"], &model)
		var isSidechain bool
		json.Unmarshal(d["isSidechain"], &isSidechain)

		raw := make([]byte, len(line))
		copy(raw, line)

		messages = append(messages, Message{
			UUID:           uuid,
			ParentUUID:     parentUUID,
			Type:           t,
			Role:           role,
			Timestamp:      timestamp,
			IsSidechain:    isSidechain,
			ContentSummary: cs,
			IsToolOnly:     isToolOnly,
			IsSystem:       isSystem,
			Model:          model,
			Raw:            raw,
		})
	}
	return &Conversation{Messages: messages, TotalSize: info.Size(), SessionID: sessionID}, nil
}

func (a *App) SaveConversation(projectID, sessionID string, req SaveRequest) (*SaveResult, error) {
	path := filepath.Join(claudeDir(), projectID, sessionID+".jsonl")
	if _, err := os.Stat(path); err != nil {
		return nil, fmt.Errorf("session not found: %s", sessionID)
	}

	keepSet := make(map[string]bool)
	for _, u := range req.KeepUUIDs {
		keepSet[u] = true
	}

	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	var rawLines [][]byte
	uuidToParent := map[string]string{}
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 4*1024*1024), 4*1024*1024)
	for scanner.Scan() {
		line := make([]byte, len(scanner.Bytes()))
		copy(line, scanner.Bytes())
		rawLines = append(rawLines, line)
		var d map[string]json.RawMessage
		if err := json.Unmarshal(line, &d); err == nil {
			var uuid, parent string
			json.Unmarshal(d["uuid"], &uuid)
			json.Unmarshal(d["parentUuid"], &parent)
			if uuid != "" {
				uuidToParent[uuid] = parent
			}
		}
	}
	f.Close()

	var findKeptAncestor func(string) string
	findKeptAncestor = func(uuid string) string {
		visited := map[string]bool{}
		cur := uuid
		for cur != "" && !visited[cur] {
			visited[cur] = true
			if keepSet[cur] {
				return cur
			}
			cur = uuidToParent[cur]
		}
		return ""
	}

	// Build deleted set for insert-gap detection
	deletedSet := make(map[string]bool)
	for _, u := range req.DeletedUUIDs {
		deletedSet[u] = true
	}
	// Determine last inserted UUID (for parentUuid repair of post-gap messages)
	var lastInsertUUID string
	if len(req.InsertLines) > 0 {
		var d map[string]json.RawMessage
		last := req.InsertLines[len(req.InsertLines)-1]
		if json.Unmarshal([]byte(last), &d) == nil {
			json.Unmarshal(d["uuid"], &lastInsertUUID)
		}
	}

	var linesToWrite []string
	insertedAtGap := false
	for _, line := range rawLines {
		var d map[string]json.RawMessage
		if err := json.Unmarshal(line, &d); err != nil {
			linesToWrite = append(linesToWrite, string(line))
			continue
		}
		var t string
		json.Unmarshal(d["type"], &t)
		if t != "user" && t != "assistant" {
			linesToWrite = append(linesToWrite, string(line))
			continue
		}
		var uuid string
		json.Unmarshal(d["uuid"], &uuid)
		if !keepSet[uuid] {
			// Insert lines at the first gap
			if !insertedAtGap && len(req.InsertLines) > 0 {
				linesToWrite = append(linesToWrite, req.InsertLines...)
				insertedAtGap = true
			}
			continue
		}
		var parent string
		json.Unmarshal(d["parentUuid"], &parent)
		// Fix parentUuid: if parent was deleted and we inserted, point to last insert
		if parent != "" && deletedSet[parent] && lastInsertUUID != "" {
			d["parentUuid"], _ = json.Marshal(lastInsertUUID)
			out, _ := json.Marshal(d)
			linesToWrite = append(linesToWrite, string(out))
		} else if parent != "" && !keepSet[parent] && !deletedSet[parent] {
			ancestor := findKeptAncestor(parent)
			if ancestor == "" {
				d["parentUuid"] = json.RawMessage("null")
			} else {
				b, _ := json.Marshal(ancestor)
				d["parentUuid"] = b
			}
			out, _ := json.Marshal(d)
			linesToWrite = append(linesToWrite, string(out))
		} else {
			linesToWrite = append(linesToWrite, string(line))
		}
	}

	backup := path + ".bak"
	src, _ := os.Open(path)
	dst, _ := os.Create(backup)
	io.Copy(dst, src)
	src.Close()
	dst.Close()

	out, err := os.Create(path)
	if err != nil {
		return nil, err
	}
	for _, line := range linesToWrite {
		fmt.Fprintln(out, line)
	}
	out.Close()

	newInfo, _ := os.Stat(path)
	return &SaveResult{
		Success:   true,
		KeptLines: len(linesToWrite),
		NewSize:   newInfo.Size(),
		Backup:    backup,
	}, nil
}

// EditMessage updates the text content of a specific message.
func (a *App) EditMessage(projectID, sessionID, uuid, newText string) error {
	path := filepath.Join(claudeDir(), projectID, sessionID+".jsonl")

	f, err := os.Open(path)
	if err != nil {
		return err
	}
	var lines []string
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 4*1024*1024), 4*1024*1024)
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
	}
	f.Close()

	var updated []string
	for _, line := range lines {
		var d map[string]json.RawMessage
		if err := json.Unmarshal([]byte(line), &d); err != nil {
			updated = append(updated, line)
			continue
		}
		var u string
		json.Unmarshal(d["uuid"], &u)
		if u != uuid {
			updated = append(updated, line)
			continue
		}

		// Update content: replace text block(s) with newText
		var msg map[string]json.RawMessage
		json.Unmarshal(d["message"], &msg)
		content := msg["content"]

		var s string
		if json.Unmarshal(content, &s) == nil {
			// String content: replace directly
			b, _ := json.Marshal(newText)
			msg["content"] = b
		} else {
			var arr []json.RawMessage
			if json.Unmarshal(content, &arr) == nil {
				// Array: update all text blocks
				first := true
				for i, item := range arr {
					var block map[string]json.RawMessage
					if err := json.Unmarshal(item, &block); err != nil {
						continue
					}
					var t string
					json.Unmarshal(block["type"], &t)
					if t == "text" {
						if first {
							b, _ := json.Marshal(newText)
							block["text"] = b
							first = false
						} else {
							block["text"] = json.RawMessage(`""`)
						}
						arr[i], _ = json.Marshal(block)
					}
				}
				b, _ := json.Marshal(arr)
				msg["content"] = b
			}
		}

		msgBytes, _ := json.Marshal(msg)
		d["message"] = msgBytes
		out, _ := json.Marshal(d)
		updated = append(updated, string(out))
	}

	out, err := os.Create(path)
	if err != nil {
		return err
	}
	for _, line := range updated {
		fmt.Fprintln(out, line)
	}
	out.Close()
	return nil
}

// setSidechainFrom sets isSidechain on all descendants of fromUUID (exclusive).
// If toMain=true, restores them to main (isSidechain=false).
func (a *App) setSidechainFrom(projectID, sessionID, fromUUID string, toSidechain bool) error {
	path := filepath.Join(claudeDir(), projectID, sessionID+".jsonl")

	f, err := os.Open(path)
	if err != nil {
		return err
	}
	var lines []string
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 4*1024*1024), 4*1024*1024)
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
	}
	f.Close()

	// Build uuid->parentUuid map and collect all descendants of fromUUID
	uuidToParent := map[string]string{}
	for _, line := range lines {
		var d map[string]json.RawMessage
		if json.Unmarshal([]byte(line), &d) != nil {
			continue
		}
		var uuid, parent string
		json.Unmarshal(d["uuid"], &uuid)
		json.Unmarshal(d["parentUuid"], &parent)
		if uuid != "" {
			uuidToParent[uuid] = parent
		}
	}

	// Find all descendants (messages whose ancestor chain passes through fromUUID)
	isDescendant := map[string]bool{}
	for uuid := range uuidToParent {
		cur := uuid
		for cur != "" {
			if cur == fromUUID {
				isDescendant[uuid] = true
				break
			}
			cur = uuidToParent[cur]
		}
	}

	var updated []string
	for _, line := range lines {
		var d map[string]json.RawMessage
		if json.Unmarshal([]byte(line), &d) != nil {
			updated = append(updated, line)
			continue
		}
		var uuid string
		json.Unmarshal(d["uuid"], &uuid)
		if uuid != "" && isDescendant[uuid] {
			if toSidechain {
				d["isSidechain"] = json.RawMessage("true")
			} else {
				d["isSidechain"] = json.RawMessage("false")
			}
			out, _ := json.Marshal(d)
			updated = append(updated, string(out))
		} else {
			updated = append(updated, line)
		}
	}

	out, err := os.Create(path)
	if err != nil {
		return err
	}
	for _, line := range updated {
		fmt.Fprintln(out, line)
	}
	out.Close()
	return nil
}

func projectIDToPath(id string) string {
	return "/" + strings.ReplaceAll(strings.TrimPrefix(id, "-"), "-", "/")
}

func (a *App) ExecClaude(projectID string, skipPermissions bool) error {
	dir := projectIDToPath(projectID)
	claudeCmd := "claude"
	if skipPermissions {
		claudeCmd = "claude --dangerously-skip-permissions"
	}
	script := fmt.Sprintf("cd %s && %s", dir, claudeCmd)
	return exec.Command("osascript",
		"-e", `tell application "Terminal" to activate`,
		"-e", fmt.Sprintf(`tell application "Terminal" to do script %q`, script),
	).Run()
}

// InsertMessage inserts a new message immediately after afterUUID.
// The message after afterUUID gets its parentUuid updated to the new message.
func (a *App) InsertMessage(projectID, sessionID, afterUUID, role, text string) error {
	path := filepath.Join(claudeDir(), projectID, sessionID+".jsonl")
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	var lines []string
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 4*1024*1024), 4*1024*1024)
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
	}
	f.Close()

	// Generate new UUID
	b := make([]byte, 16)
	rand.Read(b)
	newUUID := fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
		b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])

	// Build new message line
	msgContent, _ := json.Marshal(text)
	newMsg := map[string]interface{}{
		"role":    role,
		"content": json.RawMessage(msgContent),
	}
	msgBytes, _ := json.Marshal(newMsg)
	newLine := map[string]interface{}{
		"uuid":       newUUID,
		"parentUuid": afterUUID,
		"type":       role,
		"timestamp":  "2006-01-02T15:04:05.000Z",
		"message":    json.RawMessage(msgBytes),
	}
	newLineBytes, _ := json.Marshal(newLine)

	// Insert after afterUUID, update next message's parentUuid
	var out []string
	for _, line := range lines {
		out = append(out, line)
		var d map[string]json.RawMessage
		if json.Unmarshal([]byte(line), &d) != nil {
			continue
		}
		var uuid string
		json.Unmarshal(d["uuid"], &uuid)
		if uuid == afterUUID {
			out = append(out, string(newLineBytes))
		}
	}

	// Update any message whose parentUuid == afterUUID (except our new one) to point to newUUID
	// Only update the first one found (the direct child in the main chain)
	updatedChild := false
	for i, line := range out {
		var d map[string]json.RawMessage
		if json.Unmarshal([]byte(line), &d) != nil {
			continue
		}
		var uuid, parent string
		json.Unmarshal(d["uuid"], &uuid)
		json.Unmarshal(d["parentUuid"], &parent)
		if uuid == newUUID {
			continue // skip our inserted message
		}
		if parent == afterUUID && !updatedChild {
			d["parentUuid"], _ = json.Marshal(newUUID)
			fixed, _ := json.Marshal(d)
			out[i] = string(fixed)
			updatedChild = true
		}
	}

	outFile, err := os.Create(path)
	if err != nil {
		return err
	}
	for _, line := range out {
		fmt.Fprintln(outFile, line)
	}
	outFile.Close()
	return nil
}

func (a *App) BranchFrom(projectID, sessionID, fromUUID string) error {
	return a.setSidechainFrom(projectID, sessionID, fromUUID, true)
}

func (a *App) RestoreSidechain(projectID, sessionID, fromUUID string) error {
	return a.setSidechainFrom(projectID, sessionID, fromUUID, false)
}

// BranchNewSession copies all lines up to and including fromUUID into a new session file.
// Returns the new session ID.
func (a *App) BranchNewSession(projectID, sessionID, fromUUID string) (string, error) {
	src := filepath.Join(claudeDir(), projectID, sessionID+".jsonl")

	f, err := os.Open(src)
	if err != nil {
		return "", err
	}
	var lines []string
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 4*1024*1024), 4*1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		lines = append(lines, line)
		// stop after the target message
		var d map[string]json.RawMessage
		if json.Unmarshal([]byte(line), &d) == nil {
			var uuid string
			json.Unmarshal(d["uuid"], &uuid)
			if uuid == fromUUID {
				break
			}
		}
	}
	f.Close()

	// generate new session ID (UUID v4-ish)
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	newID := fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
		b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])

	dst := filepath.Join(claudeDir(), projectID, newID+".jsonl")
	out, err := os.Create(dst)
	if err != nil {
		return "", err
	}
	for _, line := range lines {
		fmt.Fprintln(out, line)
	}
	out.Close()

	return newID, nil
}

// extractSelectedMessages returns raw message objects (role+content) for selected UUIDs, in file order.
func extractSelectedMessages(path string, uuidSet map[string]bool) ([]map[string]json.RawMessage, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var msgs []map[string]json.RawMessage
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 4*1024*1024), 4*1024*1024)
	for scanner.Scan() {
		var d map[string]json.RawMessage
		if json.Unmarshal(scanner.Bytes(), &d) != nil {
			continue
		}
		var uuid string
		json.Unmarshal(d["uuid"], &uuid)
		if !uuidSet[uuid] {
			continue
		}
		var msg map[string]json.RawMessage
		json.Unmarshal(d["message"], &msg)
		msgs = append(msgs, msg)
	}
	return msgs, nil
}

// SummarizeMessages calls `claude -p` to summarize the selected messages.
func (a *App) SummarizeMessages(projectID, sessionID string, uuids []string) (string, error) {
	path := filepath.Join(claudeDir(), projectID, sessionID+".jsonl")
	uuidSet := make(map[string]bool, len(uuids))
	for _, u := range uuids {
		uuidSet[u] = true
	}
	msgs, err := extractSelectedMessages(path, uuidSet)
	if err != nil {
		return "", err
	}
	if len(msgs) == 0 {
		return "", fmt.Errorf("no selected messages found")
	}

	var parts []string
	for _, msg := range msgs {
		var role string
		json.Unmarshal(msg["role"], &role)
		text := extractText(msg["content"])
		if text == "" {
			text = "[no text]"
		}
		parts = append(parts, role+": "+text)
	}

	prompt := "以下の会話を簡潔にサマリーしてください。重要な決定・発見・コンテキストを保持してください。サマリーのみ出力してください。\n\n" +
		strings.Join(parts, "\n\n")

	out, err := runClaude(prompt, "")
	if err != nil {
		return "", fmt.Errorf("claude failed: %w", err)
	}
	return strings.TrimSpace(out), nil
}

var idealizeSchema = `{"type":"object","properties":{"messages":{"type":"array","items":{"type":"object","properties":{"role":{"type":"string","enum":["user","assistant"]},"content":{"type":"array","items":{"type":"object","properties":{"type":{"type":"string","enum":["text","tool_use","tool_result"]},"text":{"type":"string"},"id":{"type":"string"},"name":{"type":"string"},"input":{"type":"object"},"tool_use_id":{"type":"string"},"content":{"type":"string"}},"required":["type"]}}},"required":["role","content"]}}},"required":["messages"]}`

const idealizeSystemPrompt = `You are rewriting a conversation to its ideal form — the version that would have occurred if everything went perfectly on the first try.

Rules:
- Remove all errors, failed attempts, retries, and unnecessary detours
- The correct approach is taken immediately every time
- Tool calls return correct results on the first try (regenerate realistic tool_result content)
- Keep all semantically important exchanges; remove only waste
- Preserve the overall goal and outcome
- Output ONLY the idealized messages array in the required JSON format`

// IdealizeMessages generates an idealized version of the selected messages using claude.
// Returns JSON string of the idealized messages array.
func (a *App) IdealizeMessages(projectID, sessionID string, uuids []string) (string, error) {
	path := filepath.Join(claudeDir(), projectID, sessionID+".jsonl")
	uuidSet := make(map[string]bool, len(uuids))
	for _, u := range uuids {
		uuidSet[u] = true
	}
	msgs, err := extractSelectedMessages(path, uuidSet)
	if err != nil {
		return "", err
	}
	if len(msgs) == 0 {
		return "", fmt.Errorf("no selected messages found")
	}

	// Serialize messages as JSON for the prompt
	msgsJSON, _ := json.MarshalIndent(msgs, "", "  ")
	prompt := "Rewrite the following conversation in its ideal form and output it as structured JSON matching the schema:\n\n" + string(msgsJSON)

	out, err := runClaudeStreaming(a.ctx, prompt, idealizeSchema, idealizeSystemPrompt)
	if err != nil {
		return "", fmt.Errorf("claude failed: %w", err)
	}

	// out may be the JSON object directly, or a JSON-encoded string containing it
	extract := func(s string) (string, error) {
		var wrapper struct {
			Messages json.RawMessage `json:"messages"`
		}
		if err := json.Unmarshal([]byte(s), &wrapper); err != nil {
			return "", fmt.Errorf("parse failed: %w\nraw: %s", err, s)
		}
		return string(wrapper.Messages), nil
	}

	if result, err := extract(out); err == nil {
		return result, nil
	}
	var inner string
	if json.Unmarshal([]byte(out), &inner) == nil {
		if result, err := extract(inner); err == nil {
			return result, nil
		}
	}
	return "", fmt.Errorf("parse failed\nraw: %s", out)
}

// ApplyIdealized creates a new session replacing the selected range with idealized messages.
// Returns the new session ID.
func (a *App) ApplyIdealized(projectID, sessionID string, uuids []string, messagesJSON string) (string, error) {
	// Parse idealized messages
	var idealMsgs []struct {
		Role    string            `json:"role"`
		Content json.RawMessage   `json:"content"`
	}
	if err := json.Unmarshal([]byte(messagesJSON), &idealMsgs); err != nil {
		return "", fmt.Errorf("invalid messages JSON: %w", err)
	}

	path := filepath.Join(claudeDir(), projectID, sessionID+".jsonl")
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	var lines []string
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 4*1024*1024), 4*1024*1024)
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
	}
	f.Close()

	uuidSet := make(map[string]bool, len(uuids))
	for _, u := range uuids {
		uuidSet[u] = true
	}

	// Find parentUuid of first selected message
	var firstParentUUID string
	for _, line := range lines {
		var d map[string]json.RawMessage
		if json.Unmarshal([]byte(line), &d) != nil {
			continue
		}
		var uuid string
		json.Unmarshal(d["uuid"], &uuid)
		if uuidSet[uuid] {
			json.Unmarshal(d["parentUuid"], &firstParentUUID)
			break
		}
	}

	// Generate UUIDs for idealized messages and build JSONL lines
	type idealLine struct {
		uuid string
		line string
	}
	var idealLines []idealLine
	prevUUID := firstParentUUID
	for _, im := range idealMsgs {
		b := make([]byte, 16)
		rand.Read(b)
		newUUID := fmt.Sprintf("%08x-%04x-%04x-%04x-%012x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])

		msgBytes, _ := json.Marshal(map[string]interface{}{
			"role":    im.Role,
			"content": im.Content,
		})
		d := map[string]interface{}{
			"uuid":       newUUID,
			"parentUuid": prevUUID,
			"type":       im.Role,
			"timestamp":  "2006-01-02T15:04:05.000Z",
			"message":    json.RawMessage(msgBytes),
		}
		lineBytes, _ := json.Marshal(d)
		idealLines = append(idealLines, idealLine{uuid: newUUID, line: string(lineBytes)})
		prevUUID = newUUID
	}

	lastIdealUUID := prevUUID

	// Build new session: pre-selection + idealized + post-selection
	var outLines []string
	inSelection := false
	passedSelection := false
	firstPostFixed := false
	for _, line := range lines {
		var d map[string]json.RawMessage
		if json.Unmarshal([]byte(line), &d) != nil {
			outLines = append(outLines, line)
			continue
		}
		var uuid string
		json.Unmarshal(d["uuid"], &uuid)

		if uuidSet[uuid] {
			if !inSelection {
				inSelection = true
				// Insert idealized messages here
				for _, il := range idealLines {
					outLines = append(outLines, il.line)
				}
			}
			passedSelection = true
			continue // skip original selected messages
		}

		if passedSelection && !firstPostFixed {
			// Fix parentUuid of first post-selection message
			var parent string
			json.Unmarshal(d["parentUuid"], &parent)
			if uuidSet[parent] {
				d["parentUuid"], _ = json.Marshal(lastIdealUUID)
				fixed, _ := json.Marshal(d)
				outLines = append(outLines, string(fixed))
				firstPostFixed = true
				continue
			}
		}
		outLines = append(outLines, line)
	}
	_ = inSelection

	// Write new session
	b := make([]byte, 16)
	rand.Read(b)
	newID := fmt.Sprintf("%08x-%04x-%04x-%04x-%012x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
	dst := filepath.Join(claudeDir(), projectID, newID+".jsonl")
	out, err := os.Create(dst)
	if err != nil {
		return "", err
	}
	for _, line := range outLines {
		fmt.Fprintln(out, line)
	}
	out.Close()
	return newID, nil
}

func extractText(content json.RawMessage) string {
	var s string
	if json.Unmarshal(content, &s) == nil {
		return strings.TrimSpace(s)
	}
	var arr []json.RawMessage
	if json.Unmarshal(content, &arr) != nil {
		return ""
	}
	var texts []string
	for _, item := range arr {
		var block map[string]json.RawMessage
		if json.Unmarshal(item, &block) != nil {
			continue
		}
		var t string
		json.Unmarshal(block["type"], &t)
		switch t {
		case "text":
			var text string
			json.Unmarshal(block["text"], &text)
			if text != "" {
				texts = append(texts, text)
			}
		case "tool_use":
			var name string
			json.Unmarshal(block["name"], &name)
			input, _ := json.Marshal(block["input"])
			texts = append(texts, fmt.Sprintf("[tool_use: %s %s]", name, string(input)))
		case "tool_result":
			var resultText string
			// content may be array or string
			var sub []json.RawMessage
			if json.Unmarshal(block["content"], &sub) == nil {
				for _, s := range sub {
					var sb map[string]json.RawMessage
					json.Unmarshal(s, &sb)
					var tx string
					json.Unmarshal(sb["text"], &tx)
					resultText += tx
				}
			} else {
				json.Unmarshal(block["content"], &resultText)
			}
			if len(resultText) > 500 {
				resultText = resultText[:500] + "…"
			}
			texts = append(texts, fmt.Sprintf("[tool_result: %s]", resultText))
		case "thinking":
			// skip thinking blocks
		}
	}
	return strings.Join(texts, "\n")
}

const summarizeSystemPrompt = `You are a conversation summarizer. Output only a concise summary of the conversation provided. Do not use any tools. Do not ask questions. Output the summary text directly with no preamble.`

// runClaude calls claude -p with --output-format json.
// If jsonSchema is non-empty, --json-schema is passed; systemPrompt overrides the default.
func runClaude(prompt, jsonSchema string) (string, error) {
	return runClaudeWithSystem(prompt, jsonSchema, "")
}

// runClaudeStreaming streams output tokens via Wails events and returns the final result.
func runClaudeStreaming(ctx context.Context, prompt, jsonSchema, systemPrompt string) (string, error) {
	args := []string{"-p", "--output-format", "stream-json", "--verbose", "--effort", "low"}
	if jsonSchema != "" {
		args = append(args, "--json-schema", jsonSchema)
	}
	sp := systemPrompt
	if sp == "" {
		sp = summarizeSystemPrompt
	}
	args = append(args, "--system-prompt", sp, prompt)

	cmd := exec.Command("claude", args...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return "", err
	}
	var stderrBuf strings.Builder
	cmd.Stderr = &stderrBuf
	if err := cmd.Start(); err != nil {
		return "", err
	}

	var finalResult string
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 4*1024*1024), 4*1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}
		var event map[string]json.RawMessage
		if json.Unmarshal([]byte(line), &event) != nil {
			continue
		}
		var evType string
		json.Unmarshal(event["type"], &evType)

		switch evType {
		case "assistant":
			// streaming content delta — emit token to frontend
			if ctx != nil {
				runtime.EventsEmit(ctx, "claude:stream", line)
			}
		case "result":
			var result string
			json.Unmarshal(event["result"], &result)
			finalResult = result
		}
	}

	if err := cmd.Wait(); err != nil {
		if se := strings.TrimSpace(stderrBuf.String()); se != "" {
			return "", fmt.Errorf("%s", se)
		}
		return "", err
	}
	return strings.TrimSpace(finalResult), nil
}

func runClaudeWithSystem(prompt, jsonSchema, systemPrompt string) (string, error) {
	args := []string{"-p", "--output-format", "json", "--effort", "low"}
	if jsonSchema != "" {
		args = append(args, "--json-schema", jsonSchema)
	}
	sp := systemPrompt
	if sp == "" {
		sp = summarizeSystemPrompt
	}
	args = append(args, "--system-prompt", sp, prompt)

	cmd := exec.Command("claude", args...)
	out, err := cmd.Output()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok && len(ee.Stderr) > 0 {
			return "", fmt.Errorf("%s", strings.TrimSpace(string(ee.Stderr)))
		}
		return "", err
	}

	// Parse JSON envelope: {"type":"result","result":"..."}
	var envelope struct {
		Result string `json:"result"`
		Error  string `json:"error"`
	}
	if err := json.Unmarshal(out, &envelope); err != nil {
		return strings.TrimSpace(string(out)), nil
	}
	if envelope.Error != "" {
		return "", fmt.Errorf("%s", envelope.Error)
	}
	return strings.TrimSpace(envelope.Result), nil
}

// ApplySummary replaces the selected messages with a single summary user message.
func (a *App) ApplySummary(projectID, sessionID string, uuids []string, summary string) error {
	path := filepath.Join(claudeDir(), projectID, sessionID+".jsonl")
	f, err := os.Open(path)
	if err != nil {
		return err
	}

	uuidSet := make(map[string]bool, len(uuids))
	for _, u := range uuids {
		uuidSet[u] = true
	}

	var lines []string
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 4*1024*1024), 4*1024*1024)
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
	}
	f.Close()

	// Find where the first selected message appears and its parentUuid
	firstIdx := -1
	var firstParentUUID string
	for i, line := range lines {
		var d map[string]json.RawMessage
		if json.Unmarshal([]byte(line), &d) != nil {
			continue
		}
		var uuid string
		json.Unmarshal(d["uuid"], &uuid)
		if uuidSet[uuid] {
			firstIdx = i
			json.Unmarshal(d["parentUuid"], &firstParentUUID)
			break
		}
	}
	if firstIdx < 0 {
		return fmt.Errorf("selected messages not found")
	}

	// Generate UUID for summary message
	b := make([]byte, 16)
	rand.Read(b)
	summaryUUID := fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
		b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])

	// Build summary message line
	summaryMsg := map[string]interface{}{
		"role":    "user",
		"content": "[Summary]\n" + summary,
	}
	summaryMsgBytes, _ := json.Marshal(summaryMsg)
	summaryLine := map[string]interface{}{
		"uuid":       summaryUUID,
		"parentUuid": firstParentUUID,
		"type":       "user",
		"timestamp":  "2006-01-02T15:04:05.000Z",
		"message":    json.RawMessage(summaryMsgBytes),
		"isSummary":  true,
	}
	summaryLineBytes, _ := json.Marshal(summaryLine)

	// Write output: replace selected block with summary message,
	// update any message whose parentUuid is in uuidSet to point to summaryUUID
	out, err := os.Create(path)
	if err != nil {
		return err
	}
	inserted := false
	for _, line := range lines {
		var d map[string]json.RawMessage
		if json.Unmarshal([]byte(line), &d) != nil {
			fmt.Fprintln(out, line)
			continue
		}
		var uuid string
		json.Unmarshal(d["uuid"], &uuid)
		if uuidSet[uuid] {
			if !inserted {
				fmt.Fprintln(out, string(summaryLineBytes))
				inserted = true
			}
			continue // skip selected messages
		}
		// Fix parentUuid if it pointed to a deleted message
		var parent string
		json.Unmarshal(d["parentUuid"], &parent)
		if uuidSet[parent] {
			d["parentUuid"], _ = json.Marshal(summaryUUID)
			fixed, _ := json.Marshal(d)
			fmt.Fprintln(out, string(fixed))
		} else {
			fmt.Fprintln(out, line)
		}
	}
	out.Close()
	return nil
}
