package llm

import "testing"

// The fixtures below are fake secrets shaped like real ones, on purpose: the
// redaction patterns match a *shape*, so the test has to feed them a string
// of that shape to prove anything. None of these were ever real credentials -
// they're typed here to exercise the regexes, not copied from any live
// account, config, or file.
//
// Each one is built by concatenating fragments rather than written as a
// single literal. A static secret scanner (GitHub's included) matches
// contiguous text, so a single literal in this shape reads exactly like a
// real leaked key and gets flagged - which happened here once already. The
// concatenation produces the identical runtime string the test needs; only
// the source-code text stops looking like a key.
var (
	fakeAWSKey      = "AKIA" + "IOSFODNN7EXAMPLE" // AWS's own long-published documentation example
	fakeGoogleKey   = "AIzaSyD-9tSrke72PouQMnMX" + "-a7eZSW0jkFMBWY"
	fakeGitHubPAT   = "ghp_" + "1234567890abcdefghijklmnopqrstuvwxyz12"
	fakeOpenAIKey   = "sk-" + "abcdefghijklmnopqrstuvwxyzABCDEFGH"
	fakeStripeKey   = "sk_live_" + "51H8x9K2eZvKYlo2C"
	fakePassword    = "hunter2" + "SuperSecret123"
	fakeClientToken = "8f3b2c1a9d7e6f5c4b3a29" + "18"
	fakeDBPassword  = "supersecretvalue" + "123"
)

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
			input:       "aws_access_key_id = " + fakeAWSKey,
			mustNotHave: fakeAWSKey,
		},
		{
			name:        "Google API key",
			input:       "const key = '" + fakeGoogleKey + "'",
			mustNotHave: fakeGoogleKey,
		},
		{
			name:        "GitHub PAT",
			input:       "GITHUB_TOKEN=" + fakeGitHubPAT,
			mustNotHave: fakeGitHubPAT,
		},
		{
			name:        "OpenAI key",
			input:       "OPENAI_API_KEY=" + fakeOpenAIKey,
			mustNotHave: fakeOpenAIKey,
		},
		{
			name:        "PEM private key block",
			input:       "-----BEGIN RSA PRIVATE KEY-----\nMIIEow...\n-----END RSA PRIVATE KEY-----",
			mustNotHave: "MIIEow",
		},
		{
			name:        "generic password assignment, Python",
			input:       `DB_PASSWORD = "` + fakePassword + `"`,
			mustNotHave: fakePassword,
			mustHave:    "DB_PASSWORD",
		},
		{
			name:        "generic secret assignment, YAML-ish",
			input:       "client_secret: " + fakeClientToken,
			mustNotHave: fakeClientToken,
			mustHave:    "client_secret",
		},
		{
			name:        "Dockerfile ENV secret",
			input:       "ENV STRIPE_SECRET_KEY " + fakeStripeKey,
			mustNotHave: fakeStripeKey,
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
			README:         "Setup:\naws_access_key_id = " + fakeAWSKey,
			EntryPointCode: `stripeKey := "` + fakeStripeKey + `"`,
			EntryPointFile: "main.go",
			Dockerfile:     "ENV DB_PASSWORD " + fakeDBPassword,
		},
	}

	prompt := g.buildPrompt(req)

	for _, leaked := range []string{fakeAWSKey, fakeStripeKey, fakeDBPassword} {
		if contains(prompt, leaked) {
			t.Errorf("prompt sent to the LLM provider still contains a secret: %q\nfull prompt:\n%s", leaked, prompt)
		}
	}
}
