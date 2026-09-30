package server

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"astudio2api/internal/store"
)

// Anthropic Messages API bridge.
//
// The upstream only speaks the OpenAI Chat Completions protocol, so this file
// translates in both directions: request blocks -> messages/tools, and the
// completion (streaming or not) -> Anthropic SSE events.

type anthropicRequest struct {
	Model         string          `json:"model"`
	MaxTokens     int             `json:"max_tokens"`
	System        json.RawMessage `json:"system"`
	Messages      []anthropicMsg  `json:"messages"`
	Stream        bool            `json:"stream"`
	Temperature   *float64        `json:"temperature"`
	TopP          *float64        `json:"top_p"`
	StopSequences []string        `json:"stop_sequences"`
	Tools         []anthropicTool `json:"tools"`
	ToolChoice    json.RawMessage `json:"tool_choice"`
	Metadata      map[string]any  `json:"metadata"`
}

type anthropicMsg struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

type anthropicTool struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"input_schema"`
}

func (s *Server) handleAnthropicMessages(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeAnthropicError(w, http.StatusMethodNotAllowed, "invalid_request_error", "Use POST.")
		return
	}
	raw, err := io.ReadAll(io.LimitReader(r.Body, maxRequestBody))
	if err != nil {
		writeAnthropicError(w, http.StatusBadRequest, "invalid_request_error", "Could not read request body.")
		return
	}
	var req anthropicRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		writeAnthropicError(w, http.StatusBadRequest, "invalid_request_error", "Request body must be valid JSON.")
		return
	}
	payload, err := toChatPayload(&req, s.registry.SlugFor(req.Model))
	if err != nil {
		writeAnthropicError(w, http.StatusBadRequest, "invalid_request_error", err.Error())
		return
	}
	body, err := json.Marshal(payload)
	if err != nil {
		writeAnthropicError(w, http.StatusBadRequest, "invalid_request_error", "Could not encode request.")
		return
	}

	spec := &proxySpec{
		path:      "chat/completions",
		payload:   payload,
		body:      body,
		requested: req.Model,
		stream:    req.Stream,
		failover:  true,
	}
	started := time.Now()

	release, err := s.acquire(r.Context())
	if err != nil {
		writeAnthropicError(w, http.StatusRequestTimeout, "timeout_error", "Request cancelled while waiting for capacity.")
		return
	}
	defer release()

	ctx := r.Context()
	if !req.Stream {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, time.Duration(s.store.Settings().RequestTimeoutS)*time.Second)
		defer cancel()
	}

	result, err := s.forward(ctx, spec)
	if err != nil {
		s.recordFailure(spec, currentKey(r), started, err)
		writeAnthropicError(w, http.StatusBadGateway, "upstream_error", err.Error())
		return
	}
	if req.Stream {
		s.anthropicStream(w, r, spec, result, started)
		return
	}
	s.anthropicBuffered(w, spec, result, started)
}

// toChatPayload converts an Anthropic Messages request into a Chat Completions
// request.
func toChatPayload(req *anthropicRequest, slug string) (map[string]any, error) {
	if slug == "" {
		return nil, fmt.Errorf("`model` is required")
	}
	messages := make([]any, 0, len(req.Messages)+1)

	if sys := anthropicSystemText(req.System); sys != "" {
		messages = append(messages, map[string]any{"role": "system", "content": sys})
	}

	for _, m := range req.Messages {
		converted, err := convertAnthropicMessage(m)
		if err != nil {
			return nil, err
		}
		messages = append(messages, converted...)
	}

	payload := map[string]any{
		"model":    slug,
		"messages": messages,
		"stream":   req.Stream,
	}
	if req.MaxTokens > 0 {
		payload["max_tokens"] = req.MaxTokens
	}
	if req.Temperature != nil {
		payload["temperature"] = *req.Temperature
	}
	if req.TopP != nil {
		payload["top_p"] = *req.TopP
	}
	if len(req.StopSequences) > 0 {
		payload["stop"] = req.StopSequences
	}
	if len(req.Tools) > 0 {
		tools := make([]any, 0, len(req.Tools))
		for _, t := range req.Tools {
			schema := t.InputSchema
			if len(schema) == 0 {
				schema = json.RawMessage(`{"type":"object","properties":{}}`)
			}
			tools = append(tools, map[string]any{
				"type": "function",
				"function": map[string]any{
					"name":        t.Name,
					"description": t.Description,
					"parameters":  schema,
				},
			})
		}
		payload["tools"] = tools
	}
	if tc := anthropicToolChoice(req.ToolChoice); tc != nil {
		payload["tool_choice"] = tc
	}
	if req.Stream {
		payload["stream_options"] = map[string]any{"include_usage": true}
	}
	return payload, nil
}

// anthropicSystemText flattens the `system` field, which may be a string or an
// array of text blocks.
func anthropicSystemText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var asString string
	if err := json.Unmarshal(raw, &asString); err == nil {
		return asString
	}
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(raw, &blocks); err == nil {
		var parts []string
		for _, b := range blocks {
			if b.Type == "text" && b.Text != "" {
				parts = append(parts, b.Text)
			}
		}
		return strings.Join(parts, "\n\n")
	}
	return ""
}

// convertAnthropicMessage turns one Anthropic message into one or more
// Chat Completions messages (tool results become separate `tool` messages).
func convertAnthropicMessage(m anthropicMsg) ([]any, error) {
	// Plain string content.
	var text string
	if err := json.Unmarshal(m.Content, &text); err == nil {
		return []any{map[string]any{"role": m.Role, "content": text}}, nil
	}

	var blocks []map[string]any
	if err := json.Unmarshal(m.Content, &blocks); err != nil {
		return nil, fmt.Errorf("message content must be a string or an array of blocks")
	}

	parts := make([]any, 0, len(blocks))
	var toolCalls []any
	var toolResults []any

	for _, b := range blocks {
		switch b["type"] {
		case "text":
			if t, _ := b["text"].(string); t != "" {
				parts = append(parts, map[string]any{"type": "text", "text": t})
			}
		case "image":
			if url := anthropicImageURL(b); url != "" {
				parts = append(parts, map[string]any{"type": "image_url", "image_url": map[string]any{"url": url}})
			}
		case "tool_use":
			id, _ := b["id"].(string)
			name, _ := b["name"].(string)
			input, _ := json.Marshal(b["input"])
			toolCalls = append(toolCalls, map[string]any{
				"id":   id,
				"type": "function",
				"function": map[string]any{
					"name":      name,
					"arguments": string(input),
				},
			})
		case "tool_result":
			toolResults = append(toolResults, map[string]any{
				"role":         "tool",
				"tool_call_id": stringField(b, "tool_use_id"),
				"content":      anthropicToolResultText(b["content"]),
			})
		}
	}

	out := make([]any, 0, 1+len(toolResults))
	if m.Role == "assistant" {
		msg := map[string]any{"role": "assistant"}
		if len(parts) > 0 {
			msg["content"] = parts
		} else {
			msg["content"] = ""
		}
		if len(toolCalls) > 0 {
			msg["tool_calls"] = toolCalls
		}
		out = append(out, msg)
		return out, nil
	}

	// Tool results are standalone `tool` messages in the OpenAI protocol.
	out = append(out, toolResults...)
	if len(parts) > 0 {
		out = append(out, map[string]any{"role": m.Role, "content": parts})
	}
	if len(out) == 0 {
		out = append(out, map[string]any{"role": m.Role, "content": ""})
	}
	return out, nil
}

func anthropicImageURL(block map[string]any) string {
	source, ok := block["source"].(map[string]any)
	if !ok {
		return ""
	}
	switch source["type"] {
	case "base64":
		media, _ := source["media_type"].(string)
		data, _ := source["data"].(string)
		if media == "" || data == "" {
			return ""
		}
		return "data:" + media + ";base64," + data
	case "url":
		u, _ := source["url"].(string)
		return u
	}
	return ""
}

func anthropicToolResultText(content any) string {
	switch v := content.(type) {
	case string:
		return v
	case []any:
		var parts []string
		for _, item := range v {
			if m, ok := item.(map[string]any); ok {
				if t, ok := m["text"].(string); ok {
					parts = append(parts, t)
				}
			}
		}
		return strings.Join(parts, "\n")
	default:
		b, _ := json.Marshal(v)
		return string(b)
	}
}

func anthropicToolChoice(raw json.RawMessage) any {
	if len(raw) == 0 {
		return nil
	}
	var tc struct {
		Type string `json:"type"`
		Name string `json:"name"`
	}
	if err := json.Unmarshal(raw, &tc); err != nil {
		return nil
	}
	switch tc.Type {
	case "auto":
		return "auto"
	case "any":
		return "required"
	case "tool":
		if tc.Name != "" {
			return map[string]any{"type": "function", "function": map[string]any{"name": tc.Name}}
		}
	case "none":
		return "none"
	}
	return nil
}

// --- response translation ---------------------------------------------------

type anthropicUsage struct {
	InputTokens  int64 `json:"input_tokens"`
	OutputTokens int64 `json:"output_tokens"`
}

type toolCallAccumulator struct {
	ID        string
	Name      string
	Arguments strings.Builder
	Started   bool
	BlockIdx  int
}

func (s *Server) anthropicBuffered(w http.ResponseWriter, spec *proxySpec, result *upstreamResult, started time.Time) {
	defer result.resp.Body.Close()

	entry := &store.LogEntry{
		Time:          started.Unix(),
		Model:         spec.requested,
		UpstreamModel: upstreamSlug(spec),
		Status:        http.StatusOK,
	}
	if result.account != nil {
		entry.Account = firstNonEmpty(result.account.Nickname, result.account.AccountID, result.account.ID)
	}
	defer func() {
		entry.DurationMs = time.Since(started).Milliseconds()
		s.store.RecordRequest(entry)
		if result.account != nil {
			s.markAccountUsed(result.account.ID)
		}
	}()

	body, err := io.ReadAll(io.LimitReader(result.resp.Body, maxRequestBody))
	if err != nil {
		writeAnthropicError(w, http.StatusBadGateway, "api_error", "Could not read upstream response.")
		return
	}
	var completion map[string]any
	if err := json.Unmarshal(body, &completion); err != nil {
		writeAnthropicError(w, http.StatusBadGateway, "api_error", "Upstream returned an unreadable response.")
		return
	}
	foldUsageMap(mapField(completion, "usage"), entry)

	content := make([]any, 0, 2)
	stopReason := "end_turn"

	choices, _ := completion["choices"].([]any)
	if len(choices) > 0 {
		choice, _ := choices[0].(map[string]any)
		message := mapField(choice, "message")
		if text := stringField(message, "content"); text != "" {
			content = append(content, map[string]any{"type": "text", "text": text})
		}
		if calls, ok := message["tool_calls"].([]any); ok && len(calls) > 0 {
			stopReason = "tool_use"
			for _, c := range calls {
				cm, _ := c.(map[string]any)
				fn := mapField(cm, "function")
				content = append(content, map[string]any{
					"type":  "tool_use",
					"id":    stringField(cm, "id"),
					"name":  stringField(fn, "name"),
					"input": rawJSONObject(stringField(fn, "arguments")),
				})
			}
		}
		if fr := stringField(choice, "finish_reason"); fr != "" && len(content) == 0 || fr == "length" {
			stopReason = mapStopReason(fr, stopReason)
		}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"id":            "msg_" + randomToken()[:24],
		"type":          "message",
		"role":          "assistant",
		"model":         spec.requested,
		"content":       content,
		"stop_reason":   stopReason,
		"stop_sequence": nil,
		"usage": anthropicUsage{
			InputTokens:  entry.PromptTokens,
			OutputTokens: entry.CompletionTokens,
		},
	})
}

// anthropicStream converts the upstream Chat Completions SSE stream into
// Anthropic Messages SSE events.
func (s *Server) anthropicStream(w http.ResponseWriter, r *http.Request, spec *proxySpec, result *upstreamResult, started time.Time) {
	defer result.resp.Body.Close()

	entry := &store.LogEntry{
		Time:          started.Unix(),
		Model:         spec.requested,
		UpstreamModel: upstreamSlug(spec),
		Stream:        true,
		Status:        http.StatusOK,
	}
	if result.account != nil {
		entry.Account = firstNonEmpty(result.account.Nickname, result.account.AccountID, result.account.ID)
	}

	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	flusher, _ := w.(http.Flusher)

	send := func(event string, data any) bool {
		payload, err := json.Marshal(data)
		if err != nil {
			return false
		}
		if _, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, payload); err != nil {
			return false
		}
		if flusher != nil {
			flusher.Flush()
		}
		return true
	}

	messageID := "msg_" + randomToken()[:24]
	send("message_start", map[string]any{
		"type": "message_start",
		"message": map[string]any{
			"id": messageID, "type": "message", "role": "assistant", "model": spec.requested,
			"content": []any{}, "stop_reason": nil, "stop_sequence": nil,
			"usage": anthropicUsage{},
		},
	})

	nextBlock := 0
	textBlockOpen := false
	toolBlocks := map[int]*toolCallAccumulator{}
	stopReason := "end_turn"

	closeText := func() {
		if textBlockOpen {
			send("content_block_stop", map[string]any{"type": "content_block_stop", "index": nextBlock - 1})
			textBlockOpen = false
		}
	}

	scanner := bufio.NewScanner(newIdleReader(result.resp.Body, time.Duration(s.store.Settings().IdleTimeoutS)*time.Second))
	scanner.Buffer(make([]byte, 0, 64<<10), 8<<20)

	for scanner.Scan() {
		if r.Context().Err() != nil {
			break
		}
		line := bytes.TrimSpace(scanner.Bytes())
		payload, ok := bytes.CutPrefix(line, []byte("data:"))
		if !ok {
			continue
		}
		payload = bytes.TrimSpace(payload)
		if len(payload) == 0 || bytes.Equal(payload, []byte("[DONE]")) {
			continue
		}
		var chunk map[string]any
		if err := json.Unmarshal(payload, &chunk); err != nil {
			continue
		}
		foldUsageMap(mapField(chunk, "usage"), entry)

		choices, _ := chunk["choices"].([]any)
		if len(choices) == 0 {
			continue
		}
		choice, _ := choices[0].(map[string]any)
		delta := mapField(choice, "delta")

		if text := stringField(delta, "content"); text != "" {
			if !textBlockOpen {
				send("content_block_start", map[string]any{
					"type": "content_block_start", "index": nextBlock,
					"content_block": map[string]any{"type": "text", "text": ""},
				})
				textBlockOpen = true
				nextBlock++
			}
			if entry.FirstTokenMs == 0 {
				entry.FirstTokenMs = time.Since(started).Milliseconds()
			}
			if !send("content_block_delta", map[string]any{
				"type": "content_block_delta", "index": nextBlock - 1,
				"delta": map[string]any{"type": "text_delta", "text": text},
			}) {
				break
			}
		}

		if calls, ok := delta["tool_calls"].([]any); ok {
			for _, c := range calls {
				cm, _ := c.(map[string]any)
				idx := int(intField(cm, "index"))
				acc := toolBlocks[idx]
				if acc == nil {
					acc = &toolCallAccumulator{BlockIdx: -1}
					toolBlocks[idx] = acc
				}
				if id := stringField(cm, "id"); id != "" {
					acc.ID = id
				}
				fn := mapField(cm, "function")
				if name := stringField(fn, "name"); name != "" {
					acc.Name = name
				}
				if !acc.Started && acc.Name != "" {
					closeText()
					acc.BlockIdx = nextBlock
					acc.Started = true
					nextBlock++
					if !send("content_block_start", map[string]any{
						"type": "content_block_start", "index": acc.BlockIdx,
						"content_block": map[string]any{"type": "tool_use", "id": acc.ID, "name": acc.Name, "input": map[string]any{}},
					}) {
						return
					}
				}
				if args := stringField(fn, "arguments"); args != "" {
					acc.Arguments.WriteString(args)
					if acc.Started {
						send("content_block_delta", map[string]any{
							"type": "content_block_delta", "index": acc.BlockIdx,
							"delta": map[string]any{"type": "input_json_delta", "partial_json": args},
						})
					}
				}
			}
		}

		if fr := stringField(choice, "finish_reason"); fr != "" {
			stopReason = mapStopReason(fr, stopReason)
		}
	}

	closeText()
	for _, acc := range toolBlocks {
		if acc.Started {
			send("content_block_stop", map[string]any{"type": "content_block_stop", "index": acc.BlockIdx})
		}
	}

	send("message_delta", map[string]any{
		"type":  "message_delta",
		"delta": map[string]any{"stop_reason": stopReason, "stop_sequence": nil},
		"usage": map[string]any{"output_tokens": entry.CompletionTokens},
	})
	send("message_stop", map[string]any{"type": "message_stop"})

	entry.DurationMs = time.Since(started).Milliseconds()
	s.store.RecordRequest(entry)
	if result.account != nil {
		s.markAccountUsed(result.account.ID)
	}
}

func mapStopReason(finishReason, fallback string) string {
	switch finishReason {
	case "stop":
		return "end_turn"
	case "length":
		return "max_tokens"
	case "tool_calls", "function_call":
		return "tool_use"
	case "content_filter":
		return "stop_sequence"
	case "":
		return fallback
	}
	return fallback
}

// rawJSONObject decodes a JSON string into a value, falling back to an empty
// object so Anthropic clients always receive an `input` object.
func rawJSONObject(s string) any {
	if strings.TrimSpace(s) == "" {
		return map[string]any{}
	}
	var out any
	if err := json.Unmarshal([]byte(s), &out); err != nil {
		return map[string]any{}
	}
	return out
}

func mapField(m map[string]any, key string) map[string]any {
	if m == nil {
		return map[string]any{}
	}
	if v, ok := m[key].(map[string]any); ok {
		return v
	}
	return map[string]any{}
}

func stringField(m map[string]any, key string) string {
	if m == nil {
		return ""
	}
	s, _ := m[key].(string)
	return s
}

func writeAnthropicError(w http.ResponseWriter, status int, kind, message string) {
	writeJSON(w, status, map[string]any{
		"type": "error",
		"error": map[string]any{
			"type":    kind,
			"message": message,
		},
	})
}
