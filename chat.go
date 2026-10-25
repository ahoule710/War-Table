package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"
)

var anthropicURL = "https://api.anthropic.com/v1/messages"

type chatMsg struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatReq struct {
	System   string    `json:"system"`
	Messages []chatMsg `json:"messages"`
	Fast     bool      `json:"fast"`
}

var apiClient = &http.Client{Timeout: 5 * time.Minute}

func friendlyAPIError(status int, body []byte) string {
	var e struct {
		Error struct {
			Type    string `json:"type"`
			Message string `json:"message"`
		} `json:"error"`
	}
	_ = json.Unmarshal(body, &e)
	msg := strings.ToLower(e.Error.Message)
	switch {
	case status == 401:
		return "Your API key was rejected. Check it in Settings."
	case status == 403:
		return "This API key isn't allowed to do that. Check its permissions in the Anthropic Console."
	case strings.Contains(msg, "credit"):
		return "Your Anthropic API account is out of credits. Add credits in the Anthropic Console."
	case status == 404 || strings.Contains(msg, "model"):
		return "That model wasn't found. Check the model names in Settings > Advanced. (" + e.Error.Message + ")"
	case status == 429:
		return "Rate limited by the API. Give it a minute."
	case status == 529 || status >= 500:
		return "Anthropic's API is busy right now. Try again shortly."
	}
	if e.Error.Message != "" {
		return e.Error.Message
	}
	return fmt.Sprintf("The API returned status %d.", status)
}

func handleKeyTest(w http.ResponseWriter, r *http.Request) {
	key := app.apiKey()
	if key == "" {
		writeJSON(w, 400, map[string]any{"error": "No API key saved yet."})
		return
	}
	app.mu.Lock()
	model := app.settings.FastModel
	app.mu.Unlock()
	body, _ := json.Marshal(map[string]any{
		"model": model, "max_tokens": 8,
		"messages": []chatMsg{{Role: "user", Content: "Reply with: ready"}},
	})
	req, _ := http.NewRequest("POST", anthropicURL, bytes.NewReader(body))
	setHeaders(req, key)
	resp, err := apiClient.Do(req)
	if err != nil {
		writeJSON(w, 502, map[string]any{"error": "Couldn't reach api.anthropic.com. Check your internet connection."})
		return
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		writeJSON(w, 400, map[string]any{"error": friendlyAPIError(resp.StatusCode, b)})
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}

func setHeaders(req *http.Request, key string) {
	req.Header.Set("x-api-key", key)
	req.Header.Set("anthropic-version", "2023-06-01")
	req.Header.Set("content-type", "application/json")
}

// handleChat streams the answer back as newline-delimited JSON:
// {"t":"text","v":"..."} {"t":"status","v":"..."} {"t":"error","v":"..."} {"t":"done"}
func handleChat(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method", http.StatusMethodNotAllowed)
		return
	}
	var cr chatReq
	if err := json.NewDecoder(io.LimitReader(r.Body, 400*1024)).Decode(&cr); err != nil || len(cr.Messages) == 0 {
		writeJSON(w, 400, map[string]any{"error": "Empty question."})
		return
	}
	key := app.apiKey()
	if key == "" {
		writeJSON(w, 400, map[string]any{"error": "Add your Anthropic API key in Settings to use the assistant."})
		return
	}
	app.mu.Lock()
	model := app.settings.Model
	if cr.Fast {
		model = app.settings.FastModel
	}
	web := app.settings.WebSearch
	app.mu.Unlock()

	w.Header().Set("Content-Type", "application/x-ndjson")
	w.Header().Set("Cache-Control", "no-store")
	flusher, _ := w.(http.Flusher)
	send := func(t, v string) {
		b, _ := json.Marshal(map[string]string{"t": t, "v": v})
		_, _ = w.Write(append(b, '\n'))
		if flusher != nil {
			flusher.Flush()
		}
	}

	resp, err := callAnthropic(r, key, model, cr, web)
	if err == nil && resp.StatusCode == 400 && web {
		// Web search may not be enabled for this account; answer without it.
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if strings.Contains(strings.ToLower(string(b)), "tool") || strings.Contains(strings.ToLower(string(b)), "web_search") {
			log.Printf("web search rejected, retrying without: %s", string(b))
			send("status", "Web lookups aren't available on this key, answering from what I know")
			resp, err = callAnthropic(r, key, model, cr, false)
		} else {
			send("error", friendlyAPIError(400, b))
			return
		}
	}
	if err != nil {
		if r.Context().Err() != nil {
			return
		}
		send("error", "Couldn't reach api.anthropic.com. Check your internet connection.")
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		b, _ := io.ReadAll(resp.Body)
		send("error", friendlyAPIError(resp.StatusCode, b))
		return
	}

	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 64*1024), 4*1024*1024)
	textSeen := false
	for sc.Scan() {
		line := sc.Text()
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		var ev struct {
			Type         string `json:"type"`
			ContentBlock struct {
				Type string `json:"type"`
				Name string `json:"name"`
			} `json:"content_block"`
			Delta struct {
				Type       string `json:"type"`
				Text       string `json:"text"`
				StopReason string `json:"stop_reason"`
			} `json:"delta"`
			Error struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if json.Unmarshal([]byte(data), &ev) != nil {
			continue
		}
		switch ev.Type {
		case "content_block_start":
			switch ev.ContentBlock.Type {
			case "server_tool_use":
				send("status", "Searching the web")
			case "web_search_tool_result":
				send("status", "Reading what I found")
			case "text":
				if textSeen {
					send("text", "")
				}
			}
		case "content_block_delta":
			if ev.Delta.Type == "text_delta" && ev.Delta.Text != "" {
				textSeen = true
				send("text", ev.Delta.Text)
			}
		case "message_delta":
			if ev.Delta.StopReason == "max_tokens" {
				send("status", "Answer was cut short. Ask for less at once.")
			}
		case "error":
			send("error", "The answer was interrupted: "+ev.Error.Message)
			return
		}
	}
	if err := sc.Err(); err != nil && r.Context().Err() == nil {
		send("error", "The connection dropped mid-answer. Try again.")
		return
	}
	send("done", "")
}

func callAnthropic(r *http.Request, key, model string, cr chatReq, web bool) (*http.Response, error) {
	payload := map[string]any{
		"model":      model,
		"max_tokens": 1500,
		"system":     cr.System,
		"messages":   cr.Messages,
		"stream":     true,
	}
	if web {
		payload["tools"] = []map[string]any{{
			"type": "web_search_20250305", "name": "web_search", "max_uses": 3,
		}}
	}
	body, _ := json.Marshal(payload)
	req, err := http.NewRequestWithContext(r.Context(), "POST", anthropicURL, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	setHeaders(req, key)
	return apiClient.Do(req)
}
