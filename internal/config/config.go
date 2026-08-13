package config

import (
	"errors"
	"os"
)

type Config struct {
	DatabaseURL     string
	APIAddr         string
	MCPAddr         string
	MCPGatewayToken string
}

func Load() (Config, error) {
	cfg := Config{
		DatabaseURL:     os.Getenv("DATABASE_URL"),
		APIAddr:         valueOrDefault("API_ADDR", ":8080"),
		MCPAddr:         valueOrDefault("MCP_ADDR", ":8081"),
		MCPGatewayToken: os.Getenv("MCP_GATEWAY_TOKEN"),
	}
	if cfg.DatabaseURL == "" {
		return Config{}, errors.New("DATABASE_URL is required")
	}
	if cfg.MCPGatewayToken == "" {
		return Config{}, errors.New("MCP_GATEWAY_TOKEN is required")
	}
	return cfg, nil
}

func valueOrDefault(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}
