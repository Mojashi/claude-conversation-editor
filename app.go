package main

import (
	"context"
	"encoding/json"
	"fmt"
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
	UUID              string           `json:"uuid"`
	ParentUUID        string           `json:"parentUuid"`
	Type              string           `json:"type"`
	Role              string           `json:"role"`
	Timestamp         string           `json:"timestamp"`
	IsSidechain       bool             `json:"isSidechain"`
	ContentSummary    ContentSummary   `json:"content_summary"`
	IsToolOnly        bool             `json:"is_tool_only"`
	IsSystem          bool             `json:"is_system"`
	IsCompactBoundary bool             `json:"is_compact_boundary"`
	CompactMeta       *CompactMetadata `json:"compact_meta,omitempty"`
	Model             string           `json:"model"`
	Raw               json.RawMessage  `json:"raw"`
}

type ConversationView struct {
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
		entries, _ := ReadJSONLFile(path)
		for _, e := range entries {
			if !e.IsMessage() {
				continue
			}
			msgCount++
			if preview == "" && e.Type == "user" && e.Message != nil {
				cs := parseContentSummary(e.Message.Content)
				if !strings.HasPrefix(cs.TextPreview, "<") && len(cs.TextPreview) > 5 {
					preview = cs.TextPreview
				}
			}
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

func (a *App) GetConversation(projectID, sessionID string) (*ConversationView, error) {
	path := filepath.Join(claudeDir(), projectID, sessionID+".jsonl")
	info, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("session not found: %s", sessionID)
	}
	entries, err := ReadJSONLFile(path)
	if err != nil {
		return nil, err
	}

	var messages []Message
	for _, e := range entries {
		if e.IsCompactBoundary() {
			raw, _ := e.Marshal()
			messages = append(messages, Message{
				UUID:              e.UUID,
				ParentUUID:        e.GetParentUUID(),
				Type:              e.Type,
				Role:              "system",
				Timestamp:         e.Timestamp,
				IsCompactBoundary: true,
				CompactMeta:       e.CompactMeta,
				Raw:               raw,
			})
			continue
		}
		if !e.IsMessage() {
			continue
		}
		var cs ContentSummary
		var model string
		if e.Message != nil {
			cs = parseContentSummary(e.Message.Content)
			model = e.Message.Model
		}

		isToolOnly := len(cs.Types) > 0
		for _, ct := range cs.Types {
			if ct != "tool_result" && ct != "tool_use" {
				isToolOnly = false
				break
			}
		}
		var contentStr string
		if e.Message != nil {
			json.Unmarshal(e.Message.Content, &contentStr)
		}
		isSystem := strings.HasPrefix(contentStr, "<")

		role := e.Type
		if e.Message != nil {
			role = e.Message.Role
		}

		raw, _ := e.Marshal()

		messages = append(messages, Message{
			UUID:           e.UUID,
			ParentUUID:     e.GetParentUUID(),
			Type:           e.Type,
			Role:           role,
			Timestamp:      e.Timestamp,
			IsSidechain:    e.IsSidechain,
			ContentSummary: cs,
			IsToolOnly:     isToolOnly,
			IsSystem:       isSystem,
			Model:          model,
			Raw:            raw,
		})
	}
	return &ConversationView{Messages: messages, TotalSize: info.Size(), SessionID: sessionID}, nil
}

func (a *App) SaveConversation(projectID, sessionID string, req SaveRequest) (*SaveResult, error) {
	path := filepath.Join(claudeDir(), projectID, sessionID+".jsonl")
	conv, err := LoadConversation(path)
	if err != nil {
		return nil, fmt.Errorf("session not found: %s", sessionID)
	}

	keepSet := make(map[string]bool)
	for _, u := range req.KeepUUIDs {
		keepSet[u] = true
	}

	// Parse inserted lines as entries
	var insertEntries []*JSONLEntry
	for _, il := range req.InsertLines {
		ie, err := ParseEntry([]byte(il))
		if err == nil {
			insertEntries = append(insertEntries, ie)
		}
	}

	// Build output: keep non-messages + kept messages, insert at first deletion gap
	var result []*JSONLEntry
	insertedAtGap := false
	for _, e := range conv.Entries {
		if !e.IsMessage() {
			result = append(result, e)
			continue
		}
		if !keepSet[e.UUID] {
			if !insertedAtGap && len(insertEntries) > 0 {
				result = append(result, insertEntries...)
				insertedAtGap = true
			}
			continue
		}
		result = append(result, e)
	}
	conv.Entries = result

	backup, err := conv.SaveWithBackup(path)
	if err != nil {
		return nil, err
	}

	newInfo, _ := os.Stat(path)
	return &SaveResult{
		Success:   true,
		KeptLines: conv.EntryCount(),
		NewSize:   newInfo.Size(),
		Backup:    backup,
	}, nil
}

// EditMessage updates the text content of a specific message.
func (a *App) EditMessage(projectID, sessionID, uuid, newText string) error {
	path := filepath.Join(claudeDir(), projectID, sessionID+".jsonl")
	conv, err := LoadConversation(path)
	if err != nil {
		return err
	}

	e := conv.FindByUUID(uuid)
	if e == nil || e.Message == nil {
		return fmt.Errorf("message not found: %s", uuid)
	}

	content := e.Message.Content
	var s string
	if json.Unmarshal(content, &s) == nil {
		e.Message.Content, _ = json.Marshal(newText)
	} else {
		var arr []json.RawMessage
		if json.Unmarshal(content, &arr) == nil {
			first := true
			for i, item := range arr {
				var block map[string]json.RawMessage
				if json.Unmarshal(item, &block) != nil {
					continue
				}
				var t string
				json.Unmarshal(block["type"], &t)
				if t == "text" {
					if first {
						block["text"], _ = json.Marshal(newText)
						first = false
					} else {
						block["text"] = json.RawMessage(`""`)
					}
					arr[i], _ = json.Marshal(block)
				}
			}
			e.Message.Content, _ = json.Marshal(arr)
		}
	}

	return conv.WriteToFile(path)
}

// setSidechainFrom sets isSidechain on all entries after fromUUID.
// If toSidechain=true, marks them as sidechain; false restores to main.
func (a *App) setSidechainFrom(projectID, sessionID, fromUUID string, toSidechain bool) error {
	path := filepath.Join(claudeDir(), projectID, sessionID+".jsonl")
	conv, err := LoadConversation(path)
	if err != nil {
		return err
	}

	// In a linear conversation, "descendants" = everything after fromUUID
	found := false
	for _, e := range conv.Entries {
		if e.UUID == fromUUID {
			found = true
			continue
		}
		if found && e.UUID != "" {
			e.IsSidechain = toSidechain
		}
	}

	return conv.WriteToFile(path)
}

// InsertMessage inserts a new message immediately after afterUUID.
func (a *App) InsertMessage(projectID, sessionID, afterUUID, role, text string) error {
	path := filepath.Join(claudeDir(), projectID, sessionID+".jsonl")
	conv, err := LoadConversation(path)
	if err != nil {
		return err
	}

	newEntry := conv.NewEntry(role, role, text)
	conv.InsertAfter(afterUUID, newEntry)

	return conv.WriteToFile(path)
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
	srcPath := filepath.Join(claudeDir(), projectID, sessionID+".jsonl")
	conv, err := LoadConversation(srcPath)
	if err != nil {
		return "", err
	}

	conv.TruncateAfter(fromUUID)

	newID := generateUUID()
	dstPath := filepath.Join(claudeDir(), projectID, newID+".jsonl")
	if err := conv.WriteToFile(dstPath); err != nil {
		return "", err
	}
	return newID, nil
}

// extractSelectedMessages returns raw message objects (role+content) for selected UUIDs, in file order.
// selectEntries returns entries matching the given UUID set, in file order.
func selectEntries(entries []*JSONLEntry, uuidSet map[string]bool) []*JSONLEntry {
	var result []*JSONLEntry
	for _, e := range entries {
		if uuidSet[e.UUID] {
			result = append(result, e)
		}
	}
	return result
}

// SummarizeMessages calls `claude -p` to summarize the selected messages.
func (a *App) SummarizeMessages(projectID, sessionID string, uuids []string) (string, error) {
	path := filepath.Join(claudeDir(), projectID, sessionID+".jsonl")
	conv, err := LoadConversation(path)
	if err != nil {
		return "", err
	}
	uuidSet := make(map[string]bool, len(uuids))
	for _, u := range uuids {
		uuidSet[u] = true
	}
	selected := selectEntries(conv.Entries, uuidSet)
	if len(selected) == 0 {
		return "", fmt.Errorf("no selected messages found")
	}

	var parts []string
	for _, e := range selected {
		role := e.Type
		if e.Message != nil {
			role = e.Message.Role
		}
		text := ""
		if e.Message != nil {
			text = extractText(e.Message.Content)
		}
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

// IdealizeMessages analyzes selected messages and returns either:
// - {"mode":"actions","actions":[{uuid,action,edited_content}]} for per-message triage
// - {"mode":"rewrite","messages":[{role,content}]} for full rewrite
func (a *App) IdealizeMessages(projectID, sessionID string, uuids []string) (string, error) {
	path := filepath.Join(claudeDir(), projectID, sessionID+".jsonl")
	conv, err := LoadConversation(path)
	if err != nil {
		return "", err
	}
	uuidSet := make(map[string]bool, len(uuids))
	for _, u := range uuids {
		uuidSet[u] = true
	}

	type msgWithUUID struct {
		UUID    string          `json:"uuid"`
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
	}
	var labeled []msgWithUUID
	for _, e := range selectEntries(conv.Entries, uuidSet) {
		role := e.Type
		var content json.RawMessage
		if e.Message != nil {
			role = e.Message.Role
			content = e.Message.Content
		}
		labeled = append(labeled, msgWithUUID{UUID: e.UUID, Role: role, Content: content})
	}

	if len(labeled) == 0 {
		return "", fmt.Errorf("no selected messages found")
	}

	schema := buildIdealizeSchema(uuids)
	msgsJSON, _ := json.MarshalIndent(labeled, "", "  ")
	prompt := "Analyze each message and decide delete/keep/edit, or rewrite entirely:\n\n" + string(msgsJSON)

	out, err := runClaudeWithSystem(prompt, schema, idealizeSystemPrompt)
	if err != nil {
		return "", fmt.Errorf("claude failed: %w", err)
	}

	return out, nil
}

// ApplyIdealized creates a new session replacing the selected range with idealized messages.
// Returns the new session ID.
func (a *App) ApplyIdealized(projectID, sessionID string, uuids []string, messagesJSON string) (string, error) {
	var idealMsgs []struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
	}
	if err := json.Unmarshal([]byte(messagesJSON), &idealMsgs); err != nil {
		return "", fmt.Errorf("invalid messages JSON: %w", err)
	}

	path := filepath.Join(claudeDir(), projectID, sessionID+".jsonl")
	conv, err := LoadConversation(path)
	if err != nil {
		return "", err
	}

	uuidSet := make(map[string]bool, len(uuids))
	for _, u := range uuids {
		uuidSet[u] = true
	}

	// Build new entries for idealized messages
	var newEntries []*JSONLEntry
	for _, im := range idealMsgs {
		e := conv.NewEntryWithBlocks(im.Role, NewMessageContentBlocks(im.Role, im.Content))
		newEntries = append(newEntries, e)
	}

	conv.ReplaceRange(uuidSet, newEntries)

	newID := generateUUID()
	dstPath := filepath.Join(claudeDir(), projectID, newID+".jsonl")
	if err := conv.WriteToFile(dstPath); err != nil {
		return "", err
	}
	return newID, nil
}

// FastCompact runs rule-based compaction on a session and returns a report.
// If dryRun is true, no changes are written.
func (a *App) FastCompact(projectID, sessionID string, ruleNames []string, dryRun bool) (*CompactReport, error) {
	path := filepath.Join(claudeDir(), projectID, sessionID+".jsonl")
	conv, err := LoadConversation(path)
	if err != nil {
		return nil, fmt.Errorf("session not found: %s", sessionID)
	}

	rules := selectRules(ruleNames)
	report := RunCompaction(conv, rules)

	if !dryRun {
		if _, err := conv.SaveWithBackup(path); err != nil {
			return nil, err
		}
	}

	return &report, nil
}

// ValidateSession checks a session for chain and tool pairing issues.
func (a *App) ValidateSession(projectID, sessionID string) ([]ChainIssue, error) {
	path := filepath.Join(claudeDir(), projectID, sessionID+".jsonl")
	conv, err := LoadConversation(path)
	if err != nil {
		return nil, err
	}
	return conv.ValidateChain(), nil
}

// FixSession repairs chain issues and tool pairing problems in a session.
func (a *App) FixSession(projectID, sessionID string) (*CompactReport, error) {
	path := filepath.Join(claudeDir(), projectID, sessionID+".jsonl")
	conv, err := LoadConversation(path)
	if err != nil {
		return nil, err
	}

	rules := []CompactRule{
		&RepairToolPairsRule{},
		&FillMissingToolResultsRule{},
		&MergeConsecutiveRule{},
	}
	report := RunCompaction(conv, rules)

	if _, err := conv.SaveWithBackup(path); err != nil {
		return nil, err
	}
	return &report, nil
}

// ApplySummary replaces the selected messages with a single summary user message.
func (a *App) ApplySummary(projectID, sessionID string, uuids []string, summary string) error {
	path := filepath.Join(claudeDir(), projectID, sessionID+".jsonl")
	conv, err := LoadConversation(path)
	if err != nil {
		return err
	}

	uuidSet := make(map[string]bool, len(uuids))
	for _, u := range uuids {
		uuidSet[u] = true
	}

	summaryEntry := conv.NewEntry("user", "user", "[Summary]\n"+summary)
	summaryEntry.IsSummary = true

	conv.ReplaceRange(uuidSet, []*JSONLEntry{summaryEntry})

	return conv.WriteToFile(path)
}
