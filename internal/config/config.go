package config

import (
	"errors"
	"net/url"
	"os"
	"strings"
)

const (
	ProviderDeterministic = "deterministic"
	ProviderOpenAI        = "openai"
	DefaultOpenAIBaseURL  = "https://api.openai.com"
)

type ProviderSettings struct {
	LLMProvider          string
	EmbeddingProvider    string
	OpenAIAPIKey         string
	OpenAIBaseURL        string
	OpenAIModel          string
	OpenAIEmbeddingModel string
}

type Config struct {
	ProviderSettings
	DatabaseURL     string
	APIAddr         string
	MCPAddr         string
	MCPGatewayToken string
}

func Load() (Config, error) {
	providers, err := LoadProviderSettings()
	if err != nil {
		return Config{}, err
	}
	cfg := Config{
		ProviderSettings: providers,
		DatabaseURL:      os.Getenv("DATABASE_URL"),
		APIAddr:          valueOrDefault("API_ADDR", ":8080"),
		MCPAddr:          valueOrDefault("MCP_ADDR", ":8081"),
		MCPGatewayToken:  os.Getenv("MCP_GATEWAY_TOKEN"),
	}
	if cfg.DatabaseURL == "" {
		return Config{}, errors.New("DATABASE_URL is required")
	}
	if cfg.MCPGatewayToken == "" {
		return Config{}, errors.New("MCP_GATEWAY_TOKEN is required")
	}
	return cfg, nil
}

func LoadProviderSettings() (ProviderSettings, error) {
	settings := ProviderSettings{
		LLMProvider:          valueOrDefault("LLM_PROVIDER", ProviderDeterministic),
		EmbeddingProvider:    valueOrDefault("EMBEDDING_PROVIDER", ProviderDeterministic),
		OpenAIAPIKey:         os.Getenv("OPENAI_API_KEY"),
		OpenAIBaseURL:        valueOrDefault("OPENAI_BASE_URL", DefaultOpenAIBaseURL),
		OpenAIModel:          os.Getenv("OPENAI_MODEL"),
		OpenAIEmbeddingModel: os.Getenv("OPENAI_EMBEDDING_MODEL"),
	}
	if !knownProvider(settings.LLMProvider) {
		return ProviderSettings{}, errors.New("LLM_PROVIDER must be deterministic or openai")
	}
	if !knownProvider(settings.EmbeddingProvider) {
		return ProviderSettings{}, errors.New("EMBEDDING_PROVIDER must be deterministic or openai")
	}
	usesOpenAI := settings.LLMProvider == ProviderOpenAI || settings.EmbeddingProvider == ProviderOpenAI
	if usesOpenAI && strings.TrimSpace(settings.OpenAIAPIKey) == "" {
		return ProviderSettings{}, errors.New("OPENAI_API_KEY is required for openai providers")
	}
	if settings.LLMProvider == ProviderOpenAI && strings.TrimSpace(settings.OpenAIModel) == "" {
		return ProviderSettings{}, errors.New("OPENAI_MODEL is required when LLM_PROVIDER=openai")
	}
	if settings.EmbeddingProvider == ProviderOpenAI && strings.TrimSpace(settings.OpenAIEmbeddingModel) == "" {
		return ProviderSettings{}, errors.New("OPENAI_EMBEDDING_MODEL is required when EMBEDDING_PROVIDER=openai")
	}
	if usesOpenAI && !validBaseURL(settings.OpenAIBaseURL) {
		return ProviderSettings{}, errors.New("OPENAI_BASE_URL must be an http or https URL without credentials")
	}
	return settings, nil
}

func knownProvider(provider string) bool {
	return provider == ProviderDeterministic || provider == ProviderOpenAI
}

func validBaseURL(value string) bool {
	parsed, err := url.Parse(value)
	return err == nil && (parsed.Scheme == "http" || parsed.Scheme == "https") && parsed.Host != "" && parsed.User == nil && parsed.RawQuery == "" && parsed.Fragment == ""
}

func valueOrDefault(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}
