package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
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
	KeepUUIDs []string `json:"keep_uuids"`
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

	var linesToWrite []string
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
			continue
		}
		var parent string
		json.Unmarshal(d["parentUuid"], &parent)
		if parent != "" && !keepSet[parent] {
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
