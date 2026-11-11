package llm

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"unicode/utf8"
)

// ErrModelUnavailable is matched (via errors.Is) by every *ModelError. It
// means the provider rejected the configured model name: the model was
// retired, never existed, is misspelled, or is not enabled for this account.
var ErrModelUnavailable = errors.New("model unavailable")

// ModelError reports that a provider rejected the requested model. Its
// Error() text is written for an operator reading the UI: it says which model
// was refused and what to do about it, instead of echoing the raw upstream
// payload.
type ModelError struct {
	Provider string // provider id, e.g. "anthropic"
	Model    string // the model name that was rejected
	Status   int    // upstream HTTP status, if any
	Detail   string // upstream message, already passed through redactSecrets
}

func (e *ModelError) Error() string {
	switch Provider(e.Provider) {
	case ProviderOllama:
		return fmt.Sprintf("Ollama does not have model %q installed. Run `ollama pull %s`, or pick another model in AI settings (or set OLLAMA_MODEL).", e.Model, e.Model)
	case ProviderLMStudio:
		return fmt.Sprintf("LM Studio rejected model %q: it is probably not downloaded or loaded. Load it in LM Studio, or pick another model in AI settings (or set LMSTUDIO_MODEL).", e.Model)
	}
	return fmt.Sprintf("%s rejected model %q: it may be retired, misspelled, or not enabled for your account. "+
		"Pick another model in AI settings (or set %s).", providerDisplayName(e.Provider), e.Model, modelEnvVar(e.Provider))
}

// Is lets callers use errors.Is(err, ErrModelUnavailable).
func (e *ModelError) Is(target error) bool { return target == ErrModelUnavailable }

// APIError is any other non-2xx answer from a provider. Detail is redacted.
type APIError struct {
	Provider string
	Status   int
	Detail   string
}

func (e *APIError) Error() string {
	switch e.Status {
	case http.StatusUnauthorized, http.StatusForbidden:
		return fmt.Sprintf("%s rejected the API key (HTTP %d): %s", providerDisplayName(e.Provider), e.Status, e.Detail)
	case http.StatusTooManyRequests:
		return fmt.Sprintf("%s rate limit or quota exceeded (HTTP %d): %s", providerDisplayName(e.Provider), e.Status, e.Detail)
	}
	return fmt.Sprintf("%s error (HTTP %d): %s", providerDisplayName(e.Provider), e.Status, e.Detail)
}

// ResponseRefusedError means the provider answered successfully at the HTTP
// level but produced no usable documentation: the model refused, a safety
// filter blocked the prompt or the output, or the answer was empty. It is an
// error so callers (and the UI's localStorage cache) never treat it as docs.
type ResponseRefusedError struct {
	Provider string
	Model    string
	Reason   string // provider's reason code, e.g. "refusal", "content_filter", "SAFETY"
	Detail   string // refusal text from the model, if any (redacted, truncated)
}

func (e *ResponseRefusedError) Error() string {
	msg := fmt.Sprintf("%s model %s returned no documentation (reason: %s)", providerDisplayName(e.Provider), e.Model, e.Reason)
	if e.Detail != "" {
		msg += ": " + e.Detail
	}
	return msg + ". The request was declined or filtered; try again, or pick another model or provider."
}

// maxErrorDetail bounds how much upstream text is carried in an error.
const maxErrorDetail = 300

// cleanDetail redacts first and truncates second: truncating first could cut
// a credential in half and leave a prefix the patterns no longer recognise.
// Truncation happens on a UTF-8 rune boundary.
func cleanDetail(s string) string {
	s = redactSecrets(strings.TrimSpace(s))
	if len(s) <= maxErrorDetail {
		return s
	}
	cut := maxErrorDetail
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "..."
}

// upstreamError turns a provider's error answer into a *ModelError or an
// *APIError. model is the model a completion request used, or "" for
// list-models and other calls that do not name a model (those are never
// classified as model rejections). msg/code are whatever the provider's JSON
// error object carried (may be empty if the body was not JSON); body is the
// raw body used as a fallback. Upstream text is redacted because some
// providers echo a partial API key back in authentication errors.
func upstreamError(provider, model string, status int, code, msg string, body []byte) error {
	detail := strings.TrimSpace(msg)
	if detail == "" {
		detail = strings.TrimSpace(string(body))
	}
	if detail == "" {
		detail = http.StatusText(status)
	}
	detail = cleanDetail(detail)

	if looksLikeModelRejection(model, status, code, msg) {
		return &ModelError{Provider: provider, Model: model, Status: status, Detail: detail}
	}
	if status == 0 {
		status = http.StatusBadGateway
	}
	return &APIError{Provider: provider, Status: status, Detail: detail}
}

// looksLikeModelRejection recognises the shapes the supported providers use
// for "no such model" on a completion request:
//
//	OpenAI:    404 {"error":{"code":"model_not_found","message":"The model `x` does not exist ..."}}
//	Anthropic: 404 {"type":"error","error":{"type":"not_found_error","message":"model: x"}}
//	Gemini:    404 {"error":{"status":"NOT_FOUND","message":"models/x is not found for API version v1beta ..."}}
//	           400 "... model x is not supported for generateContent"
//	Ollama:    404 {"error":"model \"x\" not found, try pulling it first"}
//
// Apart from OpenAI's unambiguous model_not_found code, the upstream message
// must name the model that was sent, so an error about something else (a
// deprecated parameter, a quota, a wrong base URL answering 404) is not
// mislabelled as "pick another model". Auth, rate-limit and server errors
// are never model rejections.
func looksLikeModelRejection(model string, status int, code, msg string) bool {
	if model == "" {
		return false
	}
	switch {
	case status == http.StatusUnauthorized, status == http.StatusForbidden,
		status == http.StatusTooManyRequests, status >= 500:
		return false
	}
	c := strings.ToLower(code)
	if c == "model_not_found" {
		return true
	}
	m := strings.ToLower(msg)
	if !strings.Contains(m, strings.ToLower(model)) {
		return false
	}
	// A message about a request parameter that merely mentions the model.
	for _, p := range []string{"parameter", "argument", "param "} {
		if strings.Contains(m, p) {
			return false
		}
	}
	if status == http.StatusNotFound || c == "not_found_error" || c == "not_found" {
		return true
	}
	for _, phrase := range []string{
		"not found", "does not exist", "not supported for generatecontent",
		"has been deprecated", "has been retired", "was retired", "no longer available", "unknown model", "invalid model",
	} {
		if strings.Contains(m, phrase) {
			return true
		}
	}
	return false
}

// decodeErrorFields extracts code/message from the common provider error
// envelopes without needing per-provider structs:
//
//	{"error": {"message": "...", "code": "...", "type": "...", "status": "..."}}
//	{"error": "..."}
func decodeErrorFields(body []byte) (code, msg string) {
	var env struct {
		Error json.RawMessage `json:"error"`
	}
	if json.Unmarshal(body, &env) != nil || len(env.Error) == 0 {
		return "", ""
	}
	var s string
	if json.Unmarshal(env.Error, &s) == nil {
		return "", s
	}
	var obj struct {
		Message string          `json:"message"`
		Code    json.RawMessage `json:"code"`
		Type    string          `json:"type"`
		Status  string          `json:"status"`
	}
	if json.Unmarshal(env.Error, &obj) != nil {
		return "", ""
	}
	// OpenAI uses a string code, Gemini a numeric code plus a string status.
	var codeStr string
	_ = json.Unmarshal(obj.Code, &codeStr)
	switch {
	case codeStr != "":
		code = codeStr
	case obj.Type != "":
		code = obj.Type
	default:
		code = obj.Status
	}
	return code, obj.Message
}

func providerDisplayName(p string) string {
	switch Provider(p) {
	case ProviderOpenAI:
		return "OpenAI"
	case ProviderAnthropic:
		return "Anthropic"
	case ProviderGemini:
		return "Google Gemini"
	case ProviderOllama:
		return "Ollama"
	case ProviderLMStudio:
		return "LM Studio"
	}
	return p
}

func modelEnvVar(p string) string {
	switch Provider(p) {
	case ProviderOpenAI:
		return "OPENAI_MODEL"
	case ProviderAnthropic:
		return "ANTHROPIC_MODEL"
	case ProviderGemini:
		return "GEMINI_MODEL"
	case ProviderOllama:
		return "OLLAMA_MODEL"
	case ProviderLMStudio:
		return "LMSTUDIO_MODEL"
	}
	return "the provider's *_MODEL variable"
}
