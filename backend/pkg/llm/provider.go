// Package llm provides a unified interface for multiple LLM providers.
package llm

import (
	"context"
	"fmt"
)

// Provider represents an LLM provider type.
type Provider string

const (
	ProviderOpenAI    Provider = "openai"
	ProviderAnthropic Provider = "anthropic"
	ProviderGemini    Provider = "gemini"
	ProviderOllama    Provider = "ollama"   // Local LLM
	ProviderLMStudio  Provider = "lmstudio" // Local LLM
)

// Message represents a chat message.
type Message struct {
	Role    string `json:"role"` // "system", "user", "assistant"
	Content string `json:"content"`
}

// CompletionRequest represents a request to generate a completion.
type CompletionRequest struct {
	Messages    []Message `json:"messages"`
	MaxTokens   int       `json:"max_tokens,omitempty"`
	Temperature float64   `json:"temperature,omitempty"`
	Model       string    `json:"model,omitempty"` // Override default model
}

// CompletionResponse represents the response from an LLM.
type CompletionResponse struct {
	Content      string `json:"content"`
	Model        string `json:"model"`
	Provider     string `json:"provider"`
	TokensUsed   int    `json:"tokens_used,omitempty"`
	FinishReason string `json:"finish_reason,omitempty"`
}

// LLMProvider is the interface that all LLM providers must implement.
type LLMProvider interface {
	// Name returns the provider name.
	Name() string

	// IsConfigured returns true if the provider has valid configuration.
	IsConfigured() bool

	// Complete generates a completion for the given request.
	Complete(ctx context.Context, req CompletionRequest) (*CompletionResponse, error)

	// ListModels returns available models for this provider.
	ListModels(ctx context.Context) ([]string, error)
}

// Config holds configuration for all LLM providers.
type Config struct {
	// OpenAI
	OpenAIAPIKey string `json:"openai_api_key,omitempty"`
	OpenAIModel  string `json:"openai_model,omitempty"` // Default: gpt-4-turbo-preview

	// Anthropic
	AnthropicAPIKey string `json:"anthropic_api_key,omitempty"`
	AnthropicModel  string `json:"anthropic_model,omitempty"` // Default: claude-3-opus-20240229

	// Google Gemini
	GeminiAPIKey string `json:"gemini_api_key,omitempty"`
	GeminiModel  string `json:"gemini_model,omitempty"` // Default: gemini-pro

	// Ollama (local)
	OllamaURL   string `json:"ollama_url,omitempty"`   // Default: http://localhost:11434
	OllamaModel string `json:"ollama_model,omitempty"` // Default: llama2

	// LM Studio (local)
	LMStudioURL   string `json:"lmstudio_url,omitempty"` // Default: http://localhost:1234
	LMStudioModel string `json:"lmstudio_model,omitempty"`

	// Default provider to use
	DefaultProvider Provider `json:"default_provider,omitempty"`
}

// Manager manages multiple LLM providers.
type Manager struct {
	config    *Config
	providers map[Provider]LLMProvider
}

// NewManager creates a new LLM manager with the given configuration.
func NewManager(config *Config) *Manager {
	if config == nil {
		config = &Config{}
	}

	m := &Manager{
		config:    config,
		providers: make(map[Provider]LLMProvider),
	}

	// Initialize providers
	m.providers[ProviderOpenAI] = NewOpenAIProvider(config)
	m.providers[ProviderAnthropic] = NewAnthropicProvider(config)
	m.providers[ProviderGemini] = NewGeminiProvider(config)
	m.providers[ProviderOllama] = NewOllamaProvider(config)
	m.providers[ProviderLMStudio] = NewLMStudioProvider(config)

	return m
}

// GetProvider returns a specific provider.
func (m *Manager) GetProvider(p Provider) (LLMProvider, error) {
	provider, ok := m.providers[p]
	if !ok {
		return nil, fmt.Errorf("unknown provider: %s", p)
	}
	return provider, nil
}

// GetConfiguredProviders returns all providers that have valid configuration.
func (m *Manager) GetConfiguredProviders() []LLMProvider {
	var configured []LLMProvider
	for _, p := range m.providers {
		if p.IsConfigured() {
			configured = append(configured, p)
		}
	}
	return configured
}

// GetDefaultProvider returns the default provider (first configured one).
func (m *Manager) GetDefaultProvider() (LLMProvider, error) {
	// If a default is set and configured, use it
	if m.config.DefaultProvider != "" {
		p, err := m.GetProvider(m.config.DefaultProvider)
		if err == nil && p.IsConfigured() {
			return p, nil
		}
	}

	// Otherwise, return first configured provider
	// Priority: OpenAI > Anthropic > Gemini > Ollama > LMStudio
	priority := []Provider{ProviderOpenAI, ProviderAnthropic, ProviderGemini, ProviderOllama, ProviderLMStudio}
	for _, name := range priority {
		if p, ok := m.providers[name]; ok && p.IsConfigured() {
			return p, nil
		}
	}

	return nil, fmt.Errorf("no LLM provider configured")
}

// UpdateConfig updates the configuration and reinitializes providers.
func (m *Manager) UpdateConfig(config *Config) {
	m.config = config
	m.providers[ProviderOpenAI] = NewOpenAIProvider(config)
	m.providers[ProviderAnthropic] = NewAnthropicProvider(config)
	m.providers[ProviderGemini] = NewGeminiProvider(config)
	m.providers[ProviderOllama] = NewOllamaProvider(config)
	m.providers[ProviderLMStudio] = NewLMStudioProvider(config)
}

// Complete generates a completion using the default provider.
func (m *Manager) Complete(ctx context.Context, req CompletionRequest) (*CompletionResponse, error) {
	provider, err := m.GetDefaultProvider()
	if err != nil {
		return nil, err
	}
	return provider.Complete(ctx, req)
}

// CompleteWith generates a completion using a specific provider.
func (m *Manager) CompleteWith(ctx context.Context, providerName Provider, req CompletionRequest) (*CompletionResponse, error) {
	provider, err := m.GetProvider(providerName)
	if err != nil {
		return nil, err
	}
	if !provider.IsConfigured() {
		return nil, fmt.Errorf("provider %s is not configured", providerName)
	}
	return provider.Complete(ctx, req)
}

// Status returns the configuration status of all providers.
func (m *Manager) Status() map[string]bool {
	status := make(map[string]bool)
	for name, p := range m.providers {
		status[string(name)] = p.IsConfigured()
	}
	return status
}
