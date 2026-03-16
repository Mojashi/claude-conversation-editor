package main

import (
	"bufio"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"os"
	"time"
)

// generateUUID creates a UUID v4-ish string.
func generateUUID() string {
	b := make([]byte, 16)
	rand.Read(b)
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
		b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// CompactMetadata holds metadata about a compaction event.
type CompactMetadata struct {
	Trigger   string `json:"trigger"`
	PreTokens int    `json:"preTokens"`
}

// JSONLEntry represents a single line in a Claude Code JSONL session file.
// It preserves all fields for round-tripping, even unknown ones.
type JSONLEntry struct {
	// Known fields (typed accessors)
	UUID              string           `json:"-"`
	ParentUUID        *string          `json:"-"` // nil = null
	Type              string           `json:"-"`
	Subtype           string           `json:"-"`
	Timestamp         string           `json:"-"`
	SessionID         string           `json:"-"`
	IsSidechain       bool             `json:"-"`
	IsSummary         bool             `json:"-"`
	LogicalParentUUID *string          `json:"-"` // non-nil on compact_boundary
	CompactMeta       *CompactMetadata `json:"-"` // non-nil on compact_boundary
	Message           *EntryMessage    `json:"-"`

	// Raw stores ALL fields including the known ones, for round-tripping
	raw map[string]json.RawMessage
}

// EntryMessage is the message field inside a JSONL entry.
type EntryMessage struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
	Model   string          `json:"model,omitempty"`
}

// ParseEntry parses a single JSONL line into a JSONLEntry.
func ParseEntry(data []byte) (*JSONLEntry, error) {
	e := &JSONLEntry{
		raw: make(map[string]json.RawMessage),
	}
	if err := json.Unmarshal(data, &e.raw); err != nil {
		return nil, err
	}
	// Extract known fields
	if v, ok := e.raw["uuid"]; ok {
		json.Unmarshal(v, &e.UUID)
	}
	if v, ok := e.raw["parentUuid"]; ok {
		if string(v) != "null" {
			var s string
			json.Unmarshal(v, &s)
			e.ParentUUID = &s
		}
	}
	if v, ok := e.raw["type"]; ok {
		json.Unmarshal(v, &e.Type)
	}
	if v, ok := e.raw["timestamp"]; ok {
		json.Unmarshal(v, &e.Timestamp)
	}
	if v, ok := e.raw["sessionId"]; ok {
		json.Unmarshal(v, &e.SessionID)
	}
	if v, ok := e.raw["isSidechain"]; ok {
		json.Unmarshal(v, &e.IsSidechain)
	}
	if v, ok := e.raw["subtype"]; ok {
		json.Unmarshal(v, &e.Subtype)
	}
	if v, ok := e.raw["isSummary"]; ok {
		json.Unmarshal(v, &e.IsSummary)
	}
	if v, ok := e.raw["logicalParentUuid"]; ok {
		if string(v) != "null" {
			var s string
			json.Unmarshal(v, &s)
			e.LogicalParentUUID = &s
		}
	}
	if v, ok := e.raw["compactMetadata"]; ok {
		var cm CompactMetadata
		if json.Unmarshal(v, &cm) == nil {
			e.CompactMeta = &cm
		}
	}
	if v, ok := e.raw["message"]; ok {
		var msg EntryMessage
		if json.Unmarshal(v, &msg) == nil {
			e.Message = &msg
		}
	}
	return e, nil
}

// Marshal serializes the entry back to JSON, merging known fields into raw.
func (e *JSONLEntry) Marshal() ([]byte, error) {
	if e.raw == nil {
		e.raw = make(map[string]json.RawMessage)
	}
	// Sync known fields back to raw
	if e.UUID != "" {
		e.raw["uuid"], _ = json.Marshal(e.UUID)
	}
	if e.ParentUUID != nil {
		e.raw["parentUuid"], _ = json.Marshal(*e.ParentUUID)
	} else {
		e.raw["parentUuid"] = json.RawMessage("null")
	}
	if e.Type != "" {
		e.raw["type"], _ = json.Marshal(e.Type)
	}
	if e.Timestamp != "" {
		e.raw["timestamp"], _ = json.Marshal(e.Timestamp)
	}
	if e.SessionID != "" {
		e.raw["sessionId"], _ = json.Marshal(e.SessionID)
	}
	e.raw["isSidechain"], _ = json.Marshal(e.IsSidechain)
	if e.IsSummary {
		e.raw["isSummary"], _ = json.Marshal(e.IsSummary)
	}
	if e.Message != nil {
		e.raw["message"], _ = json.Marshal(e.Message)
	}
	return json.Marshal(e.raw)
}

// MarshalString is a convenience wrapper.
func (e *JSONLEntry) MarshalString() string {
	b, _ := e.Marshal()
	return string(b)
}

// SetParentUUID sets parentUuid (helper to avoid pointer boilerplate).
func (e *JSONLEntry) SetParentUUID(uuid string) {
	e.ParentUUID = &uuid
}

// GetParentUUID returns parentUuid or "" if null.
func (e *JSONLEntry) GetParentUUID() string {
	if e.ParentUUID == nil {
		return ""
	}
	return *e.ParentUUID
}

// IsMessage returns true if the entry is a user or assistant message.
func (e *JSONLEntry) IsMessage() bool {
	return e.Type == "user" || e.Type == "assistant"
}

// IsCompactBoundary returns true if the entry is a compaction boundary marker.
func (e *JSONLEntry) IsCompactBoundary() bool {
	return e.Type == "system" && e.Subtype == "compact_boundary"
}

// NewEntry creates a new JSONL entry with all required fields.
func NewEntry(uuid, parentUUID, entryType, sessionID string, message *EntryMessage) *JSONLEntry {
	var parent *string
	if parentUUID != "" {
		parent = &parentUUID
	}
	return &JSONLEntry{
		UUID:        uuid,
		ParentUUID:  parent,
		Type:        entryType,
		Timestamp:   time.Now().UTC().Format("2006-01-02T15:04:05.000Z"),
		SessionID:   sessionID,
		IsSidechain: false,
		Message:     message,
		raw:         make(map[string]json.RawMessage),
	}
}

// NewMessageContent creates an EntryMessage with text content as content blocks.
func NewMessageContent(role, text string) *EntryMessage {
	blocks := []map[string]string{{"type": "text", "text": text}}
	content, _ := json.Marshal(blocks)
	return &EntryMessage{Role: role, Content: content}
}

// NewMessageContentBlocks creates an EntryMessage with content blocks.
func NewMessageContentBlocks(role string, content json.RawMessage) *EntryMessage {
	return &EntryMessage{Role: role, Content: content}
}

// ReadJSONLFile reads all JSONL entries from a file.
func ReadJSONLFile(path string) ([]*JSONLEntry, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return ReadJSONLEntries(f)
}

// ReadJSONLEntries reads all JSONL entries from a reader.
func ReadJSONLEntries(f *os.File) ([]*JSONLEntry, error) {
	var entries []*JSONLEntry
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 4*1024*1024), 4*1024*1024)
	for scanner.Scan() {
		data := make([]byte, len(scanner.Bytes()))
		copy(data, scanner.Bytes())
		entry, err := ParseEntry(data)
		if err != nil {
			// Preserve unparseable lines as-is
			entries = append(entries, &JSONLEntry{
				raw: map[string]json.RawMessage{"_raw_line": json.RawMessage(fmt.Sprintf("%q", string(data)))},
			})
			continue
		}
		entries = append(entries, entry)
	}
	return entries, scanner.Err()
}

// WriteJSONLFile writes entries to a file.
func WriteJSONLFile(path string, entries []*JSONLEntry) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	for _, e := range entries {
		b, err := e.Marshal()
		if err != nil {
			return err
		}
		fmt.Fprintln(f, string(b))
	}
	return nil
}
