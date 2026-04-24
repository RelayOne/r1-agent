// Package provider — gemini.go
//
// Native Google Gemini provider. Text-in text-out, no tool-use —
// purpose-built for reviewer roles where the harness just needs
// the model to read prompt text and emit review text back.
//
// Speaks Google's generativelanguage.googleapis.com API directly
// via `:generateContent`. No LiteLLM, no OpenAI translation shim.
// Auth via x-goog-api-key header (NOT `Authorization: Bearer ...` —
// Google's OpenAI-compat endpoint rejects Bearer auth).
//
// Use via:
//   --reasoning-base-url gemini://
//   --reasoning-api-key <GOOGLE_API_KEY>
//   --reasoning-model gemini-3.1-pro-preview  (or 3-pro-preview,
//                                              2.5-pro, 2.5-flash, etc.)
//
// If --reasoning-api-key is omitted, reads GEMINI_API_KEY from env.
package provider

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	"github.com/ericmacdougall/stoke/internal/stream"
)

// GeminiProvider is a thin HTTP client for Google's Gemini API.
// Implements Provider for reviewer/judge roles. ChatStream is
// non-streaming here — we fall through to Chat and synthesize a
// single-block event, which is fine because reviewer callers
// only read the final text anyway.
type GeminiProvider struct {
	APIKey  string
	Model   string
	BaseURL string // defaults to https://generativelanguage.googleapis.com
	Timeout time.Duration
	HTTP    *http.Client
}

func NewGeminiProvider(apiKey, model string) *GeminiProvider {
	if apiKey == "" {
		apiKey = os.Getenv("GEMINI_API_KEY")
	}
	return &GeminiProvider{
		APIKey:  apiKey,
		Model:   model,
		BaseURL: "https://generativelanguage.googleapis.com",
		Timeout: 5 * time.Minute,
		HTTP:    &http.Client{Timeout: 5 * time.Minute},
	}
}

func (p *GeminiProvider) Name() string { return "gemini" }

// geminiContent is a single message in Gemini's content array.
// Role is "user" or "model"; system instructions go in a separate
// top-level field.
type geminiContent struct {
	Role  string       `json:"role,omitempty"`
	Parts []geminiPart `json:"parts"`
}

type geminiPart struct {
	Text string `json:"text"`
}

type geminiRequest struct {
	Contents          []geminiContent `json:"contents"`
	SystemInstruction *geminiContent  `json:"systemInstruction,omitempty"`
	GenerationConfig  *geminiGenCfg   `json:"generationConfig,omitempty"`
}

type geminiGenCfg struct {
	MaxOutputTokens int      `json:"maxOutputTokens,omitempty"`
	Temperature     *float64 `json:"temperature,omitempty"`
}

type geminiResponse struct {
	Candidates []struct {
		Content      geminiContent `json:"content"`
		FinishReason string        `json:"finishReason"`
	} `json:"candidates"`
	UsageMetadata struct {
		PromptTokenCount     int `json:"promptTokenCount"`
		CandidatesTokenCount int `json:"candidatesTokenCount"`
		TotalTokenCount      int `json:"totalTokenCount"`
	} `json:"usageMetadata"`
	Error *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
		Status  string `json:"status"`
	} `json:"error,omitempty"`
}

// Chat converts a stoke ChatRequest to Gemini's generateContent
// format, POSTs, and maps the response back.
func (p *GeminiProvider) Chat(req ChatRequest) (*ChatResponse, error) {
	if p.APIKey == "" {
		return nil, fmt.Errorf("gemini: no API key (set GEMINI_API_KEY or pass --reasoning-api-key)")
	}
	model := req.Model
	if model == "" {
		model = p.Model
	}
	if model == "" {
		return nil, fmt.Errorf("gemini: no model specified")
	}

	// Map stoke ChatRequest.Messages → Gemini contents.
	// ChatMessage.Content is json.RawMessage and may be either a
	// plain string ("hello") or an Anthropic-style block array
	// ([{type:"text", text:"hello"}]). Try both.
	contents := make([]geminiContent, 0, len(req.Messages))
	for _, m := range req.Messages {
		text := extractGeminiText(m.Content)
		if text == "" {
			continue
		}
		role := m.Role
		// Gemini uses "model" for assistant role.
		if role == "assistant" {
			role = "model"
		}
		if role != "user" && role != "model" {
			role = "user"
		}
		contents = append(contents, geminiContent{
			Role:  role,
			Parts: []geminiPart{{Text: text}},
		})
	}
	if len(contents) == 0 {
		return nil, fmt.Errorf("gemini: no usable messages in request")
	}

	body := geminiRequest{
		Contents: contents,
	}
	if req.System != "" {
		body.SystemInstruction = &geminiContent{
			Parts: []geminiPart{{Text: req.System}},
		}
	}
	// Gemini 3.x Pro models are reasoning models — they spend output
	// tokens on internal "thinking" BEFORE emitting any visible text.
	// A stoke reviewer call asking for 50 tokens of verdict on a
	// 50 KB plan will silently return empty because thinking eats
	// the whole budget. Floor the output ceiling high enough that
	// at least several KB of review text can land after thinking.
	// Callers that want a specific cap can still set req.MaxTokens
	// above this floor.
	const geminiReviewerFloor = 32000
	maxTok := req.MaxTokens
	if maxTok < geminiReviewerFloor {
		maxTok = geminiReviewerFloor
	}
	body.GenerationConfig = &geminiGenCfg{
		MaxOutputTokens: maxTok,
		Temperature:     req.Temperature,
	}

	payload, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("gemini: marshal: %w", err)
	}

	url := fmt.Sprintf("%s/v1beta/models/%s:generateContent", p.BaseURL, model)
	httpReq, err := http.NewRequest("POST", url, bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("gemini: new request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("x-goog-api-key", p.APIKey)

	resp, err := p.HTTP.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("gemini: http: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("gemini: read body: %w", err)
	}

	var parsed geminiResponse
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return nil, fmt.Errorf("gemini: decode: %w (body head: %s)", err, truncate(string(raw), 300))
	}
	if parsed.Error != nil {
		return nil, fmt.Errorf("gemini: api %d %s: %s",
			parsed.Error.Code, parsed.Error.Status, parsed.Error.Message)
	}
	if len(parsed.Candidates) == 0 {
		return nil, fmt.Errorf("gemini: no candidates returned (body head: %s)", truncate(string(raw), 300))
	}

	// Concatenate all text parts from the first candidate.
	var out bytes.Buffer
	for _, part := range parsed.Candidates[0].Content.Parts {
		out.WriteString(part.Text)
	}
	text := out.String()

	return &ChatResponse{
		Content:    []ResponseContent{{Type: "text", Text: text}},
		StopReason: parsed.Candidates[0].FinishReason,
		Usage: stream.TokenUsage{
			Input:  parsed.UsageMetadata.PromptTokenCount,
			Output: parsed.UsageMetadata.CandidatesTokenCount,
		},
	}, nil
}

// ChatStream here is non-streaming (Gemini's stream endpoint is
// separate and we don't need it for reviewer calls). We call Chat
// and emit a single content_block_delta equivalent so callers that
// expect streaming events don't hang.
func (p *GeminiProvider) ChatStream(req ChatRequest, onEvent func(stream.Event)) (*ChatResponse, error) {
	resp, err := p.Chat(req)
	if err != nil {
		return nil, err
	}
	if onEvent != nil && len(resp.Content) > 0 {
		onEvent(stream.Event{Type: "content_block_delta"})
	}
	return resp, nil
}

// extractGeminiText pulls the text out of a ChatMessage.Content
// RawMessage. Accepts either "string" or [{type:"text",text:"…"}]
// array form.
func extractGeminiText(content json.RawMessage) string {
	if len(content) == 0 {
		return ""
	}
	// Try plain string.
	var s string
	if json.Unmarshal(content, &s) == nil {
		return s
	}
	// Try content-block array.
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(content, &blocks) == nil {
		var out bytes.Buffer
		for _, b := range blocks {
			if b.Type == "text" || b.Text != "" {
				out.WriteString(b.Text)
			}
		}
		return out.String()
	}
	return ""
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}
