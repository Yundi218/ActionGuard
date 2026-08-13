package config

import "testing"

func TestLoadRequiresDatabaseURL(t *testing.T) {
	t.Setenv("DATABASE_URL", "")
	if _, err := Load(); err == nil {
		t.Fatal("Load() error = nil, want missing DATABASE_URL")
	}
}

func TestLoadRequiresGatewayToken(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://postgres:postgres@localhost:5432/actionguard?sslmode=disable")
	t.Setenv("MCP_GATEWAY_TOKEN", "")
	if _, err := Load(); err == nil {
		t.Fatal("Load() error = nil, want missing MCP_GATEWAY_TOKEN")
	}
}

func TestLoadUsesExplicitAddresses(t *testing.T) {
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
