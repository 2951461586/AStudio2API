package server

import (
	"encoding/json"
	"reflect"
	"testing"
)

// normalizeJSON round-trips a value through encoding/json so that the many
// equivalent Go representations (json.RawMessage, []string, int vs float64)
// compare structurally rather than by concrete type.
func normalizeJSON(t *testing.T, v any) any {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var out any
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	return out
}

func equalJSON(t *testing.T, got, want any) {
	t.Helper()
	g, w := normalizeJSON(t, got), normalizeJSON(t, want)
	if !reflect.DeepEqual(g, w) {
		t.Fatalf("mismatch\n got: %#v\nwant: %#v", g, w)
	}
}

func ptrFloat(v float64) *float64 { return &v }

func TestAnthropicSystemText(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want string
	}{
		{"plain string", `"You are helpful."`, "You are helpful."},
		{"text blocks joined", `[{"type":"text","text":"a"},{"type":"text","text":"b"}]`, "a\n\nb"},
		{"non-text blocks skipped", `[{"type":"text","text":"a"},{"type":"image","text":"ignored"}]`, "a"},
		{"empty text skipped", `[{"type":"text","text":""}]`, ""},
		{"empty raw", ``, ""},
		{"malformed", `{`, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := anthropicSystemText(json.RawMessage(tc.raw)); got != tc.want {
				t.Fatalf("anthropicSystemText(%s) = %q, want %q", tc.raw, got, tc.want)
			}
		})
	}
}

func TestConvertAnthropicMessagePlainString(t *testing.T) {
	out, err := convertAnthropicMessage(anthropicMsg{Role: "user", Content: json.RawMessage(`"hello"`)})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	equalJSON(t, out, []any{map[string]any{"role": "user", "content": "hello"}})
}

func TestConvertAnthropicMessageBlocks(t *testing.T) {
	tests := []struct {
		name    string
		role    string
		content string
		want    any
	}{
		{
			name:    "user text and base64 image",
			role:    "user",
			content: `[{"type":"text","text":"look"},{"type":"image","source":{"type":"base64","media_type":"image/png","data":"AAAA"}}]`,
			want: []any{map[string]any{"role": "user", "content": []any{
				map[string]any{"type": "text", "text": "look"},
				map[string]any{"type": "image_url", "image_url": map[string]any{"url": "data:image/png;base64,AAAA"}},
			}}},
		},
		{
			name:    "assistant text plus tool call",
			role:    "assistant",
			content: `[{"type":"text","text":"checking"},{"type":"tool_use","id":"t1","name":"get_weather","input":{"city":"SF"}}]`,
			want: []any{map[string]any{
				"role":    "assistant",
				"content": []any{map[string]any{"type": "text", "text": "checking"}},
				"tool_calls": []any{map[string]any{
					"id": "t1", "type": "function",
					"function": map[string]any{"name": "get_weather", "arguments": `{"city":"SF"}`},
				}},
			}},
		},
		{
			name:    "assistant tool call only has empty content",
			role:    "assistant",
			content: `[{"type":"tool_use","id":"t2","name":"ping","input":{}}]`,
			want: []any{map[string]any{
				"role":    "assistant",
				"content": "",
				"tool_calls": []any{map[string]any{
					"id": "t2", "type": "function",
					"function": map[string]any{"name": "ping", "arguments": `{}`},
				}},
			}},
		},
		{
			name:    "user tool result becomes a tool message",
			role:    "user",
			content: `[{"type":"tool_result","tool_use_id":"t1","content":"22C"}]`,
			want: []any{map[string]any{
				"role": "tool", "tool_call_id": "t1", "content": "22C",
			}},
		},
		{
			name:    "tool result plus following text",
			role:    "user",
			content: `[{"type":"tool_result","tool_use_id":"t1","content":[{"type":"text","text":"a"},{"type":"text","text":"b"}]},{"type":"text","text":"thanks"}]`,
			want: []any{
				map[string]any{"role": "tool", "tool_call_id": "t1", "content": "a\nb"},
				map[string]any{"role": "user", "content": []any{map[string]any{"type": "text", "text": "thanks"}}},
			},
		},
		{
			name:    "empty block list yields empty message",
			role:    "user",
			content: `[]`,
			want:    []any{map[string]any{"role": "user", "content": ""}},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			out, err := convertAnthropicMessage(anthropicMsg{Role: tc.role, Content: json.RawMessage(tc.content)})
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			equalJSON(t, out, tc.want)
		})
	}
}

func TestConvertAnthropicMessageInvalidContent(t *testing.T) {
	if out, err := convertAnthropicMessage(anthropicMsg{Role: "user", Content: json.RawMessage(`42`)}); err == nil {
		t.Fatalf("expected an error for numeric content, got %#v", out)
	}
}

func TestAnthropicImageURL(t *testing.T) {
	tests := []struct {
		name  string
		block map[string]any
		want  string
	}{
		{"base64", map[string]any{"source": map[string]any{"type": "base64", "media_type": "image/png", "data": "ZZZ"}}, "data:image/png;base64,ZZZ"},
		{"url", map[string]any{"source": map[string]any{"type": "url", "url": "https://x/y.png"}}, "https://x/y.png"},
		{"missing source", map[string]any{}, ""},
		{"unknown type", map[string]any{"source": map[string]any{"type": "file"}}, ""},
		{"base64 without data", map[string]any{"source": map[string]any{"type": "base64", "media_type": "image/png"}}, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := anthropicImageURL(tc.block); got != tc.want {
				t.Fatalf("anthropicImageURL() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestAnthropicToolResultText(t *testing.T) {
	tests := []struct {
		name    string
		content any
		want    string
	}{
		{"string", "done", "done"},
		{"blocks", []any{map[string]any{"type": "text", "text": "a"}, map[string]any{"type": "text", "text": "b"}}, "a\nb"},
		{"blocks without text skipped", []any{map[string]any{"type": "image"}}, ""},
		{"other value json encoded", map[string]any{"ok": true}, `{"ok":true}`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := anthropicToolResultText(tc.content); got != tc.want {
				t.Fatalf("anthropicToolResultText() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestAnthropicToolChoice(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want any
	}{
		{"auto", `{"type":"auto"}`, "auto"},
		{"any maps to required", `{"type":"any"}`, "required"},
		{"none", `{"type":"none"}`, "none"},
		{"named tool", `{"type":"tool","name":"get_weather"}`, map[string]any{"type": "function", "function": map[string]any{"name": "get_weather"}}},
		{"tool without name", `{"type":"tool"}`, nil},
		{"unknown type", `{"type":"weird"}`, nil},
		{"empty", ``, nil},
		{"malformed", `{`, nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := anthropicToolChoice(json.RawMessage(tc.raw))
			if tc.want == nil {
				if got != nil {
					t.Fatalf("anthropicToolChoice(%s) = %#v, want nil", tc.raw, got)
				}
				return
			}
			equalJSON(t, got, tc.want)
		})
	}
}

func TestToChatPayload(t *testing.T) {
	t.Run("requires a model", func(t *testing.T) {
		if out, err := toChatPayload(&anthropicRequest{}, ""); err == nil {
			t.Fatalf("expected an error when the slug is empty, got %#v", out)
		}
	})

	t.Run("minimal request", func(t *testing.T) {
		got, err := toChatPayload(&anthropicRequest{
			Messages: []anthropicMsg{{Role: "user", Content: json.RawMessage(`"hi"`)}},
		}, "xopglm52")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		equalJSON(t, got, map[string]any{
			"model":    "xopglm52",
			"stream":   false,
			"messages": []any{map[string]any{"role": "user", "content": "hi"}},
		})
	})

	t.Run("system and sampling options", func(t *testing.T) {
		got, err := toChatPayload(&anthropicRequest{
			Model:         "GLM-5.2",
			MaxTokens:     256,
			System:        json.RawMessage(`[{"type":"text","text":"be terse"}]`),
			Messages:      []anthropicMsg{{Role: "user", Content: json.RawMessage(`"hi"`)}},
			Temperature:   ptrFloat(0.2),
			TopP:          ptrFloat(0.9),
			StopSequences: []string{"STOP", "END"},
			Stream:        true,
		}, "xopglm52")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		equalJSON(t, got, map[string]any{
			"model":          "xopglm52",
			"stream":         true,
			"max_tokens":     256,
			"temperature":    0.2,
			"top_p":          0.9,
			"stop":           []string{"STOP", "END"},
			"stream_options": map[string]any{"include_usage": true},
			"messages": []any{
				map[string]any{"role": "system", "content": "be terse"},
				map[string]any{"role": "user", "content": "hi"},
			},
		})
	})

	t.Run("tools and tool choice", func(t *testing.T) {
		got, err := toChatPayload(&anthropicRequest{
			Messages: []anthropicMsg{{Role: "user", Content: json.RawMessage(`"weather?"`)}},
			Tools: []anthropicTool{
				{Name: "get_weather", Description: "look up", InputSchema: json.RawMessage(`{"type":"object","properties":{"city":{"type":"string"}}}`)},
				{Name: "no_schema"},
			},
			ToolChoice: json.RawMessage(`{"type":"tool","name":"get_weather"}`),
		}, "xopglm52")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		equalJSON(t, got, map[string]any{
			"model":  "xopglm52",
			"stream": false,
			"messages": []any{
				map[string]any{"role": "user", "content": "weather?"},
			},
			"tools": []any{
				map[string]any{"type": "function", "function": map[string]any{
					"name": "get_weather", "description": "look up",
					"parameters": map[string]any{"type": "object", "properties": map[string]any{"city": map[string]any{"type": "string"}}},
				}},
				map[string]any{"type": "function", "function": map[string]any{
					"name": "no_schema", "description": "",
					"parameters": map[string]any{"type": "object", "properties": map[string]any{}},
				}},
			},
			"tool_choice": map[string]any{"type": "function", "function": map[string]any{"name": "get_weather"}},
		})
	})
}

func TestMapStopReason(t *testing.T) {
	tests := []struct {
		in       string
		fallback string
		want     string
	}{
		{"stop", "end_turn", "end_turn"},
		{"length", "end_turn", "max_tokens"},
		{"tool_calls", "end_turn", "tool_use"},
		{"function_call", "end_turn", "tool_use"},
		{"content_filter", "end_turn", "stop_sequence"},
		{"", "end_turn", "end_turn"},
		{"", "max_tokens", "max_tokens"},
		{"mystery", "end_turn", "end_turn"},
	}
	for _, tc := range tests {
		if got := mapStopReason(tc.in, tc.fallback); got != tc.want {
			t.Fatalf("mapStopReason(%q, %q) = %q, want %q", tc.in, tc.fallback, got, tc.want)
		}
	}
}

func TestRawJSONObject(t *testing.T) {
	if got := rawJSONObject("   "); !reflect.DeepEqual(got, map[string]any{}) {
		t.Fatalf("blank input = %#v", got)
	}
	if got := rawJSONObject("not json"); !reflect.DeepEqual(got, map[string]any{}) {
		t.Fatalf("malformed input = %#v", got)
	}
	equalJSON(t, rawJSONObject(`{"a":1}`), map[string]any{"a": 1})
	equalJSON(t, rawJSONObject(`[1,2]`), []any{1, 2})
}

func TestMapAndStringField(t *testing.T) {
	m := map[string]any{"nested": map[string]any{"k": "v"}, "s": "text", "n": 5}
	if got := mapField(m, "nested")["k"]; got != "v" {
		t.Fatalf("mapField(nested) = %v", got)
	}
	if got := mapField(m, "missing"); len(got) != 0 {
		t.Fatalf("mapField(missing) = %#v, want empty", got)
	}
	if got := mapField(m, "s"); len(got) != 0 {
		t.Fatalf("mapField(non-map) = %#v, want empty", got)
	}
	if got := mapField(nil, "x"); len(got) != 0 {
		t.Fatalf("mapField(nil) = %#v, want empty", got)
	}
	if got := stringField(m, "s"); got != "text" {
		t.Fatalf("stringField(s) = %q", got)
	}
	if got := stringField(m, "n"); got != "" {
		t.Fatalf("stringField(non-string) = %q", got)
	}
	if got := stringField(nil, "s"); got != "" {
		t.Fatalf("stringField(nil) = %q", got)
	}
}
