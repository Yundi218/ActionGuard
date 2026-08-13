package config

import (
	"strings"
	"testing"
)

func TestLoadRequiresDatabaseURL(t *testing.T) {
	setDeterministicProviders(t)
	t.Setenv("DATABASE_URL", "")
	if _, err := Load(); err == nil {
		t.Fatal("Load() error = nil, want missing DATABASE_URL")
	}
}

func TestLoadRequiresGatewayToken(t *testing.T) {
	setDeterministicProviders(t)
	t.Setenv("DATABASE_URL", "postgres://postgres:postgres@localhost:5432/actionguard?sslmode=disable")
	t.Setenv("MCP_GATEWAY_TOKEN", "")
	if _, err := Load(); err == nil {
		t.Fatal("Load() error = nil, want missing MCP_GATEWAY_TOKEN")
	}
}

func TestLoadUsesExplicitAddresses(t *testing.T) {
	setDeterministicProviders(t)
	setAPIEnvironment(t)
	t.Setenv("DATABASE_URL", "postgres://postgres:postgres@localhost:5432/actionguard?sslmode=disable")
	t.Setenv("MCP_GATEWAY_TOKEN", "test-gateway-token")
	t.Setenv("API_ADDR", ":8090")
	t.Setenv("MCP_ADDR", ":8091")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.APIAddr != ":8090" || cfg.MCPAddr != ":8091" {
		t.Fatalf("addresses = %q, %q", cfg.APIAddr, cfg.MCPAddr)
	}
}

func TestLoadProviderSettingsDefaultToExplicitDeterministicModes(t *testing.T) {
	setProviderEnvironment(t, "", "", "", "", "", "")
	settings, err := LoadProviderSettings()
	if err != nil {
		t.Fatal(err)
	}
	if settings.LLMProvider != ProviderDeterministic || settings.EmbeddingProvider != ProviderDeterministic {
		t.Fatalf("providers = %q/%q", settings.LLMProvider, settings.EmbeddingProvider)
	}
	if settings.OpenAIBaseURL != DefaultOpenAIBaseURL {
		t.Fatalf("base URL = %q, want %q", settings.OpenAIBaseURL, DefaultOpenAIBaseURL)
	}
}

func TestLoadProviderSettingsAcceptsCompleteOpenAIModes(t *testing.T) {
	setProviderEnvironment(t, ProviderOpenAI, ProviderOpenAI, "secret-key", "https://openai.example.test", "planner-model", "embedding-model")
	settings, err := LoadProviderSettings()
	if err != nil {
		t.Fatal(err)
	}
	if settings.LLMProvider != ProviderOpenAI || settings.EmbeddingProvider != ProviderOpenAI ||
		settings.OpenAIAPIKey != "secret-key" || settings.OpenAIModel != "planner-model" || settings.OpenAIEmbeddingModel != "embedding-model" {
		t.Fatalf("settings = %#v", settings)
	}
}

func TestLoadProviderSettingsRejectsUnknownOrIncompleteModesWithoutSecrets(t *testing.T) {
	tests := []struct {
		name, llmProvider, embeddingProvider, apiKey, plannerModel, embeddingModel string
	}{
		{name: "unknown llm", llmProvider: "typo"},
		{name: "unknown embedding", embeddingProvider: "typo"},
		{name: "planner missing key", llmProvider: ProviderOpenAI, plannerModel: "planner-model"},
		{name: "planner missing model", llmProvider: ProviderOpenAI, apiKey: "secret-key"},
		{name: "embedding missing key", embeddingProvider: ProviderOpenAI, embeddingModel: "embedding-model"},
		{name: "embedding missing model", embeddingProvider: ProviderOpenAI, apiKey: "secret-key"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			setProviderEnvironment(t, test.llmProvider, test.embeddingProvider, test.apiKey, "", test.plannerModel, test.embeddingModel)
			_, err := LoadProviderSettings()
			if err == nil {
				t.Fatal("LoadProviderSettings() error = nil")
			}
			if strings.Contains(err.Error(), "secret-key") {
				t.Fatalf("configuration error leaked credential: %v", err)
			}
		})
	}
}

func TestLoadIncludesProviderSettings(t *testing.T) {
	setProviderEnvironment(t, ProviderOpenAI, ProviderDeterministic, "secret-key", "https://openai.example.test", "planner-model", "")
	t.Setenv("DATABASE_URL", "postgres://postgres:postgres@localhost:5432/actionguard?sslmode=disable")
	t.Setenv("MCP_GATEWAY_TOKEN", "test-gateway-token")
	t.Setenv("MCP_URL", "http://127.0.0.1:8081/mcp")
	t.Setenv("DEMO_FULL_TOKEN", "full-token")
	t.Setenv("DEMO_READ_ONLY_TOKEN", "read-token")
	t.Setenv("DEMO_USER_999_TOKEN", "other-token")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.LLMProvider != ProviderOpenAI || cfg.OpenAIModel != "planner-model" {
		t.Fatalf("config providers = %#v", cfg.ProviderSettings)
	}
}

func TestLoadRequiresExplicitAPISecretsAndMCPURLWithoutLeakingValues(t *testing.T) {
	setDeterministicProviders(t)
	t.Setenv("DATABASE_URL", "postgres://example.test/actionguard")
	t.Setenv("MCP_GATEWAY_TOKEN", "gateway-secret")
	t.Setenv("MCP_URL", "http://127.0.0.1:8081/mcp")
	t.Setenv("DEMO_FULL_TOKEN", "full-secret")
	t.Setenv("DEMO_READ_ONLY_TOKEN", "read-secret")
	t.Setenv("DEMO_USER_999_TOKEN", "other-secret")
	keys := []string{"MCP_URL", "DEMO_FULL_TOKEN", "DEMO_READ_ONLY_TOKEN", "DEMO_USER_999_TOKEN"}
	for _, key := range keys {
		t.Run(key, func(t *testing.T) {
			t.Setenv(key, "")
			_, err := Load()
			if err == nil {
				t.Fatal("Load error = nil")
			}
			for _, secret := range []string{"gateway-secret", "full-secret", "read-secret", "other-secret"} {
				if strings.Contains(err.Error(), secret) {
					t.Fatalf("error leaked secret: %v", err)
				}
			}
		})
	}
}

func setDeterministicProviders(t *testing.T) {
	t.Helper()
	setProviderEnvironment(t, ProviderDeterministic, ProviderDeterministic, "", "", "", "")
}

func setAPIEnvironment(t *testing.T) {
	t.Helper()
	t.Setenv("MCP_URL", "http://127.0.0.1:8081/mcp")
	t.Setenv("DEMO_FULL_TOKEN", "full-token")
	t.Setenv("DEMO_READ_ONLY_TOKEN", "read-token")
	t.Setenv("DEMO_USER_999_TOKEN", "other-token")
}

func setProviderEnvironment(t *testing.T, llmProvider, embeddingProvider, apiKey, baseURL, plannerModel, embeddingModel string) {
	t.Helper()
	t.Setenv("LLM_PROVIDER", llmProvider)
	t.Setenv("EMBEDDING_PROVIDER", embeddingProvider)
	t.Setenv("OPENAI_API_KEY", apiKey)
	t.Setenv("OPENAI_BASE_URL", baseURL)
	t.Setenv("OPENAI_MODEL", plannerModel)
	t.Setenv("OPENAI_EMBEDDING_MODEL", embeddingModel)
}
