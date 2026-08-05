package llm

import "regexp"

// redactPatterns catch the secret shapes most likely to appear verbatim in a
// README, Dockerfile, or entry-point file: cloud provider keys with a
// recognisable prefix, private key blocks, and generic
// "<word like secret/token/password> = <value>" assignments across the
// languages the agent recognises as entry points (Go, Python, JS/TS, Ruby,
// PHP, Java/C#, shell/.env style, and Dockerfile ENV/ARG).
//
// This is a best-effort net, not a guarantee: source code and Docker layers
// are exactly where hardcoded credentials end up in the wild, and unlike the
// agent's env-var collection (names only, deliberately) this content is
// injected into an LLM prompt and sent to whichever provider is configured -
// which defaults to a cloud API, not a local one. Catching the common,
// recognisable shapes is worth doing even though it can't catch everything.
var redactPatterns = []*regexp.Regexp{
	// Cloud / vendor keys with a distinctive prefix.
	regexp.MustCompile(`\bAKIA[0-9A-Z]{16}\b`),                             // AWS access key ID
	regexp.MustCompile(`\bASIA[0-9A-Z]{16}\b`),                             // AWS STS temporary key
	regexp.MustCompile(`\bAIza[0-9A-Za-z_\-]{35}\b`),                       // Google API key
	regexp.MustCompile(`\bgh[pousr]_[0-9A-Za-z]{36,255}\b`),                // GitHub tokens
	regexp.MustCompile(`\bxox[baprs]-[0-9A-Za-z-]{10,}\b`),                 // Slack tokens
	regexp.MustCompile(`\bsk-[A-Za-z0-9]{20,}\b`),                          // OpenAI-style API key
	regexp.MustCompile(`\bsk-ant-[A-Za-z0-9\-_]{20,}\b`),                   // Anthropic API key
	regexp.MustCompile(`\b(?:sk|rk)_(?:live|test)_[A-Za-z0-9]{10,}\b`),     // Stripe secret/restricted key
	regexp.MustCompile(`\bSK[0-9a-f]{32}\b`),                               // Twilio API key
	regexp.MustCompile(`\bSG\.[A-Za-z0-9_\-]{20,}\.[A-Za-z0-9_\-]{20,}\b`), // SendGrid API key
	regexp.MustCompile(`-----BEGIN [A-Z ]*PRIVATE KEY-----[\s\S]*?-----END [A-Z ]*PRIVATE KEY-----`),

	// key/value assignments across common source and Dockerfile syntaxes:
	//   KEY = "value" | KEY: "value" | KEY=value | ENV KEY value
	// Captures the assignment operator+value and replaces only that part,
	// leaving the key name (useful context) intact.
	regexp.MustCompile(`(?i)((?:api[_-]?key|secret|password|passwd|token|access[_-]?key|private[_-]?key|client[_-]?secret|auth)[a-z0-9_]*\s*[:=]\s*)["']?[A-Za-z0-9_\-/+=.]{8,}["']?`),
	regexp.MustCompile(`(?i)(ENV\s+(?:[A-Z0-9_]*(?:SECRET|PASSWORD|TOKEN|KEY)[A-Z0-9_]*)\s+)\S+`),
}

const redactedPlaceholder = "${1}[REDACTED]"

// redactSecrets replaces recognisable credential shapes in text collected
// from a monitored process (README, Dockerfile, entry-point source) before
// it is placed into an LLM prompt. Patterns with a capture group keep the key
// name and redact only the value; patterns without one redact the whole match.
func redactSecrets(text string) string {
	if text == "" {
		return text
	}
	for _, re := range redactPatterns {
		if re.NumSubexp() > 0 {
			text = re.ReplaceAllString(text, redactedPlaceholder)
		} else {
			text = re.ReplaceAllString(text, "[REDACTED]")
		}
	}
	return text
}
