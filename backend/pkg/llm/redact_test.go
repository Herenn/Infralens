package llm

import "testing"

// TestRedactSecrets covers the credential shapes most likely to show up
// verbatim in a README, Dockerfile, or entry-point file - which is exactly
// the content buildPrompt sends to whichever LLM provider is configured,
// including cloud providers by default.
func TestRedactSecrets(t *testing.T) {
	tests := []struct {
		name        string
		input       string
		mustNotHave string // substring that must NOT survive redaction
		mustHave    string // substring that must still be present
	}{
		{
			name:        "AWS access key",
			input:       "aws_access_key_id = AKIAIOSFODNN7EXAMPLE",
			mustNotHave: "AKIAIOSFODNN7EXAMPLE",
		},
		{
			name:        "Google API key",
			input:       "const key = 'AIzaSyD-9tSrke72PouQMnMX-a7eZSW0jkFMBWY'",
			mustNotHave: "AIzaSyD-9tSrke72PouQMnMX-a7eZSW0jkFMBWY",
		},
		{
			name:        "GitHub PAT",
			input:       "GITHUB_TOKEN=ghp_1234567890abcdefghijklmnopqrstuvwxyz12",
			mustNotHave: "ghp_1234567890abcdefghijklmnopqrstuvwxyz12",
		},
		{
			name:        "OpenAI key",
			input:       "OPENAI_API_KEY=sk-abcdefghijklmnopqrstuvwxyzABCDEFGH",
			mustNotHave: "sk-abcdefghijklmnopqrstuvwxyzABCDEFGH",
		},
		{
			name:        "PEM private key block",
			input:       "-----BEGIN RSA PRIVATE KEY-----\nMIIEow...\n-----END RSA PRIVATE KEY-----",
			mustNotHave: "MIIEow",
		},
		{
			name:        "generic password assignment, Python",
			input:       `DB_PASSWORD = "hunter2SuperSecret123"`,
			mustNotHave: "hunter2SuperSecret123",
			mustHave:    "DB_PASSWORD",
		},
		{
			name:        "generic secret assignment, YAML-ish",
			input:       "client_secret: 8f3b2c1a9d7e6f5c4b3a2918",
			mustNotHave: "8f3b2c1a9d7e6f5c4b3a2918",
			mustHave:    "client_secret",
		},
		{
			name:        "Dockerfile ENV secret",
			input:       "ENV STRIPE_SECRET_KEY sk_live_51H8x9K2eZvKYlo2C",
			mustNotHave: "sk_live_51H8x9K2eZvKYlo2C",
			mustHave:    "STRIPE_SECRET_KEY",
		},
		{
			name:     "ordinary code is untouched",
			input:    "func main() {\n\tfmt.Println(\"hello world\")\n}",
			mustHave: "fmt.Println(\"hello world\")",
		},
		{
			name:     "a README mentioning the word token in prose",
			input:    "This service validates a JWT token on every request.",
			mustHave: "validates a JWT token",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := redactSecrets(tc.input)

			if tc.mustNotHave != "" && contains(got, tc.mustNotHave) {
				t.Errorf("secret survived redaction: %q\n  input:  %q\n  output: %q", tc.mustNotHave, tc.input, got)
			}
			if tc.mustHave != "" && !contains(got, tc.mustHave) {
				t.Errorf("expected %q to survive redaction\n  input:  %q\n  output: %q", tc.mustHave, tc.input, got)
			}
		})
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (substr == "" || indexOf(s, substr) >= 0)
}

func indexOf(s, substr string) int {
	for i := 0; i+len(substr) <= len(s); i++ {
		if s[i:i+len(substr)] == substr {
			return i
		}
	}
	return -1
}

// TestBuildPromptRedactsCodeContext is an integration-level check that the
// prompt builder actually routes README/EntryPoint/Dockerfile through
// redaction rather than the redaction existing but not being wired in.
func TestBuildPromptRedactsCodeContext(t *testing.T) {
	g := NewDocsGenerator(NewManager(&Config{}))

	req := DocumentationRequest{
		Context: ServiceContext{
			ServiceName:    "payments",
			README:         "Setup:\naws_access_key_id = AKIAIOSFODNN7EXAMPLE",
			EntryPointCode: `stripeKey := "sk_live_51H8x9K2eZvKYlo2C"`,
			EntryPointFile: "main.go",
			Dockerfile:     "ENV DB_PASSWORD supersecretvalue123",
		},
	}

	prompt := g.buildPrompt(req)

	for _, leaked := range []string{"AKIAIOSFODNN7EXAMPLE", "sk_live_51H8x9K2eZvKYlo2C", "supersecretvalue123"} {
		if contains(prompt, leaked) {
			t.Errorf("prompt sent to the LLM provider still contains a secret: %q\nfull prompt:\n%s", leaked, prompt)
		}
	}
}
