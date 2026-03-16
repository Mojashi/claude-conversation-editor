package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"

	"github.com/wailsapp/wails/v2/pkg/runtime"
)

const summarizeSystemPrompt = `You are a conversation summarizer. Output only a concise summary of the conversation provided. Do not use any tools. Do not ask questions. Output the summary text directly with no preamble.`

const idealizeSystemPrompt = `You are an expert at cleaning up Claude Code conversation histories for context engineering.

You have two modes. Choose the best one for the situation:

MODE "actions" — Per-message triage. For each message, decide:
- "delete": errors, failed attempts, retries, unnecessary detours, verbose outputs that add no value
- "keep": essential context, correct approaches, important decisions
- "edit": messages worth keeping but with wasted content (trim unnecessary parts, fix errors). Provide the cleaned text in edited_content.
Use this mode when most messages can be kept as-is or simply deleted.

MODE "rewrite" — Full rewrite. Output a new sequence of messages (role + content) that replaces the entire selection.
Use this mode when the conversation is too messy for per-message triage and needs a clean rewrite.

Be aggressive about deleting waste. Preserve the minimum context needed to understand what happened and continue the work.`

// runClaude calls claude -p with --output-format json.
func runClaude(prompt, jsonSchema string) (string, error) {
	return runClaudeWithSystem(prompt, jsonSchema, "")
}

// runClaudeStreaming streams output tokens via Wails events and returns the final result.
func runClaudeStreaming(ctx context.Context, prompt, jsonSchema, systemPrompt string) (string, error) {
	args := []string{"-p", "--output-format", "stream-json", "--verbose", "--effort", "low", "--model", "claude-sonnet-4-6", "--max-turns", "1"}
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
	var allLines []string
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 4*1024*1024), 4*1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}
		allLines = append(allLines, line)
		var event map[string]json.RawMessage
		if json.Unmarshal([]byte(line), &event) != nil {
			continue
		}
		var evType string
		json.Unmarshal(event["type"], &evType)

		switch evType {
		case "assistant":
			if ctx != nil {
				runtime.EventsEmit(ctx, "claude:stream", line)
			}
		case "result":
			if so, ok := event["structured_output"]; ok && string(so) != "null" {
				finalResult = string(so)
			} else {
				raw := event["result"]
				var s string
				if json.Unmarshal(raw, &s) == nil {
					finalResult = s
				} else {
					finalResult = string(raw)
				}
			}
		}
	}

	if err := cmd.Wait(); err != nil {
		if se := strings.TrimSpace(stderrBuf.String()); se != "" {
			return "", fmt.Errorf("%s", se)
		}
		return "", err
	}
	if finalResult == "" && len(allLines) > 0 {
		return "", fmt.Errorf("no result event found. last lines: %s", allLines[len(allLines)-1])
	}
	return strings.TrimSpace(finalResult), nil
}

func runClaudeWithSystem(prompt, jsonSchema, systemPrompt string) (string, error) {
	args := []string{"-p", "--output-format", "json", "--effort", "low", "--model", "claude-sonnet-4-6", "--max-turns", "3", "--tools", ""}
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
		if ee, ok := err.(*exec.ExitError); ok {
			combined := out
			if len(combined) == 0 {
				combined = ee.Stderr
			}
			if len(combined) > 0 {
				var envelope map[string]json.RawMessage
				if json.Unmarshal(combined, &envelope) == nil {
					if so, ok := envelope["structured_output"]; ok && string(so) != "null" {
						return string(so), nil
					}
					if r, ok := envelope["result"]; ok {
						var s string
						if json.Unmarshal(r, &s) == nil && s != "" {
							return strings.TrimSpace(s), nil
						}
					}
				}
				return "", fmt.Errorf("%s", strings.TrimSpace(string(combined)))
			}
		}
		return "", err
	}

	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(out, &envelope); err != nil {
		return strings.TrimSpace(string(out)), nil
	}
	if errField, ok := envelope["error"]; ok {
		var e string
		json.Unmarshal(errField, &e)
		if e != "" {
			return "", fmt.Errorf("%s", e)
		}
	}
	if so, ok := envelope["structured_output"]; ok && string(so) != "null" {
		return string(so), nil
	}
	var result string
	if r, ok := envelope["result"]; ok {
		if json.Unmarshal(r, &result) == nil {
			return strings.TrimSpace(result), nil
		}
		return string(r), nil
	}
	return strings.TrimSpace(string(out)), nil
}

// extractText extracts readable text from a message content field.
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
			// skip
		}
	}
	return strings.Join(texts, "\n")
}

// buildIdealizeSchema generates a JSON schema with mode field to choose actions or rewrite.
func buildIdealizeSchema(uuids []string) string {
	uuidEnum, _ := json.Marshal(uuids)
	return fmt.Sprintf(`{"type":"object","properties":{"mode":{"type":"string","enum":["actions","rewrite"]},"actions":{"type":"array","items":{"type":"object","properties":{"uuid":{"type":"string","enum":%s},"action":{"type":"string","enum":["delete","keep","edit"]},"edited_content":{"type":"string"}},"required":["uuid","action"]}},"messages":{"type":"array","items":{"type":"object","properties":{"role":{"type":"string","enum":["user","assistant"]},"content":{"type":"string"}},"required":["role","content"]}}},"required":["mode"]}`,
		string(uuidEnum))
}
