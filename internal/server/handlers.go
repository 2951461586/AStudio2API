package server

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"time"

	"astudio2api/internal/store"
)

// readJSONBody reads, size-limits and decodes a JSON request body.
func readJSONBody(r *http.Request) (map[string]any, []byte, error) {
	raw, err := io.ReadAll(io.LimitReader(r.Body, maxRequestBody))
	if err != nil {
		return nil, nil, err
	}
	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil, raw, err
	}
	return payload, raw, nil
}

// handleChatCompletions serves POST /v1/chat/completions.
func (s *Server) handleChatCompletions(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeOpenAIError(w, http.StatusMethodNotAllowed, "invalid_request_error", "Use POST.")
		return
	}
	payload, _, err := readJSONBody(r)
	if err != nil {
		writeOpenAIError(w, http.StatusBadRequest, "invalid_request_error", "Request body must be valid JSON.")
		return
	}
	requested, _ := payload["model"].(string)
	slug := s.registry.SlugFor(requested)
	if slug == "" {
		writeOpenAIError(w, http.StatusBadRequest, "invalid_request_error", "`model` is required.")
		return
	}
	payload["model"] = slug

	body, err := json.Marshal(payload)
	if err != nil {
		writeOpenAIError(w, http.StatusBadRequest, "invalid_request_error", "Could not encode request.")
		return
	}

	spec := &proxySpec{
		path:      "chat/completions",
		payload:   payload,
		body:      body,
		requested: requested,
		stream:    boolValue(payload["stream"]),
		failover:  true,
	}
	s.dispatch(w, r, spec)
}

// handleResponses serves POST /v1/responses (the OpenAI Responses API used by
// Codex and by the AStudio kernel itself).
func (s *Server) handleResponses(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeOpenAIError(w, http.StatusMethodNotAllowed, "invalid_request_error", "Use POST.")
		return
	}
	payload, _, err := readJSONBody(r)
	if err != nil {
		writeOpenAIError(w, http.StatusBadRequest, "invalid_request_error", "Request body must be valid JSON.")
		return
	}
	requested, _ := payload["model"].(string)
	slug := s.registry.SlugFor(requested)
	if slug == "" {
		writeOpenAIError(w, http.StatusBadRequest, "invalid_request_error", "`model` is required.")
		return
	}
	payload["model"] = slug

	body, err := json.Marshal(payload)
	if err != nil {
		writeOpenAIError(w, http.StatusBadRequest, "invalid_request_error", "Could not encode request.")
		return
	}

	spec := &proxySpec{
		path:      "responses",
		payload:   payload,
		body:      body,
		requested: requested,
		stream:    boolValue(payload["stream"]),
		failover:  true,
	}
	s.dispatch(w, r, spec)
}

// dispatch runs one prepared upstream call with admission control, timeouts and
// error reporting.
func (s *Server) dispatch(w http.ResponseWriter, r *http.Request, spec *proxySpec) {
	started := time.Now()

	release, err := s.acquire(r.Context())
	if err != nil {
		writeOpenAIError(w, http.StatusRequestTimeout, "timeout_error", "Request cancelled while waiting for capacity.")
		return
	}
	defer release()

	ctx := r.Context()
	if !spec.stream {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, time.Duration(s.store.Settings().RequestTimeoutS)*time.Second)
		defer cancel()
	}

	result, err := s.forward(ctx, spec)
	if err != nil {
		s.recordFailure(spec, currentKey(r), started, err)
		writeOpenAIError(w, http.StatusBadGateway, "upstream_error", err.Error())
		return
	}
	s.relay(w, spec, result, currentKey(r), started)
}

// recordFailure logs a request that never reached a usable upstream response.
func (s *Server) recordFailure(spec *proxySpec, key *store.APIKey, started time.Time, err error) {
	entry := &store.LogEntry{
		Time:          started.Unix(),
		Model:         spec.requested,
		UpstreamModel: upstreamSlug(spec),
		Stream:        spec.stream,
		Status:        http.StatusBadGateway,
		DurationMs:    time.Since(started).Milliseconds(),
		Error:         truncate(err.Error(), 500),
	}
	if key != nil {
		entry.KeyNote = key.Note
	}
	s.store.RecordRequest(entry)
}

func boolValue(v any) bool {
	b, _ := v.(bool)
	return b
}
