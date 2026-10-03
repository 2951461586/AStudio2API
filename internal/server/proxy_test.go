package server

import (
	"encoding/json"
	"testing"
	"time"

	"astudio2api/internal/store"
)

func decodeChunk(t *testing.T, b []byte) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("output is not valid JSON: %v (%s)", err, b)
	}
	return m
}

func TestIntField(t *testing.T) {
	tests := []struct {
		name string
		m    map[string]any
		keys []string
		want int64
	}{
		{"float64", map[string]any{"a": float64(12)}, []string{"a"}, 12},
		{"json number", map[string]any{"a": json.Number("34")}, []string{"a"}, 34},
		{"int64", map[string]any{"a": int64(5)}, []string{"a"}, 5},
		{"int", map[string]any{"a": 7}, []string{"a"}, 7},
		{"missing", map[string]any{}, []string{"a"}, 0},
		{"zero ignored", map[string]any{"a": float64(0)}, []string{"a"}, 0},
		{"negative ignored", map[string]any{"a": float64(-3)}, []string{"a"}, 0},
		{"wrong type", map[string]any{"a": "9"}, []string{"a"}, 0},
		{"first non-zero key wins", map[string]any{"a": float64(1), "b": float64(2)}, []string{"a", "b"}, 1},
		{"falls through to second key", map[string]any{"b": float64(2)}, []string{"a", "b"}, 2},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := intField(tc.m, tc.keys...); got != tc.want {
				t.Fatalf("intField(%v) = %d, want %d", tc.keys, got, tc.want)
			}
		})
	}
}

func TestFoldUsageMapChatShape(t *testing.T) {
	entry := &store.LogEntry{}
	foldUsageMap(map[string]any{
		"prompt_tokens":     float64(100),
		"completion_tokens": float64(20),
		"prompt_tokens_details": map[string]any{
			"cached_tokens": float64(64),
		},
		"completion_tokens_details": map[string]any{
			"reasoning_tokens": float64(8),
		},
	}, entry)

	if entry.PromptTokens != 100 || entry.CompletionTokens != 20 {
		t.Fatalf("tokens = (%d, %d)", entry.PromptTokens, entry.CompletionTokens)
	}
	if entry.CachedTokens != 64 || entry.ReasoningTokens != 8 {
		t.Fatalf("details = (cached %d, reasoning %d)", entry.CachedTokens, entry.ReasoningTokens)
	}
}

func TestFoldUsageMapResponsesShape(t *testing.T) {
	entry := &store.LogEntry{}
	foldUsageMap(map[string]any{
		"input_tokens":  float64(50),
		"output_tokens": float64(9),
		"input_tokens_details": map[string]any{
			"cached_tokens": float64(16),
		},
		"output_tokens_details": map[string]any{
			"reasoning_tokens": float64(4),
		},
	}, entry)

	if entry.PromptTokens != 50 || entry.CompletionTokens != 9 {
		t.Fatalf("tokens = (%d, %d)", entry.PromptTokens, entry.CompletionTokens)
	}
	if entry.CachedTokens != 16 || entry.ReasoningTokens != 4 {
		t.Fatalf("details = (cached %d, reasoning %d)", entry.CachedTokens, entry.ReasoningTokens)
	}
}

func TestFoldUsageMapTopLevelFallbackAndPreserve(t *testing.T) {
	// Top-level cached/reasoning tokens are only used when the details blocks
	// did not already set them.
	entry := &store.LogEntry{CachedTokens: 7, ReasoningTokens: 3}
	foldUsageMap(map[string]any{
		"cached_tokens":    float64(99),
		"reasoning_tokens": float64(99),
	}, entry)
	if entry.CachedTokens != 7 || entry.ReasoningTokens != 3 {
		t.Fatalf("existing details were overwritten: cached=%d reasoning=%d", entry.CachedTokens, entry.ReasoningTokens)
	}

	// Zero counts from a later chunk must not clobber a known value.
	entry2 := &store.LogEntry{PromptTokens: 40, CompletionTokens: 10}
	foldUsageMap(map[string]any{"prompt_tokens": float64(0), "completion_tokens": float64(0)}, entry2)
	if entry2.PromptTokens != 40 || entry2.CompletionTokens != 10 {
		t.Fatalf("zero usage clobbered counters: %+v", entry2)
	}
}

func TestApplyUsage(t *testing.T) {
	entry := &store.LogEntry{}
	applyUsage(map[string]any{"usage": map[string]any{"prompt_tokens": float64(11)}}, entry)
	if entry.PromptTokens != 11 {
		t.Fatalf("PromptTokens = %d, want 11", entry.PromptTokens)
	}
	// A body without a usage object must be a no-op.
	before := *entry
	applyUsage(map[string]any{"id": "x"}, entry)
	if *entry != before {
		t.Fatalf("applyUsage mutated entry without usage: %+v", entry)
	}
}

func TestHasContentDelta(t *testing.T) {
	tests := []struct {
		name  string
		chunk map[string]any
		want  bool
	}{
		{"text delta", map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"content": "hi"}}}}, true},
		{"empty text", map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"content": ""}}}}, false},
		{"reasoning delta", map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"reasoning_content": "think"}}}}, true},
		{"tool call delta", map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"tool_calls": []any{}}}}}, true},
		{"role-only delta", map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"role": "assistant"}}}}, false},
		{"responses text event", map[string]any{"type": "response.output_text.delta"}, true},
		{"responses reasoning event", map[string]any{"type": "response.reasoning_summary_text.delta"}, true},
		{"responses completed event", map[string]any{"type": "response.completed"}, false},
		{"empty", map[string]any{}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := hasContentDelta(tc.chunk); got != tc.want {
				t.Fatalf("hasContentDelta() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestRewriteChunkInvalid(t *testing.T) {
	if out, ok := rewriteChunk([]byte("{not json"), "glm", &store.LogEntry{}, time.Now()); ok || out != nil {
		t.Fatalf("rewriteChunk(invalid) = (%q, %v), want (nil, false)", out, ok)
	}
}

func TestRewriteChunkRewritesTopLevelModel(t *testing.T) {
	entry := &store.LogEntry{}
	out, ok := rewriteChunk([]byte(`{"model":"xopglm52","choices":[]}`), "GLM-5.2", entry, time.Now())
	if !ok {
		t.Fatal("rewriteChunk returned ok=false")
	}
	if got := decodeChunk(t, out)["model"]; got != "GLM-5.2" {
		t.Fatalf("model = %v, want GLM-5.2", got)
	}
}

func TestRewriteChunkDoesNotInventModel(t *testing.T) {
	entry := &store.LogEntry{}
	out, ok := rewriteChunk([]byte(`{"choices":[]}`), "GLM-5.2", entry, time.Now())
	if !ok {
		t.Fatal("rewriteChunk returned ok=false")
	}
	if _, present := decodeChunk(t, out)["model"]; present {
		t.Fatal("rewriteChunk added a model field that was not in the chunk")
	}
}

func TestRewriteChunkResponsesEnvelope(t *testing.T) {
	entry := &store.LogEntry{}
	chunk := []byte(`{"type":"response.output_text.delta","response":{"model":"xopglm52","usage":{"input_tokens":12,"output_tokens":3}}}`)
	out, ok := rewriteChunk(chunk, "GLM-5.2", entry, time.Now())
	if !ok {
		t.Fatal("rewriteChunk returned ok=false")
	}
	resp, _ := decodeChunk(t, out)["response"].(map[string]any)
	if resp["model"] != "GLM-5.2" {
		t.Fatalf("response.model = %v", resp["model"])
	}
	if entry.PromptTokens != 12 || entry.CompletionTokens != 3 {
		t.Fatalf("nested usage not folded: %+v", entry)
	}
}

func TestRewriteChunkFoldsUsage(t *testing.T) {
	entry := &store.LogEntry{}
	out, ok := rewriteChunk([]byte(`{"usage":{"prompt_tokens":5,"completion_tokens":2},"choices":[]}`), "", entry, time.Now())
	if !ok {
		t.Fatal("rewriteChunk returned ok=false")
	}
	if entry.PromptTokens != 5 || entry.CompletionTokens != 2 {
		t.Fatalf("usage not folded: %+v", entry)
	}
	// requested=="" must not inject a model field either.
	if _, present := decodeChunk(t, out)["model"]; present {
		t.Fatal("rewriteChunk injected model with empty requested name")
	}
}

func TestRewriteChunkFirstTokenLatency(t *testing.T) {
	entry := &store.LogEntry{}
	started := time.Now().Add(-40 * time.Millisecond)
	if _, ok := rewriteChunk([]byte(`{"choices":[{"delta":{"content":"hi"}}]}`), "m", entry, started); !ok {
		t.Fatal("rewriteChunk returned ok=false")
	}
	if entry.FirstTokenMs < 30 {
		t.Fatalf("FirstTokenMs = %d, want >= 30", entry.FirstTokenMs)
	}
	// A usage-only chunk carries no output and must not set first-token latency.
	entry2 := &store.LogEntry{}
	rewriteChunk([]byte(`{"usage":{"prompt_tokens":1}}`), "m", entry2, started)
	if entry2.FirstTokenMs != 0 {
		t.Fatalf("usage-only chunk set FirstTokenMs = %d", entry2.FirstTokenMs)
	}
	// An already-measured stream keeps its first latency.
	entry3 := &store.LogEntry{FirstTokenMs: 123}
	rewriteChunk([]byte(`{"choices":[{"delta":{"content":"more"}}]}`), "m", entry3, started)
	if entry3.FirstTokenMs != 123 {
		t.Fatalf("FirstTokenMs was overwritten: %d", entry3.FirstTokenMs)
	}
}

func TestUpstreamSlug(t *testing.T) {
	tests := []struct {
		name    string
		payload map[string]any
		want    string
	}{
		{"string model", map[string]any{"model": "xopglm52"}, "xopglm52"},
		{"missing", map[string]any{}, ""},
		{"wrong type", map[string]any{"model": 42}, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := upstreamSlug(&proxySpec{payload: tc.payload}); got != tc.want {
				t.Fatalf("upstreamSlug() = %q, want %q", got, tc.want)
			}
		})
	}
}
