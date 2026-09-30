package server

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"astudio2api/internal/store"
)

const maxRequestBody = 64 << 20 // 64 MiB, matches the desktop app's attachment ceiling

// upstreamResult bundles a live upstream response with the account that served it.
type upstreamResult struct {
	resp    *http.Response
	account *store.Account
	slug    string
}

// proxySpec describes one downstream request to replay upstream.
type proxySpec struct {
	path      string // upstream sub path, e.g. "chat/completions"
	payload   map[string]any
	body      []byte
	requested string // model name the client asked for
	stream    bool
	failover  bool // whether another account may serve this request
}

// acquire blocks until the concurrency budget allows another upstream call.
func (s *Server) acquire(ctx context.Context) (func(), error) {
	s.mu.RLock()
	admission := s.admission
	s.mu.RUnlock()
	select {
	case admission <- struct{}{}:
		return func() { <-admission }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// forward performs the upstream call with token refresh, failover and bounded
// retries. The caller owns the returned response body.
func (s *Server) forward(ctx context.Context, spec *proxySpec) (*upstreamResult, error) {
	skip := map[string]bool{}
	var lastErr error

	for attempt := 0; attempt < 3; attempt++ {
		account, err := s.pickAccount(ctx, skip)
		if err != nil {
			if lastErr != nil {
				return nil, fmt.Errorf("%w (last upstream error: %v)", err, lastErr)
			}
			return nil, err
		}

		resp, err := s.doUpstream(ctx, account, spec)
		if err != nil {
			lastErr = err
			s.markAccountFailed(account.ID, err)
			skip[account.ID] = true
			continue
		}

		if resp.StatusCode == http.StatusUnauthorized {
			body := drain(resp)
			_ = body
			if refreshErr := s.RefreshAccount(ctx, account.ID); refreshErr == nil {
				skip[account.ID] = true
				retry, retryErr := s.forwardWithAccount(ctx, account.ID, spec)
				if retryErr == nil {
					return retry, nil
				}
				lastErr = retryErr
				skip[account.ID] = true
				continue
			}
			lastErr = errors.New("upstream rejected the model credential (HTTP 401)")
			s.markAccountFailed(account.ID, lastErr)
			skip[account.ID] = true
			continue
		}

		if spec.failover && (resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500) {
			body := drain(resp)
			lastErr = fmt.Errorf("upstream returned HTTP %d: %s", resp.StatusCode, truncate(string(body), 300))
			s.markAccountFailed(account.ID, lastErr)
			skip[account.ID] = true
			continue
		}

		return &upstreamResult{resp: resp, account: account, slug: spec.path}, nil
	}
	if lastErr == nil {
		lastErr = errors.New("upstream request failed")
	}
	return nil, lastErr
}

func (s *Server) forwardWithAccount(ctx context.Context, id string, spec *proxySpec) (*upstreamResult, error) {
	var account *store.Account
	for _, a := range s.store.Accounts() {
		if a.ID == id {
			account = a
			break
		}
	}
	if account == nil {
		return nil, errors.New("account disappeared during retry")
	}
	resp, err := s.doUpstream(ctx, account, spec)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 400 {
		body := drain(resp)
		return nil, fmt.Errorf("upstream returned HTTP %d: %s", resp.StatusCode, truncate(string(body), 300))
	}
	return &upstreamResult{resp: resp, account: account, slug: spec.path}, nil
}

func (s *Server) doUpstream(ctx context.Context, account *store.Account, spec *proxySpec) (*http.Response, error) {
	client := s.Client()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, client.InferenceURL(spec.path), bytes.NewReader(spec.body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+account.ModelBearer)
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("clientType", "21")
	req.Header.Set("User-Agent", "AStudio2API/"+s.Version)
	if uid := strings.TrimSpace(account.UID); uid != "" && uid != "0" {
		req.Header.Set("uid", uid)
	}
	return client.HTTP.Do(req)
}

func drain(resp *http.Response) []byte {
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	return body
}

// relay proxies an upstream response to the client, rewriting the model name
// and folding usage into the request log.
func (s *Server) relay(w http.ResponseWriter, spec *proxySpec, result *upstreamResult, key *store.APIKey, started time.Time) {
	defer result.resp.Body.Close()

	logEntry := &store.LogEntry{
		Time:          started.Unix(),
		Model:         spec.requested,
		UpstreamModel: upstreamSlug(spec),
		Stream:        spec.stream,
		Status:        result.resp.StatusCode,
	}
	if key != nil {
		logEntry.KeyNote = key.Note
	}
	if result.account != nil {
		logEntry.Account = firstNonEmpty(result.account.Nickname, result.account.AccountID, result.account.ID)
	}

	if result.resp.StatusCode >= 400 {
		body, _ := io.ReadAll(io.LimitReader(result.resp.Body, 4<<20))
		logEntry.Error = truncate(string(body), 500)
		logEntry.DurationMs = time.Since(started).Milliseconds()
		s.store.RecordRequest(logEntry)
		copyHeaders(w.Header(), result.resp.Header)
		w.Header().Set("Content-Type", contentTypeOr(result.resp, "application/json; charset=utf-8"))
		w.WriteHeader(result.resp.StatusCode)
		_, _ = w.Write(body)
		return
	}

	copyHeaders(w.Header(), result.resp.Header)
	if spec.stream {
		w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Accel-Buffering", "no")
	}
	w.WriteHeader(result.resp.StatusCode)

	flusher, _ := w.(http.Flusher)
	idle := time.Duration(s.store.Settings().IdleTimeoutS) * time.Second
	if spec.stream {
		s.streamRelay(w, flusher, result, spec, logEntry, started, idle)
	} else {
		s.bufferRelay(w, result, spec, logEntry, started, idle)
	}

	logEntry.DurationMs = time.Since(started).Milliseconds()
	s.store.RecordRequest(logEntry)
	if result.account != nil {
		s.markAccountUsed(result.account.ID)
	}
}

func (s *Server) streamRelay(w http.ResponseWriter, flusher http.Flusher, result *upstreamResult, spec *proxySpec, entry *store.LogEntry, started time.Time, idle time.Duration) {
	reader := newIdleReader(result.resp.Body, idle)
	defer reader.stop()
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 0, 64<<10), 8<<20)

	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()
	done := make(chan struct{})
	go func() {
		for {
			select {
			case <-done:
				return
			case <-heartbeat.C:
				if reader.idleFor() >= 15*time.Second {
					_, _ = io.WriteString(w, ": ping\n\n")
					if flusher != nil {
						flusher.Flush()
					}
				}
			}
		}
	}()

	for scanner.Scan() {
		line := scanner.Bytes()
		out := line
		if rest, ok := bytes.CutPrefix(line, []byte("data:")); ok {
			payload := bytes.TrimSpace(rest)
			if len(payload) > 0 && !bytes.Equal(payload, []byte("[DONE]")) {
				if rewritten, ok := rewriteChunk(payload, spec.requested, entry, started); ok {
					out = append([]byte("data: "), rewritten...)
				}
			}
		}
		if _, err := w.Write(out); err != nil {
			return
		}
		if _, err := w.Write([]byte("\n")); err != nil {
			return
		}
		if flusher != nil {
			flusher.Flush()
		}
	}
	close(done)
	if err := scanner.Err(); err != nil && !errors.Is(err, io.EOF) {
		if entry.Error == "" {
			entry.Error = truncate(err.Error(), 500)
		}
	}
}

func (s *Server) bufferRelay(w http.ResponseWriter, result *upstreamResult, spec *proxySpec, entry *store.LogEntry, started time.Time, idle time.Duration) {
	reader := newIdleReader(result.resp.Body, idle)
	defer reader.stop()
	body, err := io.ReadAll(reader)
	if err != nil {
		entry.Error = truncate(err.Error(), 500)
		return
	}
	if len(body) > 0 {
		var payload map[string]any
		if err := json.Unmarshal(body, &payload); err == nil {
			applyUsage(payload, entry)
			if spec.requested != "" {
				payload["model"] = spec.requested
			}
			if rewritten, err := json.Marshal(payload); err == nil {
				body = rewritten
			}
		}
	}
	_, _ = w.Write(body)
	if entry.FirstTokenMs == 0 {
		entry.FirstTokenMs = time.Since(started).Milliseconds()
	}
}

// rewriteChunk rewrites the model field of one streaming chunk and folds any
// usage/usage-delta it carries into the log entry.
func rewriteChunk(payload []byte, requested string, entry *store.LogEntry, started time.Time) ([]byte, bool) {
	var decoded map[string]any
	if err := json.Unmarshal(payload, &decoded); err != nil {
		return nil, false
	}
	if usage, ok := decoded["usage"].(map[string]any); ok {
		foldUsageMap(usage, entry)
	}
	if resp, ok := decoded["response"].(map[string]any); ok {
		if usage, ok := resp["usage"].(map[string]any); ok {
			foldUsageMap(usage, entry)
		}
		if requested != "" {
			resp["model"] = requested
		}
	}
	if hasContentDelta(decoded) && entry.FirstTokenMs == 0 {
		entry.FirstTokenMs = time.Since(started).Milliseconds()
	}
	if requested != "" {
		if _, ok := decoded["model"]; ok {
			decoded["model"] = requested
		}
	}
	out, err := json.Marshal(decoded)
	if err != nil {
		return nil, false
	}
	return out, true
}

// hasContentDelta reports whether a chunk carries actual model output.
func hasContentDelta(chunk map[string]any) bool {
	if choices, ok := chunk["choices"].([]any); ok {
		for _, c := range choices {
			cm, ok := c.(map[string]any)
			if !ok {
				continue
			}
			if delta, ok := cm["delta"].(map[string]any); ok {
				if s, ok := delta["content"].(string); ok && s != "" {
					return true
				}
				if _, ok := delta["reasoning_content"]; ok {
					return true
				}
				if _, ok := delta["tool_calls"]; ok {
					return true
				}
			}
		}
	}
	if t, ok := chunk["type"].(string); ok {
		switch t {
		case "response.output_text.delta", "response.reasoning_summary_text.delta", "response.function_call_arguments.delta":
			return true
		}
	}
	return false
}

// applyUsage extracts usage from a non-streaming body.
func applyUsage(payload map[string]any, entry *store.LogEntry) {
	usage, ok := payload["usage"].(map[string]any)
	if !ok {
		return
	}
	foldUsageMap(usage, entry)
}

// foldUsageMap normalises both the Chat Completions and Responses usage shapes.
func foldUsageMap(usage map[string]any, entry *store.LogEntry) {
	if v := intField(usage, "prompt_tokens", "input_tokens"); v > 0 {
		entry.PromptTokens = v
	}
	if v := intField(usage, "completion_tokens", "output_tokens"); v > 0 {
		entry.CompletionTokens = v
	}
	for _, key := range []string{"prompt_tokens_details", "input_tokens_details"} {
		if details, ok := usage[key].(map[string]any); ok {
			if v := intField(details, "cached_tokens"); v > 0 {
				entry.CachedTokens = v
			}
		}
	}
	for _, key := range []string{"completion_tokens_details", "output_tokens_details"} {
		if details, ok := usage[key].(map[string]any); ok {
			if v := intField(details, "reasoning_tokens"); v > 0 {
				entry.ReasoningTokens = v
			}
		}
	}
	if v := intField(usage, "cached_tokens"); v > 0 && entry.CachedTokens == 0 {
		entry.CachedTokens = v
	}
	if v := intField(usage, "reasoning_tokens"); v > 0 && entry.ReasoningTokens == 0 {
		entry.ReasoningTokens = v
	}
}

func intField(m map[string]any, keys ...string) int64 {
	for _, k := range keys {
		switch v := m[k].(type) {
		case float64:
			if v > 0 {
				return int64(v)
			}
		case json.Number:
			if n, err := v.Int64(); err == nil && n > 0 {
				return n
			}
		case int64:
			if v > 0 {
				return v
			}
		case int:
			if v > 0 {
				return int64(v)
			}
		}
	}
	return 0
}

func upstreamSlug(spec *proxySpec) string {
	if v, ok := spec.payload["model"].(string); ok {
		return v
	}
	return ""
}

func copyHeaders(dst, src http.Header) {
	for k, vals := range src {
		switch strings.ToLower(k) {
		case "content-length", "connection", "transfer-encoding", "keep-alive", "content-type":
			continue
		}
		for _, v := range vals {
			dst.Add(k, v)
		}
	}
}

// contentTypeOr returns the upstream content type, but never a browser-renderable
// HTML type: this endpoint relays third-party bodies to arbitrary clients, so
// HTML is downgraded to JSON to avoid reflected-content surprises.
func contentTypeOr(resp *http.Response, fallback string) string {
	ct := resp.Header.Get("Content-Type")
	if ct == "" {
		return fallback
	}
	lower := strings.ToLower(ct)
	if strings.Contains(lower, "text/html") || strings.Contains(lower, "application/xhtml") {
		return "application/json; charset=utf-8"
	}
	return ct
}

// idleReader aborts a body read that stalls for longer than the idle window.
type idleReader struct {
	rc      io.ReadCloser
	timeout time.Duration
	timer   *time.Timer
	last    time.Time
	mu      chan struct{}
}

func newIdleReader(rc io.ReadCloser, timeout time.Duration) *idleReader {
	r := &idleReader{rc: rc, timeout: timeout, last: time.Now(), mu: make(chan struct{}, 1)}
	r.mu <- struct{}{}
	if timeout > 0 {
		r.timer = time.AfterFunc(timeout, func() { _ = rc.Close() })
	}
	return r
}

func (r *idleReader) Read(p []byte) (int, error) {
	n, err := r.rc.Read(p)
	<-r.mu
	r.last = time.Now()
	if r.timer != nil {
		r.timer.Reset(r.timeout)
	}
	r.mu <- struct{}{}
	return n, err
}

func (r *idleReader) idleFor() time.Duration {
	<-r.mu
	d := time.Since(r.last)
	r.mu <- struct{}{}
	return d
}

func (r *idleReader) stop() {
	<-r.mu
	if r.timer != nil {
		r.timer.Stop()
	}
	r.mu <- struct{}{}
}
