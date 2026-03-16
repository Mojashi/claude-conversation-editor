package main

import (
	"fmt"
	"io"
	"os"
)

// Conversation is the core model: an ordered list of JSONL entries with
// a parentUuid tree overlay.
//
// Claude Code uses parentUuid to build a tree (via Pa_): it loads all
// entries with UUIDs into a map and walks parentUuid from the leaf to
// the root. Entries without UUIDs (file-history-snapshot, queue-operation)
// are not part of the tree.
//
// When entries are removed, their children must be reparented to the
// removed entry's parent to preserve the tree structure.
type Conversation struct {
	Entries   []*JSONLEntry
	SessionID string
}

// LoadConversation reads a JSONL file and extracts the main Pa_ chain.
// Off-chain entries (progress, sidechains, forks) are discarded at load time.
func LoadConversation(path string) (*Conversation, error) {
	allEntries, err := ReadJSONLFile(path)
	if err != nil {
		return nil, err
	}
	var sessionID string
	for _, e := range allEntries {
		if e.SessionID != "" {
			sessionID = e.SessionID
			break
		}
	}
	chain := extractMainChain(allEntries)
	return &Conversation{Entries: chain, SessionID: sessionID}, nil
}

// extractMainChain walks the parentUuid tree from the leaf back to root,
// returning only the entries on that path in file order.
func extractMainChain(entries []*JSONLEntry) []*JSONLEntry {
	byUUID := make(map[string]*JSONLEntry)
	for _, e := range entries {
		if e.UUID != "" {
			byUUID[e.UUID] = e
		}
	}

	// Find leaf (last entry with UUID)
	var leaf *JSONLEntry
	for i := len(entries) - 1; i >= 0; i-- {
		if entries[i].UUID != "" {
			leaf = entries[i]
			break
		}
	}
	if leaf == nil {
		return nil
	}

	// Walk backward from leaf
	onChain := make(map[string]bool)
	visited := make(map[string]bool)
	cur := leaf
	for cur != nil {
		if visited[cur.UUID] {
			break
		}
		visited[cur.UUID] = true
		onChain[cur.UUID] = true
		if cur.ParentUUID == nil {
			break
		}
		cur = byUUID[*cur.ParentUUID]
	}

	// Return in file order
	var chain []*JSONLEntry
	for _, e := range entries {
		if e.UUID != "" && onChain[e.UUID] {
			chain = append(chain, e)
		}
	}
	return chain
}

// Messages returns only user/assistant entries.
func (c *Conversation) Messages() []*JSONLEntry {
	var msgs []*JSONLEntry
	for _, e := range c.Entries {
		if e.IsMessage() {
			msgs = append(msgs, e)
		}
	}
	return msgs
}

// RemoveEntries removes entries matching the predicate and reparents
// their children in the parentUuid tree.
func (c *Conversation) RemoveEntries(shouldRemove func(*JSONLEntry) bool) int {
	// Build parent map for removed entries: removed UUID → its parentUUID
	removedParent := make(map[string]string) // uuid → parent uuid (or "" for root)
	for _, e := range c.Entries {
		if e.UUID == "" || !shouldRemove(e) {
			continue
		}
		parent := ""
		if e.ParentUUID != nil {
			parent = *e.ParentUUID
		}
		removedParent[e.UUID] = parent
	}

	if len(removedParent) == 0 {
		return 0
	}

	// Reparent: for remaining entries whose parent was removed,
	// walk up the removed chain until we find a non-removed ancestor.
	for _, e := range c.Entries {
		if e.UUID == "" || e.ParentUUID == nil || shouldRemove(e) {
			continue
		}
		newParent := resolveParent(*e.ParentUUID, removedParent)
		if newParent != *e.ParentUUID {
			if newParent == "" {
				e.ParentUUID = nil
			} else {
				e.SetParentUUID(newParent)
			}
		}
	}

	// Filter
	var result []*JSONLEntry
	removed := 0
	for _, e := range c.Entries {
		if shouldRemove(e) {
			removed++
		} else {
			result = append(result, e)
		}
	}
	c.Entries = result
	return removed
}

// resolveParent walks up the removed-parent chain to find the first
// ancestor that was NOT removed.
func resolveParent(parentUUID string, removedParent map[string]string) string {
	visited := make(map[string]bool)
	cur := parentUUID
	for {
		ancestor, wasRemoved := removedParent[cur]
		if !wasRemoved {
			return cur // cur is not removed, it's the valid parent
		}
		if visited[cur] {
			return "" // cycle in removed chain, return root
		}
		visited[cur] = true
		cur = ancestor
	}
}

// Filter keeps only entries for which keep() returns true.
// Delegates to RemoveEntries to handle reparenting.
func (c *Conversation) Filter(keep func(*JSONLEntry) bool) int {
	return c.RemoveEntries(func(e *JSONLEntry) bool { return !keep(e) })
}

// RemoveByUUIDs removes entries whose UUIDs are in the given set.
func (c *Conversation) RemoveByUUIDs(uuids map[string]bool) int {
	return c.RemoveEntries(func(e *JSONLEntry) bool { return uuids[e.UUID] })
}

// InsertAfter inserts newEntries immediately after the entry with afterUUID.
// New entries get afterUUID as their parent; the entry that previously
// followed afterUUID gets reparented to the last inserted entry.
func (c *Conversation) InsertAfter(afterUUID string, newEntries ...*JSONLEntry) {
	if len(newEntries) == 0 {
		return
	}

	// Set parentUuid for new entries: chain them
	newEntries[0].SetParentUUID(afterUUID)
	for i := 1; i < len(newEntries); i++ {
		if newEntries[i].UUID != "" && newEntries[i-1].UUID != "" {
			newEntries[i].SetParentUUID(newEntries[i-1].UUID)
		}
	}
	lastNewUUID := ""
	for i := len(newEntries) - 1; i >= 0; i-- {
		if newEntries[i].UUID != "" {
			lastNewUUID = newEntries[i].UUID
			break
		}
	}

	var result []*JSONLEntry
	for _, e := range c.Entries {
		result = append(result, e)
		if e.UUID == afterUUID {
			result = append(result, newEntries...)
		} else if e.ParentUUID != nil && *e.ParentUUID == afterUUID && lastNewUUID != "" {
			// Reparent the original child to the last new entry
			e.SetParentUUID(lastNewUUID)
		}
	}
	c.Entries = result
}

// ReplaceRange replaces entries matching uuidSet with newEntries.
// Handles reparenting via RemoveEntries + InsertAfter logic.
func (c *Conversation) ReplaceRange(uuidSet map[string]bool, newEntries []*JSONLEntry) {
	// Find the entry just before the first matched entry
	var beforeUUID string
	for _, e := range c.Entries {
		if uuidSet[e.UUID] {
			break
		}
		if e.UUID != "" {
			beforeUUID = e.UUID
		}
	}

	// Remove matched entries (reparents children)
	c.RemoveEntries(func(e *JSONLEntry) bool { return uuidSet[e.UUID] })

	// Insert new entries
	if beforeUUID != "" && len(newEntries) > 0 {
		c.InsertAfter(beforeUUID, newEntries...)
	} else if len(newEntries) > 0 {
		// Insert at beginning
		c.Entries = append(newEntries, c.Entries...)
	}
}

// TruncateAfter keeps entries up to and including the entry with uuid.
func (c *Conversation) TruncateAfter(uuid string) {
	var result []*JSONLEntry
	for _, e := range c.Entries {
		result = append(result, e)
		if e.UUID == uuid {
			break
		}
	}
	c.Entries = result
}

// FindByUUID returns the entry with the given UUID, or nil.
func (c *Conversation) FindByUUID(uuid string) *JSONLEntry {
	for _, e := range c.Entries {
		if e.UUID == uuid {
			return e
		}
	}
	return nil
}

// Clone returns a deep-ish copy (entries are shared, list is new).
func (c *Conversation) Clone() *Conversation {
	entries := make([]*JSONLEntry, len(c.Entries))
	copy(entries, c.Entries)
	return &Conversation{Entries: entries, SessionID: c.SessionID}
}

// --- Chain validation ---

// ChainIssue describes a single parentUuid chain problem.
type ChainIssue struct {
	Index      int    `json:"index"`
	UUID       string `json:"uuid"`
	Type       string `json:"type"` // entry type
	ParentUUID string `json:"parent_uuid"`
	Problem    string `json:"problem"` // "orphan" | "cycle"
}

// ValidateChain checks the parentUuid chain for issues.
func (c *Conversation) ValidateChain() []ChainIssue {
	present := make(map[string]bool)
	for _, e := range c.Entries {
		if e.UUID != "" {
			present[e.UUID] = true
		}
	}

	var issues []ChainIssue

	// Orphan detection
	for i, e := range c.Entries {
		if e.UUID == "" || e.ParentUUID == nil {
			continue
		}
		if !present[*e.ParentUUID] {
			issues = append(issues, ChainIssue{
				Index:      i,
				UUID:       e.UUID,
				Type:       e.Type,
				ParentUUID: *e.ParentUUID,
				Problem:    "orphan",
			})
		}
	}

	// Cycle detection
	byUUID := make(map[string]*JSONLEntry)
	for _, e := range c.Entries {
		if e.UUID != "" {
			byUUID[e.UUID] = e
		}
	}
	visited := make(map[string]bool)
	for _, e := range c.Entries {
		if e.UUID == "" {
			continue
		}
		clear(visited)
		cur := e
		for cur != nil && cur.ParentUUID != nil {
			if visited[cur.UUID] {
				issues = append(issues, ChainIssue{
					UUID:    e.UUID,
					Type:    e.Type,
					Problem: "cycle",
				})
				break
			}
			visited[cur.UUID] = true
			cur = byUUID[*cur.ParentUUID]
		}
	}

	return issues
}

// --- Chain operations ---

// rebuildLinearChain sets parentUuid on all entries as a linear chain
// based on list order. Since LoadConversation already extracted only
// the main chain, this is the correct parentUuid sequence.
func (c *Conversation) rebuildLinearChain() {
	var prevUUID string
	for _, e := range c.Entries {
		if e.UUID == "" {
			continue
		}
		if prevUUID == "" {
			e.ParentUUID = nil
		} else {
			e.SetParentUUID(prevUUID)
		}
		prevUUID = e.UUID
	}
}

// --- I/O ---

// WriteToFile rebuilds the linear parentUuid chain and writes to the given path.
func (c *Conversation) WriteToFile(path string) error {
	c.rebuildLinearChain()
	return WriteJSONLFile(path, c.Entries)
}

// SaveWithBackup creates a .bak backup and writes the conversation.
func (c *Conversation) SaveWithBackup(path string) (backup string, err error) {
	backup = path + ".bak"
	src, err := os.Open(path)
	if err == nil {
		dst, _ := os.Create(backup)
		io.Copy(dst, src)
		src.Close()
		dst.Close()
	}
	return backup, c.WriteToFile(path)
}

// --- Metrics ---

// Size returns the total serialized size in bytes.
func (c *Conversation) Size() int64 {
	var total int64
	for _, e := range c.Entries {
		b, _ := e.Marshal()
		total += int64(len(b)) + 1
	}
	return total
}

// EntryCount returns the number of entries.
func (c *Conversation) EntryCount() int {
	return len(c.Entries)
}

// MessageCount returns the number of user/assistant messages.
func (c *Conversation) MessageCount() int {
	n := 0
	for _, e := range c.Entries {
		if e.IsMessage() {
			n++
		}
	}
	return n
}

// --- Factory ---

// NewEntry creates a new entry belonging to this conversation.
func (c *Conversation) NewEntry(entryType, role, text string) *JSONLEntry {
	return NewEntry(generateUUID(), "", entryType, c.SessionID, NewMessageContent(role, text))
}

// NewEntryWithBlocks creates a new entry with raw content blocks.
func (c *Conversation) NewEntryWithBlocks(entryType string, msg *EntryMessage) *JSONLEntry {
	return NewEntry(generateUUID(), "", entryType, c.SessionID, msg)
}

// SessionPath returns the JSONL file path for a given project/session.
func SessionPath(projectID, sessionID string) string {
	return fmt.Sprintf("%s/%s/%s.jsonl", claudeDir(), projectID, sessionID)
}
