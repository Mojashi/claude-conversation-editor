package main

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strings"
)

// CompactRule is a single compaction rule that transforms a conversation.
// It receives the full entry list and returns a (possibly modified) entry list.
// It also returns a human-readable report of what it did.
type CompactRule interface {
	Name() string
	Description() string
	Apply(entries []*JSONLEntry) (result []*JSONLEntry, report CompactRuleReport)
}

// CompactRuleReport describes what a single rule did.
type CompactRuleReport struct {
	EntriesRemoved  int   `json:"entries_removed"`
	BytesBefore     int64 `json:"bytes_before"`
	BytesAfter      int64 `json:"bytes_after"`
	BytesSaved      int64 `json:"bytes_saved"`
	Details         []string `json:"details,omitempty"`
}

// CompactReport is the overall report from running all rules.
type CompactReport struct {
	Rules       []CompactRuleResult `json:"rules"`
	TotalBefore int64               `json:"total_before"`
	TotalAfter  int64               `json:"total_after"`
	TotalSaved  int64               `json:"total_saved"`
}

type CompactRuleResult struct {
	Name   string            `json:"name"`
	Report CompactRuleReport `json:"report"`
}

// AllRules returns all available compaction rules in recommended order.
func AllRules() []CompactRule {
	return []CompactRule{
		&TruncateOldReadsRule{},
		&TruncateOldWritesRule{},
		&ShortenSuccessResultsRule{},
		&FixNullContentRule{},
	}
}

// RunCompaction applies all given rules to a Conversation.
// The conversation should already contain only the main chain
// (LoadConversation extracts it at load time).
func RunCompaction(conv *Conversation, rules []CompactRule) CompactReport {
	report := CompactReport{
		TotalBefore: conv.Size(),
	}

	for _, rule := range rules {
		result, ruleReport := rule.Apply(conv.Entries)
		report.Rules = append(report.Rules, CompactRuleResult{
			Name:   rule.Name(),
			Report: ruleReport,
		})
		conv.Entries = result
	}

	report.TotalAfter = conv.Size()
	report.TotalSaved = report.TotalBefore - report.TotalAfter
	return report
}

// removeAndReparent removes entries matching shouldRemove and reparents
// their children in the parentUuid tree. This is the correct way to remove
// entries from a JSONL conversation — it preserves Claude Code's Pa_ chain.
func removeAndReparent(entries []*JSONLEntry, shouldRemove func(*JSONLEntry) bool) []*JSONLEntry {
	// Build removed parent map
	removedParent := make(map[string]string)
	for _, e := range entries {
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
		return entries
	}

	// Reparent surviving entries whose parent was removed
	for _, e := range entries {
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
	for _, e := range entries {
		if !shouldRemove(e) {
			result = append(result, e)
		}
	}
	return result
}

func entriesSize(entries []*JSONLEntry) int64 {
	var total int64
	for _, e := range entries {
		b, _ := e.Marshal()
		total += int64(len(b)) + 1 // +1 for newline
	}
	return total
}

// --- Rule: StripProgressRule ---
// Removes all non-message entries like bash_progress, agent_progress, hook_progress, etc.

type StripProgressRule struct{}

func (r *StripProgressRule) Name() string        { return "strip-progress" }
func (r *StripProgressRule) Description() string {
	return "Remove streaming progress entries (bash_progress, agent_progress, etc.)"
}

func (r *StripProgressRule) Apply(entries []*JSONLEntry) ([]*JSONLEntry, CompactRuleReport) {
	report := CompactRuleReport{BytesBefore: entriesSize(entries)}

	stripTypes := map[string]bool{
		"progress":        true,
		"queue-operation": true,
		"last-prompt":     true,
	}

	var result []*JSONLEntry
	for _, e := range entries {
		if !stripTypes[e.Type] {
			result = append(result, e)
		}
	}
	removed := len(entries) - len(result)

	report.EntriesRemoved = removed
	report.BytesAfter = entriesSize(result)
	report.BytesSaved = report.BytesBefore - report.BytesAfter
	if removed > 0 {
		report.Details = append(report.Details, fmt.Sprintf("removed %d progress entries", removed))
	}
	return result, report
}

// --- Rule: StripToolUseResultRule ---
// Removes the top-level `toolUseResult` field from user entries (duplicated data).

type StripToolUseResultRule struct{}

func (r *StripToolUseResultRule) Name() string        { return "strip-tool-use-result" }
func (r *StripToolUseResultRule) Description() string {
	return "Remove duplicated toolUseResult top-level field from user entries"
}

func (r *StripToolUseResultRule) Apply(entries []*JSONLEntry) ([]*JSONLEntry, CompactRuleReport) {
	report := CompactRuleReport{BytesBefore: entriesSize(entries)}
	stripped := 0

	for _, e := range entries {
		if _, ok := e.raw["toolUseResult"]; ok {
			delete(e.raw, "toolUseResult")
			stripped++
		}
	}

	report.BytesAfter = entriesSize(entries)
	report.BytesSaved = report.BytesBefore - report.BytesAfter
	if stripped > 0 {
		report.Details = append(report.Details, fmt.Sprintf("stripped toolUseResult from %d entries", stripped))
	}
	return entries, report
}

// --- Rule: StripCwdRule ---
// Removes the top-level `cwd` field from entries.
// Claude Code uses cwd to group messages, which can break tool_use/tool_result
// pairing on resume when cwd differs between entries.

type StripCwdRule struct{}

func (r *StripCwdRule) Name() string        { return "strip-cwd" }
func (r *StripCwdRule) Description() string { return "Remove cwd field that breaks resume" }

func (r *StripCwdRule) Apply(entries []*JSONLEntry) ([]*JSONLEntry, CompactRuleReport) {
	report := CompactRuleReport{BytesBefore: entriesSize(entries)}
	stripped := 0
	for _, e := range entries {
		if _, ok := e.raw["cwd"]; ok {
			delete(e.raw, "cwd")
			stripped++
		}
	}
	report.BytesAfter = entriesSize(entries)
	report.BytesSaved = report.BytesBefore - report.BytesAfter
	if stripped > 0 {
		report.Details = append(report.Details, fmt.Sprintf("stripped cwd from %d entries", stripped))
	}
	return entries, report
}

// --- Rule: TruncateOldReadsRule ---
// Tracks known line ranges per file and truncates Read results whose
// lines are already in context.
//
// Known lines are updated by:
//   - Read: lines returned become known (respecting offset/limit)
//   - Write: ALL lines become known (full file replacement)
//   - Edit: old_string lines removed from known, new_string lines added
//
// A Read is truncated only if ALL its lines are already known.

type TruncateOldReadsRule struct{}

func (r *TruncateOldReadsRule) Name() string { return "truncate-old-reads" }
func (r *TruncateOldReadsRule) Description() string {
	return "Truncate Read results whose lines are already in context"
}

func (r *TruncateOldReadsRule) Apply(entries []*JSONLEntry) ([]*JSONLEntry, CompactRuleReport) {
	report := CompactRuleReport{BytesBefore: entriesSize(entries)}
	truncated := 0

	// Collect tool_use info
	type toolUseInfo struct {
		name     string
		filePath string
		offset   int
		limit    int
		input    json.RawMessage // for Write/Edit content extraction
	}
	tuMap := map[string]toolUseInfo{}

	for _, e := range entries {
		if e.Message == nil || e.Type != "assistant" {
			continue
		}
		var blocks []json.RawMessage
		if json.Unmarshal(e.Message.Content, &blocks) != nil {
			continue
		}
		for _, block := range blocks {
			var b map[string]json.RawMessage
			if json.Unmarshal(block, &b) != nil {
				continue
			}
			var typ, id, name string
			json.Unmarshal(b["type"], &typ)
			if typ != "tool_use" {
				continue
			}
			json.Unmarshal(b["id"], &id)
			json.Unmarshal(b["name"], &name)
			var input struct {
				FilePath  string `json:"file_path"`
				Offset    int    `json:"offset"`
				Limit     int    `json:"limit"`
				Content   string `json:"content"`
				OldString string `json:"old_string"`
				NewString string `json:"new_string"`
			}
			json.Unmarshal(b["input"], &input)
			tuMap[id] = toolUseInfo{
				name:     name,
				filePath: input.FilePath,
				offset:   input.Offset,
				limit:    input.Limit,
				input:    b["input"],
			}
		}
	}

	// Walk forward, tracking known lines per file (line number → content hash)
	knownLines := map[string]map[int]string{} // file → line number → line content

	for _, e := range entries {
		if e.Message == nil {
			continue
		}
		var blocks []json.RawMessage
		if json.Unmarshal(e.Message.Content, &blocks) != nil {
			continue
		}

		// Process assistant tool_use: Write/Edit update known lines
		if e.Type == "assistant" {
			for _, block := range blocks {
				var b map[string]json.RawMessage
				if json.Unmarshal(block, &b) != nil {
					continue
				}
				var typ, id string
				json.Unmarshal(b["type"], &typ)
				if typ != "tool_use" {
					continue
				}
				json.Unmarshal(b["id"], &id)
				info := tuMap[id]

				switch info.name {
				case "Write":
					// Write replaces entire file — all written lines are known
					var inp struct {
						Content string `json:"content"`
					}
					json.Unmarshal(info.input, &inp)
					if inp.Content != "" && info.filePath != "" {
						lines := strings.Split(inp.Content, "\n")
						known := make(map[int]string, len(lines))
						for j, line := range lines {
							known[j+1] = line
						}
						knownLines[info.filePath] = known
					}

				case "Edit":
					// Edit replaces old_string with new_string.
					// Remove known lines matching old_string content,
					// add new_string lines as known.
					var inp struct {
						OldString string `json:"old_string"`
						NewString string `json:"new_string"`
					}
					json.Unmarshal(info.input, &inp)
					if info.filePath != "" {
						known := knownLines[info.filePath]
						if known != nil {
							// Remove lines matching old_string
							oldLines := strings.Split(inp.OldString, "\n")
							for lineNum, lineContent := range known {
								for _, oldLine := range oldLines {
									if strings.TrimSpace(lineContent) == strings.TrimSpace(oldLine) {
										delete(known, lineNum)
										break
									}
								}
							}
						}
					}
				}
			}
			continue
		}

		if e.Type != "user" {
			continue
		}

		// Process tool_results
		modified := false
		for i, block := range blocks {
			var b map[string]json.RawMessage
			if json.Unmarshal(block, &b) != nil {
				continue
			}
			var typ, toolUseID string
			json.Unmarshal(b["type"], &typ)
			if typ != "tool_result" {
				continue
			}
			json.Unmarshal(b["tool_use_id"], &toolUseID)
			info := tuMap[toolUseID]
			if info.name != "Read" || info.filePath == "" {
				continue
			}

			var isErr bool
			if errField, ok := b["is_error"]; ok {
				json.Unmarshal(errField, &isErr)
			}
			if isErr {
				continue
			}

			var content string
			if json.Unmarshal(b["content"], &content) != nil {
				continue
			}
			lines := strings.Split(content, "\n")
			if len(lines) == 0 {
				continue
			}

			startLine := 1
			if info.offset > 0 {
				startLine = info.offset
			}

			known := knownLines[info.filePath]
			if known == nil {
				known = make(map[int]string)
				knownLines[info.filePath] = known
			}

			alreadyKnown := 0
			for j, line := range lines {
				if prev, ok := known[startLine+j]; ok && prev == line {
					alreadyKnown++
				}
			}

			if alreadyKnown == len(lines) {
				// All lines already known → full truncate
				shortName := info.filePath
				if idx := strings.LastIndex(info.filePath, "/"); idx >= 0 {
					shortName = info.filePath[idx+1:]
				}
				placeholder := fmt.Sprintf("[Read %s — %d lines already in context]",
					shortName, len(lines))
				b["content"], _ = json.Marshal(placeholder)
				blocks[i], _ = json.Marshal(b)
				modified = true
				truncated++
			} else if alreadyKnown > 0 && len(content) > 200 {
				// Partial overlap — keep only new lines, collapse known ranges
				var sb strings.Builder
				knownRunStart := -1
				knownRunCount := 0

				flushKnownRun := func() {
					if knownRunCount > 0 {
						if knownRunCount == 1 {
							sb.WriteString(fmt.Sprintf("[line %d — already in context]\n", knownRunStart))
						} else {
							sb.WriteString(fmt.Sprintf("[lines %d-%d — %d lines already in context]\n",
								knownRunStart, knownRunStart+knownRunCount-1, knownRunCount))
						}
						knownRunStart = -1
						knownRunCount = 0
					}
				}

				for j, line := range lines {
					lineNum := startLine + j
					if prev, ok := known[lineNum]; ok && prev == line {
						if knownRunCount == 0 {
							knownRunStart = lineNum
						}
						knownRunCount++
					} else {
						flushKnownRun()
						sb.WriteString(line)
						if j < len(lines)-1 {
							sb.WriteByte('\n')
						}
					}
				}
				flushKnownRun()

				result := sb.String()
				if len(result) < len(content) {
					b["content"], _ = json.Marshal(result)
					blocks[i], _ = json.Marshal(b)
					modified = true
					truncated++
				}

				// Mark all lines as known
				for j, line := range lines {
					known[startLine+j] = line
				}
			} else {
				// Mark lines as known
				for j, line := range lines {
					known[startLine+j] = line
				}
			}
		}

		if modified {
			e.Message.Content, _ = json.Marshal(blocks)
		}
	}

	report.BytesAfter = entriesSize(entries)
	report.BytesSaved = report.BytesBefore - report.BytesAfter
	if truncated > 0 {
		report.Details = append(report.Details, fmt.Sprintf("truncated %d redundant Read results", truncated))
	}
	return entries, report
}

// --- Rule: FillMissingToolResultsRule ---
// Adds empty tool_result blocks for tool_use calls that have no corresponding result.

type FillMissingToolResultsRule struct{}

func (r *FillMissingToolResultsRule) Name() string { return "fill-missing-tool-results" }
func (r *FillMissingToolResultsRule) Description() string {
	return "Add placeholder tool_results for orphaned tool_use calls"
}

func (r *FillMissingToolResultsRule) Apply(entries []*JSONLEntry) ([]*JSONLEntry, CompactRuleReport) {
	report := CompactRuleReport{BytesBefore: entriesSize(entries)}
	filled := 0

	// Collect all tool_use IDs
	allToolUses := map[string]int{} // id -> entry index
	for i, e := range entries {
		if e.Type != "assistant" || e.Message == nil {
			continue
		}
		var blocks []json.RawMessage
		if json.Unmarshal(e.Message.Content, &blocks) != nil {
			continue
		}
		for _, block := range blocks {
			var b map[string]json.RawMessage
			if json.Unmarshal(block, &b) != nil {
				continue
			}
			var typ, id string
			json.Unmarshal(b["type"], &typ)
			if typ == "tool_use" {
				json.Unmarshal(b["id"], &id)
				allToolUses[id] = i
			}
		}
	}

	// Collect all tool_result IDs
	allToolResults := map[string]bool{}
	for _, e := range entries {
		if e.Type != "user" || e.Message == nil {
			continue
		}
		var blocks []json.RawMessage
		if json.Unmarshal(e.Message.Content, &blocks) != nil {
			continue
		}
		for _, block := range blocks {
			var b map[string]json.RawMessage
			if json.Unmarshal(block, &b) != nil {
				continue
			}
			var typ, tuID string
			json.Unmarshal(b["type"], &typ)
			if typ == "tool_result" {
				json.Unmarshal(b["tool_use_id"], &tuID)
				allToolResults[tuID] = true
			}
		}
	}

	// Find orphans
	orphans := map[int][]string{} // assistant entry index -> missing tool_use IDs
	for id, idx := range allToolUses {
		if !allToolResults[id] {
			orphans[idx] = append(orphans[idx], id)
		}
	}

	if len(orphans) == 0 {
		report.BytesAfter = report.BytesBefore
		return entries, report
	}

	// For each orphan, inject a placeholder tool_result into the next user message.
	// If no next user exists, create one and append to the entry list.
	for aIdx, missingIDs := range orphans {
		// Find next user entry
		var nextUser *JSONLEntry
		for j := aIdx + 1; j < len(entries); j++ {
			if entries[j].Type == "user" && entries[j].IsMessage() && entries[j].Message != nil {
				nextUser = entries[j]
				break
			}
		}

		var blocks []json.RawMessage
		if nextUser != nil {
			json.Unmarshal(nextUser.Message.Content, &blocks)
		}

		for _, id := range missingIDs {
			placeholder, _ := json.Marshal(map[string]interface{}{
				"type":        "tool_result",
				"tool_use_id": id,
				"content":     "[result lost during session]",
			})
			blocks = append(blocks, placeholder)
			filled++
		}

		if nextUser != nil {
			nextUser.Message.Content, _ = json.Marshal(blocks)
		} else {
			// No next user: remove the orphan tool_use blocks from the assistant
			// instead of creating a placeholder (avoids trailing consecutive user)
			aEntry := entries[aIdx]
			var aBlocks []json.RawMessage
			json.Unmarshal(aEntry.Message.Content, &aBlocks)
			var filtered []json.RawMessage
			missingSet := make(map[string]bool)
			for _, id := range missingIDs {
				missingSet[id] = true
			}
			for _, block := range aBlocks {
				var b map[string]json.RawMessage
				if json.Unmarshal(block, &b) == nil {
					var typ, id string
					json.Unmarshal(b["type"], &typ)
					if typ == "tool_use" {
						json.Unmarshal(b["id"], &id)
						if missingSet[id] {
							filled++
							continue
						}
					}
				}
				filtered = append(filtered, block)
			}
			aEntry.Message.Content, _ = json.Marshal(filtered)
		}
	}

	report.BytesAfter = entriesSize(entries)
	report.BytesSaved = report.BytesBefore - report.BytesAfter
	if filled > 0 {
		report.Details = append(report.Details, fmt.Sprintf("filled %d missing tool_results", filled))
	}
	return entries, report
}

// --- Rule: MergeConsecutiveRule ---
// Merges consecutive messages with the same role to ensure strict user/assistant alternation.
// This fixes conversations broken by removing interleaved non-message entries.

type MergeConsecutiveRule struct{}

func (r *MergeConsecutiveRule) Name() string { return "merge-consecutive" }
func (r *MergeConsecutiveRule) Description() string {
	return "Merge consecutive same-role messages to restore user/assistant alternation"
}

func (r *MergeConsecutiveRule) Apply(entries []*JSONLEntry) ([]*JSONLEntry, CompactRuleReport) {
	report := CompactRuleReport{BytesBefore: entriesSize(entries)}

	// Pass 1: identify which entries will be merged into their predecessor.
	// mergeTarget[i] = j means entries[i] should be merged into entries[j].
	mergeTarget := make(map[int]int) // index of merged entry → index of target
	var lastMsgIdx int = -1
	var lastMsgRole string
	for i, e := range entries {
		if !e.IsMessage() || e.Message == nil {
			continue
		}
		role := e.Message.Role
		if role == "" {
			role = e.Type
		}
		if lastMsgIdx >= 0 && role == lastMsgRole && !hasToolBlocks(e) && !hasToolBlocks(entries[lastMsgIdx]) {
			mergeTarget[i] = lastMsgIdx
			// lastMsgIdx stays the same (we keep merging into it)
		} else {
			lastMsgIdx = i
			lastMsgRole = role
		}
	}

	if len(mergeTarget) == 0 {
		report.BytesAfter = report.BytesBefore
		return entries, report
	}

	// Pass 2: merge content into targets
	for srcIdx, dstIdx := range mergeTarget {
		mergeMessageContent(entries[dstIdx], entries[srcIdx])
	}

	// Pass 3: remove merged entries with proper reparenting
	toRemove := make(map[*JSONLEntry]bool)
	for srcIdx := range mergeTarget {
		toRemove[entries[srcIdx]] = true
	}
	result := removeAndReparent(entries, func(e *JSONLEntry) bool {
		return toRemove[e]
	})

	report.EntriesRemoved = len(mergeTarget)
	report.BytesAfter = entriesSize(result)
	report.BytesSaved = report.BytesBefore - report.BytesAfter
	if len(mergeTarget) > 0 {
		report.Details = append(report.Details, fmt.Sprintf("merged %d consecutive same-role messages", len(mergeTarget)))
	}
	return result, report
}

// --- Rule: FixConsecutiveToolRule ---
// When consecutive same-role messages remain (because they have tool blocks
// and MergeConsecutiveRule skipped them), this rule inserts dummy counterpart
// messages to restore strict user/assistant alternation.
//
// For consecutive assistants with tool_use:
//   asst(tu:A) → asst(tu:B)
// becomes:
//   asst(tu:A) → user(tr:A placeholder) → asst(tu:B)
//
// For consecutive users with tool_result:
//   user(tr:A) → user(tr:B)
// becomes:
//   user(tr:A) → asst(text:"continue") → user(tr:B)

type FixConsecutiveToolRule struct{}

func (r *FixConsecutiveToolRule) Name() string { return "fix-consecutive-tool" }
func (r *FixConsecutiveToolRule) Description() string {
	return "Insert dummy messages to fix consecutive tool messages"
}

func (r *FixConsecutiveToolRule) Apply(entries []*JSONLEntry) ([]*JSONLEntry, CompactRuleReport) {
	report := CompactRuleReport{BytesBefore: entriesSize(entries)}
	inserted := 0

	var result []*JSONLEntry
	var lastMsgRole string
	for _, e := range entries {
		if !e.IsMessage() || e.Message == nil {
			result = append(result, e)
			continue
		}

		role := e.Message.Role
		if role == "" {
			role = e.Type
		}

		if role == lastMsgRole {
			// Find the last message in result to check for tool_use
			var prevMsg *JSONLEntry
			for j := len(result) - 1; j >= 0; j-- {
				if result[j].IsMessage() {
					prevMsg = result[j]
					break
				}
			}

			if role == "assistant" {
				// Insert dummy user — if prev has tool_use, include tool_results
				var trBlocks []json.RawMessage
				if prevMsg != nil && prevMsg.Message != nil {
					var blocks []json.RawMessage
					json.Unmarshal(prevMsg.Message.Content, &blocks)
					for _, block := range blocks {
						var b map[string]json.RawMessage
						if json.Unmarshal(block, &b) != nil {
							continue
						}
						var typ, id string
						json.Unmarshal(b["type"], &typ)
						if typ == "tool_use" {
							json.Unmarshal(b["id"], &id)
							tr, _ := json.Marshal(map[string]interface{}{
								"type":        "tool_result",
								"tool_use_id": id,
								"content":     "[continued]",
							})
							trBlocks = append(trBlocks, tr)
						}
					}
				}
				if len(trBlocks) == 0 {
					// No tool_use in prev — just add a text placeholder
					tb, _ := json.Marshal(map[string]string{"type": "text", "text": "[continued]"})
					trBlocks = []json.RawMessage{tb}
				}
				content, _ := json.Marshal(trBlocks)
				dummy := &JSONLEntry{
					UUID:    generateUUID(),
					Type:    "user",
					Message: &EntryMessage{Role: "user", Content: content},
					raw:     make(map[string]json.RawMessage),
				}
				result = append(result, dummy)
				inserted++
			} else {
				// Insert dummy assistant
				content, _ := json.Marshal([]map[string]string{
					{"type": "text", "text": "[continued]"},
				})
				dummy := &JSONLEntry{
					UUID:    generateUUID(),
					Type:    "assistant",
					Message: &EntryMessage{Role: "assistant", Content: content},
					raw:     make(map[string]json.RawMessage),
				}
				result = append(result, dummy)
				inserted++
			}
		}

		result = append(result, e)
		lastMsgRole = role
	}

	report.BytesAfter = entriesSize(result)
	report.BytesSaved = report.BytesBefore - report.BytesAfter
	if inserted > 0 {
		report.Details = append(report.Details, fmt.Sprintf("inserted %d dummy messages to fix alternation", inserted))
	}
	return result, report
}

// hasToolBlocks returns true if the entry's content contains tool_use or tool_result blocks.
func hasToolBlocks(e *JSONLEntry) bool {
	if e.Message == nil {
		return false
	}
	var blocks []json.RawMessage
	if json.Unmarshal(e.Message.Content, &blocks) != nil {
		return false
	}
	for _, block := range blocks {
		var b map[string]json.RawMessage
		if json.Unmarshal(block, &b) != nil {
			continue
		}
		var typ string
		json.Unmarshal(b["type"], &typ)
		if typ == "tool_use" || typ == "tool_result" {
			return true
		}
	}
	return false
}

// mergeMessageContent appends src's content blocks into dst.
func mergeMessageContent(dst, src *JSONLEntry) {
	if dst.Message == nil || src.Message == nil {
		return
	}

	// Try to parse both as arrays
	var dstBlocks, srcBlocks []json.RawMessage
	dstIsArray := json.Unmarshal(dst.Message.Content, &dstBlocks) == nil
	srcIsArray := json.Unmarshal(src.Message.Content, &srcBlocks) == nil

	if dstIsArray && srcIsArray {
		combined := append(dstBlocks, srcBlocks...)
		dst.Message.Content, _ = json.Marshal(combined)
		return
	}

	// Fallback: wrap strings as text blocks and merge
	wrap := func(content json.RawMessage) []json.RawMessage {
		var blocks []json.RawMessage
		if json.Unmarshal(content, &blocks) == nil {
			return blocks
		}
		var s string
		if json.Unmarshal(content, &s) == nil && s != "" {
			b, _ := json.Marshal(map[string]string{"type": "text", "text": s})
			return []json.RawMessage{b}
		}
		return nil
	}

	dstWrapped := wrap(dst.Message.Content)
	srcWrapped := wrap(src.Message.Content)
	combined := append(dstWrapped, srcWrapped...)
	if combined == nil {
		combined = []json.RawMessage{}
	}
	dst.Message.Content, _ = json.Marshal(combined)
}

// --- Rule: TruncateOldWritesRule ---
// For each file with multiple Writes, keeps only the LAST Write's content.
// Earlier Writes are replaced with a placeholder (since Write is a full
// file overwrite, earlier versions are completely superseded).
// Edit tool_use inputs are NEVER truncated (they contain diffs that
// provide context about what changed).

type TruncateOldWritesRule struct{}

func (r *TruncateOldWritesRule) Name() string { return "truncate-old-writes" }
func (r *TruncateOldWritesRule) Description() string {
	return "Truncate non-last Write inputs per file (Write only, not Edit)"
}

func (r *TruncateOldWritesRule) Apply(entries []*JSONLEntry) ([]*JSONLEntry, CompactRuleReport) {
	report := CompactRuleReport{BytesBefore: entriesSize(entries)}
	truncated := 0

	// Pass 1: collect Write tool_use → file_path (ignore Edit)
	type writeInfo struct {
		filePath string
		entryIdx int
		blockIdx int
	}
	var allWrites []writeInfo

	for ei, e := range entries {
		if e.Message == nil || e.Type != "assistant" {
			continue
		}
		var blocks []json.RawMessage
		if json.Unmarshal(e.Message.Content, &blocks) != nil {
			continue
		}
		for bi, block := range blocks {
			var b map[string]json.RawMessage
			if json.Unmarshal(block, &b) != nil {
				continue
			}
			var typ, name string
			json.Unmarshal(b["type"], &typ)
			if typ != "tool_use" {
				continue
			}
			json.Unmarshal(b["name"], &name)
			if name != "Write" {
				continue
			}
			var input struct {
				FilePath string `json:"file_path"`
			}
			json.Unmarshal(b["input"], &input)
			if input.FilePath == "" {
				continue
			}
			allWrites = append(allWrites, writeInfo{
				filePath: input.FilePath,
				entryIdx: ei,
				blockIdx: bi,
			})
		}
	}

	// Pass 2: find last Write per file
	lastPerFile := map[string]int{} // file_path → index in allWrites
	for i, w := range allWrites {
		lastPerFile[w.filePath] = i
	}

	// Pass 3: truncate non-last Write/Edit inputs
	for i, w := range allWrites {
		if lastPerFile[w.filePath] == i {
			continue // keep the last one
		}

		e := entries[w.entryIdx]
		var blocks []json.RawMessage
		if json.Unmarshal(e.Message.Content, &blocks) != nil {
			continue
		}

		var b map[string]json.RawMessage
		if json.Unmarshal(blocks[w.blockIdx], &b) != nil {
			continue
		}

		// Build placeholder input
		shortName := w.filePath
		if idx := strings.LastIndex(w.filePath, "/"); idx >= 0 {
			shortName = w.filePath[idx+1:]
		}
		origSize := len(b["input"])

		placeholder := map[string]string{
			"file_path": w.filePath,
			"content":   fmt.Sprintf("[wrote %s — %s, see later Write]", shortName, humanBytes(int64(origSize))),
		}

		b["input"], _ = json.Marshal(placeholder)
		blocks[w.blockIdx], _ = json.Marshal(b)
		e.Message.Content, _ = json.Marshal(blocks)
		truncated++
	}

	report.BytesAfter = entriesSize(entries)
	report.BytesSaved = report.BytesBefore - report.BytesAfter
	if truncated > 0 {
		report.Details = append(report.Details, fmt.Sprintf("truncated %d old Write/Edit inputs", truncated))
	}
	return entries, report
}

// --- Rule: ShortenSuccessResultsRule ---
// Shortens verbose success tool_result messages to just "success".
// e.g. "The file /long/path/to/file.ts has been updated successfully." → "success"

type ShortenSuccessResultsRule struct{}

func (r *ShortenSuccessResultsRule) Name() string { return "shorten-success-results" }
func (r *ShortenSuccessResultsRule) Description() string {
	return "Shorten verbose success tool_result messages"
}

func (r *ShortenSuccessResultsRule) Apply(entries []*JSONLEntry) ([]*JSONLEntry, CompactRuleReport) {
	report := CompactRuleReport{BytesBefore: entriesSize(entries)}
	shortened := 0

	for _, e := range entries {
		if e.Message == nil || e.Type != "user" {
			continue
		}
		var blocks []json.RawMessage
		if json.Unmarshal(e.Message.Content, &blocks) != nil {
			continue
		}

		modified := false
		for i, block := range blocks {
			var b map[string]json.RawMessage
			if json.Unmarshal(block, &b) != nil {
				continue
			}
			var typ string
			json.Unmarshal(b["type"], &typ)
			if typ != "tool_result" {
				continue
			}

			// Check if error
			var isErr bool
			if errField, ok := b["is_error"]; ok {
				json.Unmarshal(errField, &isErr)
			}
			if isErr {
				continue
			}

			// Get content as string
			var content string
			if json.Unmarshal(b["content"], &content) != nil {
				continue
			}

			// Match success patterns
			if strings.Contains(content, "successfully") ||
				strings.Contains(content, "has been updated") ||
				strings.Contains(content, "File created") {
				if len(content) > 20 {
					b["content"], _ = json.Marshal("success")
					blocks[i], _ = json.Marshal(b)
					modified = true
					shortened++
				}
			}
		}

		if modified {
			e.Message.Content, _ = json.Marshal(blocks)
		}
	}

	report.BytesAfter = entriesSize(entries)
	report.BytesSaved = report.BytesBefore - report.BytesAfter
	if shortened > 0 {
		report.Details = append(report.Details, fmt.Sprintf("shortened %d success messages", shortened))
	}
	return entries, report
}

// --- Rule: ShortenPathsRule ---
// Replaces the project root prefix in tool_use inputs and tool_result contents
// with "./" to save tokens. Only modifies tool blocks, not assistant text.

type ShortenPathsRule struct{}

func (r *ShortenPathsRule) Name() string { return "shorten-paths" }
func (r *ShortenPathsRule) Description() string {
	return "Shorten project root paths to ./ in tool blocks"
}

func (r *ShortenPathsRule) Apply(entries []*JSONLEntry) ([]*JSONLEntry, CompactRuleReport) {
	report := CompactRuleReport{BytesBefore: entriesSize(entries)}

	// Detect project root: find the most common path prefix in Read/Write/Edit inputs
	pathCounts := map[string]int{}
	for _, e := range entries {
		if e.Message == nil || e.Type != "assistant" {
			continue
		}
		var blocks []json.RawMessage
		if json.Unmarshal(e.Message.Content, &blocks) != nil {
			continue
		}
		for _, block := range blocks {
			var b map[string]json.RawMessage
			if json.Unmarshal(block, &b) != nil {
				continue
			}
			var typ string
			json.Unmarshal(b["type"], &typ)
			if typ == "tool_use" {
				var input struct {
					FilePath string `json:"file_path"`
				}
				json.Unmarshal(b["input"], &input)
				if input.FilePath != "" {
					// Extract directory
					dir := input.FilePath
					for i := len(dir) - 1; i >= 0; i-- {
						if dir[i] == '/' {
							dir = dir[:i+1]
							break
						}
					}
					pathCounts[dir]++
				}
			}
		}
	}

	// Find the longest common prefix that appears in >50% of paths
	var projectRoot string
	maxCount := 0
	for dir, count := range pathCounts {
		if count > maxCount || (count == maxCount && len(dir) > len(projectRoot)) {
			projectRoot = dir
			maxCount = count
		}
	}

	if projectRoot == "" || len(projectRoot) < 10 {
		report.BytesAfter = report.BytesBefore
		return entries, report
	}

	// Find the actual common prefix among all paths that start with projectRoot
	// Walk up until we find a prefix used by most paths
	for {
		matchCount := 0
		for dir, count := range pathCounts {
			if strings.HasPrefix(dir, projectRoot) {
				matchCount += count
			}
		}
		if matchCount > maxCount/2 {
			break
		}
		// Go up one level
		idx := strings.LastIndex(projectRoot[:len(projectRoot)-1], "/")
		if idx < 0 {
			break
		}
		projectRoot = projectRoot[:idx+1]
	}

	shortened := 0

	// Replace in tool_use inputs and tool_result contents
	for _, e := range entries {
		if e.Message == nil {
			continue
		}
		var blocks []json.RawMessage
		if json.Unmarshal(e.Message.Content, &blocks) != nil {
			continue
		}

		modified := false
		for i, block := range blocks {
			raw := string(block)
			if !strings.Contains(raw, projectRoot) {
				continue
			}

			var b map[string]json.RawMessage
			if json.Unmarshal(block, &b) != nil {
				continue
			}
			var typ string
			json.Unmarshal(b["type"], &typ)

			if typ == "tool_use" {
				inputRaw := string(b["input"])
				newInput := strings.ReplaceAll(inputRaw, projectRoot, "./")
				if newInput != inputRaw {
					b["input"] = json.RawMessage(newInput)
					blocks[i], _ = json.Marshal(b)
					modified = true
					shortened++
				}
			} else if typ == "tool_result" {
				contentRaw := string(b["content"])
				newContent := strings.ReplaceAll(contentRaw, projectRoot, "./")
				if newContent != contentRaw {
					b["content"] = json.RawMessage(newContent)
					blocks[i], _ = json.Marshal(b)
					modified = true
					shortened++
				}
			}
		}

		if modified {
			e.Message.Content, _ = json.Marshal(blocks)
		}
	}

	report.BytesAfter = entriesSize(entries)
	report.BytesSaved = report.BytesBefore - report.BytesAfter
	if shortened > 0 {
		report.Details = append(report.Details,
			fmt.Sprintf("shortened %d path references (root: %s)", shortened, projectRoot))
	}
	return entries, report
}

// --- Rule: FixNullContentRule ---
// Replaces null message content with an empty text block.

type FixNullContentRule struct{}

func (r *FixNullContentRule) Name() string        { return "fix-null-content" }
func (r *FixNullContentRule) Description() string { return "Replace null message content with empty text" }

func (r *FixNullContentRule) Apply(entries []*JSONLEntry) ([]*JSONLEntry, CompactRuleReport) {
	report := CompactRuleReport{BytesBefore: entriesSize(entries)}
	fixed := 0

	for _, e := range entries {
		if !e.IsMessage() || e.Message == nil {
			continue
		}
		if e.Message.Content == nil || len(e.Message.Content) == 0 || string(e.Message.Content) == "null" {
			e.Message.Content, _ = json.Marshal([]map[string]string{
				{"type": "text", "text": "[empty]"},
			})
			fixed++
		}
	}

	report.BytesAfter = entriesSize(entries)
	report.BytesSaved = report.BytesBefore - report.BytesAfter
	if fixed > 0 {
		report.Details = append(report.Details, fmt.Sprintf("fixed %d null content messages", fixed))
	}
	return entries, report
}

// --- Rule: RepairToolPairsRule ---
// When parallel tool calls scatter tool_results across multiple user messages,
// consolidate them so each assistant's tool_use IDs are answered by the immediately
// following user message.

type RepairToolPairsRule struct{}

func (r *RepairToolPairsRule) Name() string { return "repair-tool-pairs" }
func (r *RepairToolPairsRule) Description() string {
	return "Consolidate scattered tool_results to match their tool_use messages"
}

func (r *RepairToolPairsRule) Apply(entries []*JSONLEntry) ([]*JSONLEntry, CompactRuleReport) {
	report := CompactRuleReport{BytesBefore: entriesSize(entries)}
	repaired := 0

	// Collect all tool_use IDs per assistant entry index
	type tuInfo struct {
		entryIdx int
		ids      []string
	}
	var assistantTUs []tuInfo
	for i, e := range entries {
		if e.Type != "assistant" || e.Message == nil {
			continue
		}
		var blocks []json.RawMessage
		if json.Unmarshal(e.Message.Content, &blocks) != nil {
			continue
		}
		var ids []string
		for _, block := range blocks {
			var b map[string]json.RawMessage
			if json.Unmarshal(block, &b) != nil {
				continue
			}
			var typ, id string
			json.Unmarshal(b["type"], &typ)
			if typ == "tool_use" {
				json.Unmarshal(b["id"], &id)
				ids = append(ids, id)
			}
		}
		if len(ids) > 0 {
			assistantTUs = append(assistantTUs, tuInfo{entryIdx: i, ids: ids})
		}
	}

	// Build map: tool_use_id -> which assistant entry it belongs to
	tuOwner := map[string]int{} // tool_use_id -> assistant entry index
	for _, tu := range assistantTUs {
		for _, id := range tu.ids {
			tuOwner[id] = tu.entryIdx
		}
	}

	// Collect all tool_result blocks from user entries, grouped by their owner assistant
	type trBlock struct {
		block      json.RawMessage
		toolUseID  string
		sourceIdx  int // user entry index
	}
	collected := map[int][]trBlock{} // assistant entry index -> tool_result blocks
	// Track which user entries had blocks reassigned
	userModified := map[int]bool{}

	for i, e := range entries {
		if e.Type != "user" || e.Message == nil {
			continue
		}
		var blocks []json.RawMessage
		if json.Unmarshal(e.Message.Content, &blocks) != nil {
			continue
		}
		for _, block := range blocks {
			var b map[string]json.RawMessage
			if json.Unmarshal(block, &b) != nil {
				continue
			}
			var typ, tuID string
			json.Unmarshal(b["type"], &typ)
			if typ != "tool_result" {
				continue
			}
			json.Unmarshal(b["tool_use_id"], &tuID)
			if owner, ok := tuOwner[tuID]; ok {
				collected[owner] = append(collected[owner], trBlock{block: block, toolUseID: tuID, sourceIdx: i})
			}
		}
	}

	// Check if any repair is needed
	needsRepair := false
	for _, tu := range assistantTUs {
		blocks := collected[tu.entryIdx]
		if len(blocks) == 0 {
			continue
		}
		// Find the next user entry after this assistant
		nextUserIdx := -1
		for j := tu.entryIdx + 1; j < len(entries); j++ {
			if entries[j].Type == "user" && entries[j].IsMessage() {
				nextUserIdx = j
				break
			}
		}
		for _, blk := range blocks {
			if blk.sourceIdx != nextUserIdx {
				needsRepair = true
				break
			}
		}
		if needsRepair {
			break
		}
	}

	if !needsRepair {
		report.BytesAfter = report.BytesBefore
		return entries, report
	}

	// Rebuild: for each assistant with tool_use, ensure the next user entry
	// contains exactly the matching tool_results
	// Strategy: remove all tool_result blocks from their current positions,
	// then insert them into the correct user entry (right after the assistant)

	// First, strip tool_result blocks from all user entries
	trByID := map[string]json.RawMessage{} // tool_use_id -> block
	for _, blocks := range collected {
		for _, blk := range blocks {
			trByID[blk.toolUseID] = blk.block
			userModified[blk.sourceIdx] = true
		}
	}

	// Rebuild user entries without the collected tool_result blocks
	for idx := range userModified {
		e := entries[idx]
		if e.Message == nil {
			continue
		}
		var blocks []json.RawMessage
		if json.Unmarshal(e.Message.Content, &blocks) != nil {
			continue
		}
		var filtered []json.RawMessage
		for _, block := range blocks {
			var b map[string]json.RawMessage
			if json.Unmarshal(block, &b) != nil {
				filtered = append(filtered, block)
				continue
			}
			var typ, tuID string
			json.Unmarshal(b["type"], &typ)
			if typ == "tool_result" {
				json.Unmarshal(b["tool_use_id"], &tuID)
				if _, collected := trByID[tuID]; collected {
					continue // skip — will be reinserted
				}
			}
			filtered = append(filtered, block)
		}
		e.Message.Content, _ = json.Marshal(filtered)
	}

	// Now insert tool_results after their owning assistant entries
	var result []*JSONLEntry
	for i, e := range entries {
		result = append(result, e)

		// After an assistant entry with tool_use, inject a user entry with its tool_results
		if blocks, ok := collected[i]; ok && len(blocks) > 0 {
			var trBlocks []json.RawMessage
			for _, blk := range blocks {
				trBlocks = append(trBlocks, blk.block)
			}

			// Check if next entry is already a user message
			nextIdx := i + 1
			for nextIdx < len(entries) && !entries[nextIdx].IsMessage() {
				nextIdx++
			}
			if nextIdx < len(entries) && entries[nextIdx].Type == "user" && entries[nextIdx].Message != nil {
				// Prepend tool_results to existing next user entry
				var existing []json.RawMessage
				json.Unmarshal(entries[nextIdx].Message.Content, &existing)
				combined := append(trBlocks, existing...)
				entries[nextIdx].Message.Content, _ = json.Marshal(combined)
				repaired += len(blocks)
			}
		}
	}

	// Remove empty user entries (all their tool_results were moved elsewhere)
	var final []*JSONLEntry
	for _, e := range result {
		if userModified[0] && e.Type == "user" && e.Message != nil {
			var blocks []json.RawMessage
			json.Unmarshal(e.Message.Content, &blocks)
			if len(blocks) == 0 {
				continue // empty after removal
			}
		}
		final = append(final, e)
	}

	report.BytesAfter = entriesSize(final)
	report.BytesSaved = report.BytesBefore - report.BytesAfter
	if repaired > 0 {
		report.Details = append(report.Details, fmt.Sprintf("repaired %d scattered tool_result blocks", repaired))
	}
	return final, report
}

func humanBytes(b int64) string {
	switch {
	case b >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(b)/float64(1<<20))
	case b >= 1<<10:
		return fmt.Sprintf("%.1f KB", float64(b)/float64(1<<10))
	default:
		return fmt.Sprintf("%d B", b)
	}
}

// --- Rule: StripFileHistoryRule ---
// Removes file-history-snapshot entries.

type StripFileHistoryRule struct{}

func (r *StripFileHistoryRule) Name() string        { return "strip-file-history" }
func (r *StripFileHistoryRule) Description() string {
	return "Remove file-history-snapshot entries"
}

func (r *StripFileHistoryRule) Apply(entries []*JSONLEntry) ([]*JSONLEntry, CompactRuleReport) {
	report := CompactRuleReport{BytesBefore: entriesSize(entries)}

	before := len(entries)
	result := removeAndReparent(entries, func(e *JSONLEntry) bool {
		return e.Type == "file-history-snapshot"
	})
	removed := before - len(result)

	report.EntriesRemoved = removed
	report.BytesAfter = entriesSize(result)
	report.BytesSaved = report.BytesBefore - report.BytesAfter
	if removed > 0 {
		report.Details = append(report.Details, fmt.Sprintf("removed %d file-history-snapshot entries", removed))
	}
	return result, report
}

// --- Rule: StripErrorRetriesRule ---
// When the same tool is called multiple times in a row and all but the last fail,
// remove the intermediate failed attempts (both tool_use and tool_result).

type StripErrorRetriesRule struct{}

func (r *StripErrorRetriesRule) Name() string        { return "strip-error-retries" }
func (r *StripErrorRetriesRule) Description() string {
	return "Remove intermediate failed tool call retries, keeping only the last attempt"
}

func (r *StripErrorRetriesRule) Apply(entries []*JSONLEntry) ([]*JSONLEntry, CompactRuleReport) {
	report := CompactRuleReport{BytesBefore: entriesSize(entries)}

	// Find sequences of (assistant tool_use → user tool_result with error) for the same tool+input,
	// and mark intermediate ones for removal.
	// Strategy: collect consecutive error runs and strip all but the last.

	type toolCall struct {
		name      string
		inputHash string
		entryIdx  int // index in entries
		isError   bool
	}

	// Build a list of tool calls in order
	toolUseMap := map[string]toolCall{} // tool_use_id → call info
	for i, e := range entries {
		if e.Message == nil || e.Type != "assistant" {
			continue
		}
		var blocks []json.RawMessage
		if json.Unmarshal(e.Message.Content, &blocks) != nil {
			continue
		}
		for _, block := range blocks {
			var b map[string]json.RawMessage
			if json.Unmarshal(block, &b) != nil {
				continue
			}
			var typ string
			json.Unmarshal(b["type"], &typ)
			if typ == "tool_use" {
				var id, name string
				json.Unmarshal(b["id"], &id)
				json.Unmarshal(b["name"], &name)
				inputHash := fmt.Sprintf("%x", sha256.Sum256(b["input"]))
				toolUseMap[id] = toolCall{name: name, inputHash: inputHash, entryIdx: i}
			}
		}
	}

	// Find error tool_results and group consecutive same-tool errors
	type errorRun struct {
		toolUseIDs []string
		entryIdxs  []int // user entry indices containing tool_results
	}

	// Track which tool_use_ids to remove (intermediate errors in a run)
	removeToolUseIDs := map[string]bool{}
	removeEntryIdxs := map[int]bool{} // entry indices to potentially clean

	// Scan user entries for tool_result error sequences
	var currentRun []struct {
		toolUseID string
		key       string // name+inputHash
		userIdx   int
	}

	for i, e := range entries {
		if e.Message == nil || e.Type != "user" {
			// Non-user entry breaks the run
			if len(currentRun) > 1 {
				// Keep last, remove rest
				for _, r := range currentRun[:len(currentRun)-1] {
					removeToolUseIDs[r.toolUseID] = true
					removeEntryIdxs[r.userIdx] = true
				}
			}
			currentRun = nil
			continue
		}

		var blocks []json.RawMessage
		if json.Unmarshal(e.Message.Content, &blocks) != nil {
			continue
		}

		hasError := false
		var errorToolUseID, errorKey string
		for _, block := range blocks {
			var b map[string]json.RawMessage
			if json.Unmarshal(block, &b) != nil {
				continue
			}
			var typ string
			json.Unmarshal(b["type"], &typ)
			if typ == "tool_result" {
				var isErr bool
				var tuID string
				if ef, ok := b["is_error"]; ok {
					json.Unmarshal(ef, &isErr)
				}
				json.Unmarshal(b["tool_use_id"], &tuID)
				if isErr {
					hasError = true
					errorToolUseID = tuID
					if tc, ok := toolUseMap[tuID]; ok {
						errorKey = tc.name + ":" + tc.inputHash
					}
				}
			}
		}

		if hasError && errorKey != "" {
			// Check if same key as current run
			if len(currentRun) > 0 && currentRun[0].key == errorKey {
				currentRun = append(currentRun, struct {
					toolUseID string
					key       string
					userIdx   int
				}{errorToolUseID, errorKey, i})
			} else {
				// Flush previous run
				if len(currentRun) > 1 {
					for _, r := range currentRun[:len(currentRun)-1] {
						removeToolUseIDs[r.toolUseID] = true
						removeEntryIdxs[r.userIdx] = true
					}
				}
				currentRun = []struct {
					toolUseID string
					key       string
					userIdx   int
				}{{errorToolUseID, errorKey, i}}
			}
		} else {
			// Not an error — flush
			if len(currentRun) > 1 {
				for _, r := range currentRun[:len(currentRun)-1] {
					removeToolUseIDs[r.toolUseID] = true
					removeEntryIdxs[r.userIdx] = true
				}
			}
			currentRun = nil
		}
	}
	// Final flush
	if len(currentRun) > 1 {
		for _, r := range currentRun[:len(currentRun)-1] {
			removeToolUseIDs[r.toolUseID] = true
			removeEntryIdxs[r.userIdx] = true
		}
	}

	if len(removeToolUseIDs) == 0 {
		report.BytesAfter = report.BytesBefore
		return entries, report
	}

	// Remove tool_use blocks from assistant entries, and marked user entries
	var result []*JSONLEntry
	removed := 0
	for i, e := range entries {
		if removeEntryIdxs[i] && e.Type == "user" {
			// Remove this user entry (contains error tool_result for intermediate retry)
			removed++
			continue
		}
		if e.Type == "assistant" && e.Message != nil {
			// Filter out tool_use blocks whose IDs are in removeToolUseIDs
			var blocks []json.RawMessage
			if json.Unmarshal(e.Message.Content, &blocks) == nil {
				var filtered []json.RawMessage
				for _, block := range blocks {
					var b map[string]json.RawMessage
					if json.Unmarshal(block, &b) != nil {
						filtered = append(filtered, block)
						continue
					}
					var typ string
					json.Unmarshal(b["type"], &typ)
					if typ == "tool_use" {
						var id string
						json.Unmarshal(b["id"], &id)
						if removeToolUseIDs[id] {
							continue
						}
					}
					filtered = append(filtered, block)
				}
				if len(filtered) != len(blocks) {
					if len(filtered) == 0 {
						// Entire assistant message was just removed tool_uses — remove the entry
						removed++
						continue
					}
					e.Message.Content, _ = json.Marshal(filtered)
				}
			}
		}
		result = append(result, e)
	}

	report.EntriesRemoved = removed
	report.BytesAfter = entriesSize(result)
	report.BytesSaved = report.BytesBefore - report.BytesAfter
	if removed > 0 {
		report.Details = append(report.Details,
			fmt.Sprintf("removed %d intermediate retry entries (%d tool calls)",
				removed, len(removeToolUseIDs)))
	}
	return result, report
}

// --- Rule: TruncateLargeBashRule ---
// Truncates large successful Bash tool_result content, keeping only the last N lines.

type TruncateLargeBashRule struct {
	ThresholdBytes int // default: 4096
	KeepLines      int // default: 30
}

func (r *TruncateLargeBashRule) Name() string { return "truncate-large-bash" }
func (r *TruncateLargeBashRule) Description() string {
	return "Truncate large Bash output to last N lines"
}

func (r *TruncateLargeBashRule) threshold() int {
	if r.ThresholdBytes > 0 {
		return r.ThresholdBytes
	}
	return 4096
}

func (r *TruncateLargeBashRule) keepLines() int {
	if r.KeepLines > 0 {
		return r.KeepLines
	}
	return 30
}

func (r *TruncateLargeBashRule) Apply(entries []*JSONLEntry) ([]*JSONLEntry, CompactRuleReport) {
	report := CompactRuleReport{BytesBefore: entriesSize(entries)}
	truncated := 0

	// Collect Bash tool_use IDs
	bashToolUseIDs := map[string]bool{}
	for _, e := range entries {
		if e.Message == nil || e.Type != "assistant" {
			continue
		}
		var blocks []json.RawMessage
		if json.Unmarshal(e.Message.Content, &blocks) != nil {
			continue
		}
		for _, block := range blocks {
			var b map[string]json.RawMessage
			if json.Unmarshal(block, &b) != nil {
				continue
			}
			var typ, id, name string
			json.Unmarshal(b["type"], &typ)
			if typ == "tool_use" {
				json.Unmarshal(b["id"], &id)
				json.Unmarshal(b["name"], &name)
				if name == "Bash" {
					bashToolUseIDs[id] = true
				}
			}
		}
	}

	for _, e := range entries {
		if e.Message == nil || e.Type != "user" {
			continue
		}
		var blocks []json.RawMessage
		if json.Unmarshal(e.Message.Content, &blocks) != nil {
			continue
		}
		modified := false
		for i, block := range blocks {
			var b map[string]json.RawMessage
			if json.Unmarshal(block, &b) != nil {
				continue
			}
			var typ, toolUseID string
			json.Unmarshal(b["type"], &typ)
			if typ != "tool_result" {
				continue
			}
			json.Unmarshal(b["tool_use_id"], &toolUseID)
			if !bashToolUseIDs[toolUseID] {
				continue
			}

			// Extract text from content
			text := extractToolResultText(b["content"])
			if len(text) < r.threshold() {
				continue
			}

			// Keep last N lines
			lines := strings.Split(text, "\n")
			keep := r.keepLines()
			if len(lines) <= keep {
				continue
			}
			truncatedText := fmt.Sprintf("[truncated %d lines, showing last %d of %d]\n…\n%s",
				len(lines)-keep, keep, len(lines),
				strings.Join(lines[len(lines)-keep:], "\n"))

			newContent, _ := json.Marshal([]map[string]string{
				{"type": "text", "text": truncatedText},
			})
			b["content"] = newContent
			blocks[i], _ = json.Marshal(b)
			modified = true
			truncated++
		}
		if modified {
			e.Message.Content, _ = json.Marshal(blocks)
		}
	}

	report.BytesAfter = entriesSize(entries)
	report.BytesSaved = report.BytesBefore - report.BytesAfter
	if truncated > 0 {
		report.Details = append(report.Details, fmt.Sprintf("truncated %d large Bash outputs", truncated))
	}
	return entries, report
}

// extractToolResultText gets the text string from a tool_result content field.
func extractToolResultText(content json.RawMessage) string {
	// Try as string
	var s string
	if json.Unmarshal(content, &s) == nil {
		return s
	}
	// Try as array of blocks
	var arr []json.RawMessage
	if json.Unmarshal(content, &arr) != nil {
		return ""
	}
	var texts []string
	for _, item := range arr {
		var b map[string]json.RawMessage
		if json.Unmarshal(item, &b) != nil {
			continue
		}
		var typ string
		json.Unmarshal(b["type"], &typ)
		if typ == "text" {
			var t string
			json.Unmarshal(b["text"], &t)
			texts = append(texts, t)
		}
	}
	return strings.Join(texts, "\n")
}
