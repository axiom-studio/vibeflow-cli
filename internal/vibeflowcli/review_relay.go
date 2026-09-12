package vibeflowcli

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
	"time"
)

// An attempt-local model relay never accepts a destination, VibeFlow tool, or
// user credential from the child. Closing it revokes the child's model access.
func startReviewRelay(ctx context.Context, client *Client, provider, model string) (string, string, func(), error) {
	base, err := url.Parse(client.baseURL)
	if err != nil || base.User != nil || base.Host == "" || base.RawQuery != "" || base.Fragment != "" || (base.Scheme != "https" && !(base.Scheme == "http" && (base.Hostname() == "127.0.0.1" || base.Hostname() == "localhost" || base.Hostname() == "::1"))) {
		return "", "", nil, fmt.Errorf("invalid model gateway origin")
	}
	if strings.TrimSpace(model) == "" {
		return "", "", nil, fmt.Errorf("gateway reviews require an explicit --model")
	}
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return "", "", nil, err
	}
	random := make([]byte, 32)
	if _, err = rand.Read(random); err != nil {
		listener.Close()
		return "", "", nil, err
	}
	token := hex.EncodeToString(random)
	var requests atomic.Int64
	upstream := &http.Client{Timeout: 2 * time.Minute, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if key == "" {
			key = r.Header.Get("X-Api-Key")
		}
		if r.Host != listener.Addr().String() || subtle.ConstantTimeCompare([]byte(key), []byte(token)) != 1 || r.URL.RawQuery != "" || r.URL.RawPath != "" || r.Method != "POST" {
			http.Error(w, "Denied", http.StatusForbidden)
			return
		}
		allowed := provider == "claude" && (r.URL.Path == "/v1/messages" || r.URL.Path == "/v1/messages/count_tokens") || provider == "codex" && r.URL.Path == "/v1/responses"
		if !allowed {
			http.Error(w, "Model endpoint unavailable", http.StatusNotFound)
			return
		}
		if requests.Add(1) > 200 {
			http.Error(w, "Review model request limit reached", http.StatusTooManyRequests)
			return
		}
		data, err := io.ReadAll(io.LimitReader(r.Body, (8<<20)+1))
		if err != nil || len(data) > 8<<20 {
			http.Error(w, "Request too large", http.StatusRequestEntityTooLarge)
			return
		}
		var body map[string]json.RawMessage
		if json.Unmarshal(data, &body) != nil {
			http.Error(w, "Invalid model request", http.StatusBadRequest)
			return
		}
		// Provider-hosted tools and saved conversations bypass the child's local
		// execution boundary. Only tools executed back in the isolated CLI are
		// allowed through this model-only relay.
		for _, field := range []string{"mcp_servers", "previous_response_id", "conversation", "container"} {
			if _, ok := body[field]; ok {
				http.Error(w, "Remote execution or conversation reuse denied", http.StatusForbidden)
				return
			}
		}
		var declared []map[string]json.RawMessage
		if raw, ok := body["tools"]; ok && json.Unmarshal(raw, &declared) != nil {
			http.Error(w, "Invalid tools", http.StatusBadRequest)
			return
		}
		for _, tool := range declared {
			var kind, name string
			if raw, ok := tool["type"]; ok && json.Unmarshal(raw, &kind) != nil {
				http.Error(w, "Invalid tool type", http.StatusBadRequest)
				return
			}
			json.Unmarshal(tool["name"], &name)
			local := provider == "codex" && (kind == "function" || kind == "custom") && name != ""
			if provider == "claude" {
				_, schema := tool["input_schema"]
				local = (kind == "" || kind == "custom") && name != "" && schema
			}
			if !local {
				http.Error(w, "Provider-side tools denied", http.StatusForbidden)
				return
			}
		}
		// Pin the configured model, including provider-qualified gateway names.
		body["model"], _ = json.Marshal(model)
		data, _ = json.Marshal(body)
		request, err := http.NewRequestWithContext(r.Context(), http.MethodPost, strings.TrimRight(client.baseURL, "/")+"/rest/v1/llm-gateway"+r.URL.Path, bytes.NewReader(data))
		if err != nil {
			http.Error(w, "Gateway unavailable", http.StatusBadGateway)
			return
		}
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("x-axiom-api-key", client.token)
		for _, header := range []string{"Anthropic-Version", "Anthropic-Beta", "OpenAI-Beta", "Accept"} {
			if v := r.Header.Get(header); v != "" {
				request.Header.Set(header, v)
			}
		}
		response, err := upstream.Do(request)
		if err != nil {
			http.Error(w, "Gateway connection failed", http.StatusBadGateway)
			return
		}
		defer response.Body.Close()
		if response.StatusCode < 200 || response.StatusCode >= 300 {
			status := response.StatusCode
			if status >= 300 && status < 400 {
				status = http.StatusBadGateway
			}
			http.Error(w, "Gateway model request failed", status)
			return
		}
		w.Header().Set("Content-Type", response.Header.Get("Content-Type"))
		w.Header().Set("Cache-Control", "no-store")
		// Flush SSE incrementally. Neither upstream headers nor errors can leak
		// the supervisor key through a general reverse-proxy surface.
		buffer := make([]byte, 32<<10)
		remaining := int64(32 << 20)
		for remaining > 0 {
			n, readErr := response.Body.Read(buffer)
			if int64(n) > remaining {
				n = int(remaining)
			}
			if n > 0 {
				if _, err = w.Write(buffer[:n]); err != nil {
					return
				}
				remaining -= int64(n)
				if f, ok := w.(http.Flusher); ok {
					f.Flush()
				}
			}
			if readErr != nil {
				return
			}
		}
	})
	server := &http.Server{Handler: handler, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 30 * time.Second, IdleTimeout: 30 * time.Second, BaseContext: func(net.Listener) context.Context { return ctx }}
	go server.Serve(listener)
	return "http://" + listener.Addr().String(), token, func() { server.Close(); upstream.CloseIdleConnections() }, nil
}
