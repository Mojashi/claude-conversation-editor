package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestExtractMainChain verifies that extractMainChain returns all entries
// on the Pa_ path from leaf to root, in file order.
func TestExtractMainChain(t *testing.T) {
	// Build a linear chain: A → B → C → D
	entries := []*JSONLEntry{
		makeEntry("a", "", "user", "hello"),
		makeEntry("b", "a", "assistant", "hi"),
		makeEntry("c", "b", "user", "question"),
		makeEntry("d", "c", "assistant", "answer"),
	}
	chain := extractMainChain(entries)
	if len(chain) != 4 {
		t.Errorf("expected 4 entries in chain, got %d", len(chain))
	}
}

// TestExtractMainChainWithProgress verifies progress entries on the chain are included.
func TestExtractMainChainWithProgress(t *testing.T) {
	entries := []*JSONLEntry{
		makeEntry("a", "", "user", "hello"),
		makeEntry("b", "a", "assistant", "hi"),
		makeEntry("p1", "b", "progress", ""),
		makeEntry("c", "p1", "user", "question"),
		makeEntry("d", "c", "assistant", "answer"),
	}
	chain := extractMainChain(entries)
	if len(chain) != 5 {
		t.Errorf("expected 5 entries (including progress), got %d", len(chain))
	}
}

// TestExtractMainChainWithFork verifies only the main branch is kept.
func TestExtractMainChainWithFork(t *testing.T) {
	// A → B → C (main)
	//       → D (fork, not on main path since C is after D in file order)
	entries := []*JSONLEntry{
		makeEntry("a", "", "user", "hello"),
		makeEntry("b", "a", "assistant", "hi"),
		makeEntry("d", "b", "user", "fork"),       // fork
		makeEntry("c", "b", "user", "main"),        // main (later in file)
		makeEntry("e", "c", "assistant", "answer"),  // continues from c
	}
	chain := extractMainChain(entries)
	// Leaf is "e", path: e → c → b → a. "d" is excluded.
	uuids := make(map[string]bool)
	for _, e := range chain {
		uuids[e.UUID] = true
	}
	if uuids["d"] {
		t.Error("fork entry 'd' should not be in chain")
	}
	if !uuids["c"] {
		t.Error("main entry 'c' should be in chain")
	}
	if len(chain) != 4 {
		t.Errorf("expected 4 entries in chain, got %d", len(chain))
	}
}

// TestExtractMainChainDuplicateUUID verifies duplicate UUIDs are deduplicated.
func TestExtractMainChainDuplicateUUID(t *testing.T) {
	// A → B → C, but B appears twice in the file
	entries := []*JSONLEntry{
		makeEntry("a", "", "user", "hello"),
		makeEntry("b", "a", "assistant", "first"),  // first B
		makeEntry("b", "a", "assistant", "second"), // duplicate B (same UUID)
		makeEntry("c", "b", "user", "question"),
	}
	chain := extractMainChain(entries)
	// Should deduplicate: keep last occurrence of "b"
	uuidCount := make(map[string]int)
	for _, e := range chain {
		uuidCount[e.UUID]++
	}
	for uuid, count := range uuidCount {
		if count > 1 {
			t.Errorf("UUID %s appears %d times in chain (should be 1)", uuid, count)
		}
	}
	if len(chain) != 3 {
		t.Errorf("expected 3 entries, got %d", len(chain))
	}
}

// TestLoadConversationExtractsChain verifies LoadConversation returns chain only.
func TestLoadConversationExtractsChain(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.jsonl")

	entries := []*JSONLEntry{
		makeEntry("a", "", "user", "hello"),
		makeEntry("b", "a", "assistant", "hi"),
		makeEntry("p1", "b", "progress", ""),
		makeEntry("fork", "b", "user", "fork"), // fork, not on main
		makeEntry("c", "p1", "user", "question"),
		makeEntry("d", "c", "assistant", "answer"),
	}
	writeTestJSONL(t, path, entries)

	conv, err := LoadConversation(path)
	if err != nil {
		t.Fatal(err)
	}
	// Chain: a → b → p1 → c → d (5). "fork" is excluded.
	if len(conv.Entries) != 5 {
		t.Errorf("expected 5 entries, got %d", len(conv.Entries))
		for _, e := range conv.Entries {
			t.Logf("  %s (%s)", e.UUID, e.Type)
		}
	}
}

// TestRebuildLinearChain verifies WriteToFile creates a proper linear chain.
func TestRebuildLinearChain(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.jsonl")
	outPath := filepath.Join(dir, "out.jsonl")

	entries := []*JSONLEntry{
		makeEntry("a", "", "user", "hello"),
		makeEntry("b", "a", "assistant", "hi"),
		makeEntry("p1", "b", "progress", ""),
		makeEntry("c", "p1", "user", "question"),
		makeEntry("d", "c", "assistant", "answer"),
	}
	writeTestJSONL(t, path, entries)

	conv, err := LoadConversation(path)
	if err != nil {
		t.Fatal(err)
	}

	// Remove progress
	var filtered []*JSONLEntry
	for _, e := range conv.Entries {
		if e.Type != "progress" {
			filtered = append(filtered, e)
		}
	}
	conv.Entries = filtered

	if err := conv.WriteToFile(outPath); err != nil {
		t.Fatal(err)
	}

	// Reload and verify chain
	conv2, err := LoadConversation(outPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(conv2.Entries) != 4 {
		t.Errorf("expected 4 entries, got %d", len(conv2.Entries))
	}

	// Verify all entries are on the chain
	byUUID := make(map[string]*JSONLEntry)
	for _, e := range conv2.Entries {
		byUUID[e.UUID] = e
	}
	var leaf *JSONLEntry
	for i := len(conv2.Entries) - 1; i >= 0; i-- {
		if conv2.Entries[i].UUID != "" {
			leaf = conv2.Entries[i]
			break
		}
	}
	chainLen := 0
	visited := make(map[string]bool)
	cur := leaf
	for cur != nil {
		if visited[cur.UUID] {
			break
		}
		visited[cur.UUID] = true
		chainLen++
		if cur.ParentUUID == nil {
			break
		}
		cur = byUUID[*cur.ParentUUID]
	}
	if chainLen != len(conv2.Entries) {
		t.Errorf("chain length %d != entry count %d", chainLen, len(conv2.Entries))
	}
}

// TestCompactPreservesChain verifies that after compaction, all entries
// remain on the Pa_ chain (chain length == entry count).
func TestCompactPreservesChain(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.jsonl")
	outPath := filepath.Join(dir, "out.jsonl")

	entries := []*JSONLEntry{
		makeEntry("a", "", "user", "hello"),
		makeEntryWithMessage("b", "a", "assistant", &EntryMessage{
			Role:    "assistant",
			Content: mustMarshal([]map[string]string{{"type": "text", "text": "thinking..."}}),
			ID:      "msg_001",
		}),
		makeEntry("p1", "b", "progress", ""),
		makeEntry("p2", "p1", "progress", ""),
		makeEntryWithMessage("c", "p2", "assistant", &EntryMessage{
			Role:    "assistant",
			Content: mustMarshal([]map[string]interface{}{{"type": "tool_use", "id": "toolu_01AAA", "name": "Bash", "input": map[string]string{"command": "ls"}}}),
			ID:      "msg_002",
		}),
		makeEntryWithMessage("d", "c", "user", &EntryMessage{
			Role:    "user",
			Content: mustMarshal([]map[string]interface{}{{"type": "tool_result", "tool_use_id": "toolu_01AAA", "content": "file.txt"}}),
		}),
		makeEntryWithMessage("e", "d", "assistant", &EntryMessage{
			Role:    "assistant",
			Content: mustMarshal([]map[string]string{{"type": "text", "text": "done"}}),
			ID:      "msg_003",
		}),
	}
	writeTestJSONL(t, path, entries)

	conv, err := LoadConversation(path)
	if err != nil {
		t.Fatal(err)
	}

	report := RunCompaction(conv, AllRules())
	_ = report

	if err := conv.WriteToFile(outPath); err != nil {
		t.Fatal(err)
	}

	// Reload and verify
	conv2, err := LoadConversation(outPath)
	if err != nil {
		t.Fatal(err)
	}

	totalWithUUID := 0
	for _, e := range conv2.Entries {
		if e.UUID != "" {
			totalWithUUID++
		}
	}

	byUUID := make(map[string]*JSONLEntry)
	for _, e := range conv2.Entries {
		if e.UUID != "" {
			byUUID[e.UUID] = e
		}
	}
	var leaf *JSONLEntry
	for i := len(conv2.Entries) - 1; i >= 0; i-- {
		if conv2.Entries[i].UUID != "" {
			leaf = conv2.Entries[i]
			break
		}
	}
	chainLen := 0
	visited := make(map[string]bool)
	cur := leaf
	for cur != nil {
		if visited[cur.UUID] {
			t.Fatal("cycle in chain")
		}
		visited[cur.UUID] = true
		chainLen++
		if cur.ParentUUID == nil {
			break
		}
		cur = byUUID[*cur.ParentUUID]
	}
	if chainLen != totalWithUUID {
		t.Errorf("chain length %d != entries with UUID %d (entries dropped from chain!)", chainLen, totalWithUUID)
	}
}

// TestCompactPreservesMessageID verifies message.id is preserved.
func TestCompactPreservesMessageID(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.jsonl")
	outPath := filepath.Join(dir, "out.jsonl")

	entries := []*JSONLEntry{
		makeEntry("a", "", "user", "hello"),
		makeEntryWithMessage("b", "a", "assistant", &EntryMessage{
			Role:    "assistant",
			Content: mustMarshal([]map[string]string{{"type": "text", "text": "hi"}}),
			ID:      "msg_12345",
		}),
	}
	writeTestJSONL(t, path, entries)

	conv, err := LoadConversation(path)
	if err != nil {
		t.Fatal(err)
	}
	RunCompaction(conv, AllRules())
	conv.WriteToFile(outPath)

	// Read raw and check message.id
	data, _ := os.ReadFile(outPath)
	lines := splitLines(data)
	for _, line := range lines {
		var raw map[string]json.RawMessage
		json.Unmarshal(line, &raw)
		var typ string
		json.Unmarshal(raw["type"], &typ)
		if typ == "assistant" {
			var msg map[string]json.RawMessage
			json.Unmarshal(raw["message"], &msg)
			var id string
			json.Unmarshal(msg["id"], &id)
			if id != "msg_12345" {
				t.Errorf("message.id lost: expected msg_12345, got %q", id)
			}
		}
	}
}

// TestTruncateOldReads verifies that non-last Reads of the same file are truncated.
func TestTruncateOldReads(t *testing.T) {
	entries := []*JSONLEntry{
		makeEntry("a", "", "user", "hello"),
		// First Read of file.ts
		makeEntryWithMessage("b", "a", "assistant", &EntryMessage{
			Role: "assistant",
			Content: mustMarshal([]map[string]interface{}{
				{"type": "tool_use", "id": "tu1", "name": "Read", "input": map[string]string{"file_path": "/tmp/file.ts"}},
			}),
			ID: "msg1",
		}),
		makeEntryWithMessage("c", "b", "user", &EntryMessage{
			Role: "user",
			Content: mustMarshal([]map[string]interface{}{
				{"type": "tool_result", "tool_use_id": "tu1", "content": "line1\nline2\nline3\nthis is the full file content that is quite long"},
			}),
		}),
		// Some work
		makeEntryWithMessage("d", "c", "assistant", &EntryMessage{
			Role:    "assistant",
			Content: mustMarshal([]map[string]string{{"type": "text", "text": "I see the file"}}),
			ID:      "msg2",
		}),
		// Second Read of same file (last)
		makeEntryWithMessage("e", "d", "assistant", &EntryMessage{
			Role: "assistant",
			Content: mustMarshal([]map[string]interface{}{
				{"type": "tool_use", "id": "tu2", "name": "Read", "input": map[string]string{"file_path": "/tmp/file.ts"}},
			}),
			ID: "msg3",
		}),
		makeEntryWithMessage("f", "e", "user", &EntryMessage{
			Role: "user",
			Content: mustMarshal([]map[string]interface{}{
				{"type": "tool_result", "tool_use_id": "tu2", "content": "line1\nline2\nline3\nupdated content"},
			}),
		}),
	}

	rule := &TruncateOldReadsRule{}
	result, report := rule.Apply(entries)

	if report.BytesSaved <= 0 {
		t.Error("expected bytes saved > 0")
	}

	// Verify first Read was truncated
	for _, e := range result {
		if e.UUID != "c" {
			continue
		}
		var blocks []json.RawMessage
		json.Unmarshal(e.Message.Content, &blocks)
		for _, block := range blocks {
			var b map[string]json.RawMessage
			json.Unmarshal(block, &b)
			var content string
			json.Unmarshal(b["content"], &content)
			if content == "line1\nline2\nline3\nthis is the full file content that is quite long" {
				t.Error("first Read should have been truncated")
			}
			if len(content) > 100 {
				t.Errorf("truncated content too long: %d chars", len(content))
			}
		}
	}

	// Verify second Read was NOT truncated
	for _, e := range result {
		if e.UUID != "f" {
			continue
		}
		var blocks []json.RawMessage
		json.Unmarshal(e.Message.Content, &blocks)
		for _, block := range blocks {
			var b map[string]json.RawMessage
			json.Unmarshal(block, &b)
			var content string
			json.Unmarshal(b["content"], &content)
			if content != "line1\nline2\nline3\nupdated content" {
				t.Errorf("last Read should not be truncated, got: %s", content)
			}
		}
	}
}

// TestTruncateOldReadsKeepsSingleRead verifies single Reads are not touched.
func TestTruncateOldReadsKeepsSingleRead(t *testing.T) {
	entries := []*JSONLEntry{
		makeEntry("a", "", "user", "hello"),
		makeEntryWithMessage("b", "a", "assistant", &EntryMessage{
			Role: "assistant",
			Content: mustMarshal([]map[string]interface{}{
				{"type": "tool_use", "id": "tu1", "name": "Read", "input": map[string]string{"file_path": "/tmp/only.ts"}},
			}),
			ID: "msg1",
		}),
		makeEntryWithMessage("c", "b", "user", &EntryMessage{
			Role: "user",
			Content: mustMarshal([]map[string]interface{}{
				{"type": "tool_result", "tool_use_id": "tu1", "content": "full content here"},
			}),
		}),
	}

	rule := &TruncateOldReadsRule{}
	_, report := rule.Apply(entries)

	if report.BytesSaved != 0 {
		t.Errorf("single Read should not be truncated, but saved %d bytes", report.BytesSaved)
	}
}

// TestTruncateOldWrites verifies that non-last Write/Edit inputs are truncated.
func TestTruncateOldWrites(t *testing.T) {
	bigContent := strings.Repeat("x", 5000)
	entries := []*JSONLEntry{
		makeEntry("a", "", "user", "hello"),
		// Write file.ts (first)
		makeEntryWithMessage("b", "a", "assistant", &EntryMessage{
			Role: "assistant",
			Content: mustMarshal([]map[string]interface{}{
				{"type": "tool_use", "id": "tu1", "name": "Write", "input": map[string]string{
					"file_path": "/tmp/file.ts",
					"content":   bigContent,
				}},
			}),
			ID: "msg1",
		}),
		makeEntryWithMessage("c", "b", "user", &EntryMessage{
			Role:    "user",
			Content: mustMarshal([]map[string]interface{}{{"type": "tool_result", "tool_use_id": "tu1", "content": "File created successfully"}}),
		}),
		// Edit file.ts (last)
		makeEntryWithMessage("d", "c", "assistant", &EntryMessage{
			Role: "assistant",
			Content: mustMarshal([]map[string]interface{}{
				{"type": "tool_use", "id": "tu2", "name": "Edit", "input": map[string]string{
					"file_path":  "/tmp/file.ts",
					"old_string": "xxx",
					"new_string": "yyy",
				}},
			}),
			ID: "msg2",
		}),
		makeEntryWithMessage("e", "d", "user", &EntryMessage{
			Role:    "user",
			Content: mustMarshal([]map[string]interface{}{{"type": "tool_result", "tool_use_id": "tu2", "content": "File updated"}}),
		}),
	}

	rule := &TruncateOldWritesRule{}
	result, report := rule.Apply(entries)

	if report.BytesSaved <= 0 {
		t.Error("expected bytes saved > 0")
	}

	// Verify first Write was truncated (input.content replaced)
	for _, e := range result {
		if e.UUID != "b" {
			continue
		}
		var blocks []json.RawMessage
		json.Unmarshal(e.Message.Content, &blocks)
		for _, block := range blocks {
			var b map[string]json.RawMessage
			json.Unmarshal(block, &b)
			var typ string
			json.Unmarshal(b["type"], &typ)
			if typ == "tool_use" {
				inputStr := string(b["input"])
				if strings.Contains(inputStr, bigContent) {
					t.Error("first Write content should have been truncated")
				}
				if !strings.Contains(inputStr, "see later version") {
					t.Error("truncated Write should contain placeholder")
				}
			}
		}
	}

	// Verify last Edit was NOT truncated
	for _, e := range result {
		if e.UUID != "d" {
			continue
		}
		var blocks []json.RawMessage
		json.Unmarshal(e.Message.Content, &blocks)
		for _, block := range blocks {
			var b map[string]json.RawMessage
			json.Unmarshal(block, &b)
			var typ string
			json.Unmarshal(b["type"], &typ)
			if typ == "tool_use" {
				inputStr := string(b["input"])
				if !strings.Contains(inputStr, "yyy") {
					t.Error("last Edit should not be truncated")
				}
			}
		}
	}
}

// TestCompactOnRealSession tests compact on a real session file if available.
func TestCompactOnRealSession(t *testing.T) {
	// Use a known test session if it exists
	testFile := "/tmp/test-session-backup.jsonl"
	if _, err := os.Stat(testFile); err != nil {
		t.Skip("no test session backup at /tmp/test-session-backup.jsonl")
	}

	conv, err := LoadConversation(testFile)
	if err != nil {
		t.Fatal(err)
	}

	beforeEntries := len(conv.Entries)
	report := RunCompaction(conv, AllRules())
	afterEntries := len(conv.Entries)

	t.Logf("Before: %d entries, After: %d entries, Saved: %d bytes",
		beforeEntries, afterEntries, report.TotalSaved)

	// Verify chain integrity
	totalWithUUID := 0
	byUUID := make(map[string]*JSONLEntry)
	for _, e := range conv.Entries {
		if e.UUID != "" {
			totalWithUUID++
			byUUID[e.UUID] = e
		}
	}

	dir := t.TempDir()
	outPath := filepath.Join(dir, "out.jsonl")
	conv.WriteToFile(outPath)

	conv2, _ := LoadConversation(outPath)
	byUUID2 := make(map[string]*JSONLEntry)
	for _, e := range conv2.Entries {
		if e.UUID != "" {
			byUUID2[e.UUID] = e
		}
	}

	var leaf *JSONLEntry
	for i := len(conv2.Entries) - 1; i >= 0; i-- {
		if conv2.Entries[i].UUID != "" {
			leaf = conv2.Entries[i]
			break
		}
	}
	chainLen := 0
	visited := make(map[string]bool)
	cur := leaf
	for cur != nil {
		if visited[cur.UUID] {
			t.Fatal("cycle in chain")
		}
		visited[cur.UUID] = true
		chainLen++
		if cur.ParentUUID == nil {
			break
		}
		cur = byUUID2[*cur.ParentUUID]
	}

	totalWithUUID2 := 0
	for _, e := range conv2.Entries {
		if e.UUID != "" {
			totalWithUUID2++
		}
	}

	if chainLen != totalWithUUID2 {
		t.Errorf("chain length %d != entries with UUID %d after reload", chainLen, totalWithUUID2)
	}
}

// --- helpers ---

func makeEntry(uuid, parent, typ, text string) *JSONLEntry {
	msg := &EntryMessage{
		Role:    typ,
		Content: json.RawMessage(`"` + text + `"`),
	}
	if typ == "progress" {
		msg = nil
	}
	e := &JSONLEntry{
		UUID:    uuid,
		Type:    typ,
		Message: msg,
		raw:     make(map[string]json.RawMessage),
	}
	if parent != "" {
		e.SetParentUUID(parent)
	}
	return e
}

func makeEntryWithMessage(uuid, parent, typ string, msg *EntryMessage) *JSONLEntry {
	e := &JSONLEntry{
		UUID:    uuid,
		Type:    typ,
		Message: msg,
		raw:     make(map[string]json.RawMessage),
	}
	if parent != "" {
		e.SetParentUUID(parent)
	}
	return e
}

func writeTestJSONL(t *testing.T, path string, entries []*JSONLEntry) {
	t.Helper()
	if err := WriteJSONLFile(path, entries); err != nil {
		t.Fatal(err)
	}
}

func mustMarshal(v interface{}) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}

func splitLines(data []byte) [][]byte {
	var lines [][]byte
	for _, line := range json.RawMessage(data) {
		_ = line
	}
	// Simple split
	start := 0
	for i, b := range data {
		if b == '\n' {
			if i > start {
				lines = append(lines, data[start:i])
			}
			start = i + 1
		}
	}
	if start < len(data) {
		lines = append(lines, data[start:])
	}
	return lines
}
