package config

import (
	"log/slog"
	"net/url"
	"os"
	"strings"
	"testing"
)

func TestLoadAcceptsSupportedLogLevels(t *testing.T) {
	tests := []struct {
		value string
		want  slog.Level
	}{
		{value: "debug", want: slog.LevelDebug},
		{value: "info", want: slog.LevelInfo},
		{value: "warn", want: slog.LevelWarn},
		{value: "error", want: slog.LevelError},
	}
	for _, tt := range tests {
		t.Run(tt.value, func(t *testing.T) {
			t.Setenv("LOG_LEVEL", tt.value)
			t.Setenv("PROVIDER_MODE", "mock")

			cfg, err := LoadAPI()
			if err != nil {
				t.Fatalf("LoadAPI() error = %v", err)
			}
			if cfg.LogLevel != tt.want {
				t.Fatalf("LogLevel = %v, want %v", cfg.LogLevel, tt.want)
			}
		})
	}
}

func TestLoadRejectsUnsupportedLogLevel(t *testing.T) {
	for _, value := range []string{"trace", "INFO", "warning", "0"} {
		t.Run(value, func(t *testing.T) {
			t.Setenv("LOG_LEVEL", value)
			t.Setenv("PROVIDER_MODE", "mock")

			if _, err := LoadAPI(); err == nil || !strings.Contains(err.Error(), "LOG_LEVEL") {
				t.Fatalf("expected LOG_LEVEL validation error, got %v", err)
			}
		})
	}
}

func TestLoadRejectsUnsafeSessionDurations(t *testing.T) {
	tests := []struct {
		name string
		ttl  string
		idle string
	}{
		{name: "zero absolute TTL", ttl: "0s", idle: "1s"},
		{name: "negative idle TTL", ttl: "12h", idle: "-1s"},
		{name: "idle exceeds absolute TTL", ttl: "1h", idle: "2h"},
		{name: "absolute TTL too long", ttl: "744h", idle: "1h"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("APP_ENV", "development")
			t.Setenv("APP_PUBLIC_URL", "http://localhost:8080")
			t.Setenv("SESSION_COOKIE_SECURE", "false")
			t.Setenv("PROVIDER_MODE", "mock")
			t.Setenv("SESSION_TTL", tt.ttl)
			t.Setenv("SESSION_IDLE_TTL", tt.idle)
			if _, err := LoadAPI(); err == nil {
				t.Fatal("LoadAPI accepted an unsafe session duration")
			}
		})
	}
}

func TestLoadRejectsMissingExplicitSecretFile(t *testing.T) {
	t.Setenv("PROVIDER_MODE", "mock")
	t.Setenv("LEGNEXT_API_KEY", "")
	t.Setenv("LEGNEXT_API_KEY_FILE", t.TempDir()+"/missing")

	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "LEGNEXT_API_KEY_FILE") {
		t.Fatalf("expected missing secret file error, got %v", err)
	}
}

func TestLoadRequiresLiveSecrets(t *testing.T) {
	t.Setenv("PROVIDER_MODE", "live")
	t.Setenv("LEGNEXT_API_KEY", "")
	t.Setenv("OPENROUTER_API_KEY", "")
	t.Setenv("BFL_API_KEY", "")
	t.Setenv("PROVIDER_CALLBACK_SECRET", "")
	t.Setenv("PROVIDER_URL_SIGNING_SECRET", "")
	t.Setenv("LEGNEXT_API_KEY_FILE", "")
	t.Setenv("OPENROUTER_API_KEY_FILE", "")
	t.Setenv("BFL_API_KEY_FILE", "")
	t.Setenv("PROVIDER_CALLBACK_SECRET_FILE", "")
	t.Setenv("PROVIDER_URL_SIGNING_SECRET_FILE", "")

	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "LEGNEXT_API_KEY") {
		t.Fatalf("expected missing live secret error, got %v", err)
	}
}

func TestLoadAPIDoesNotRequireProviderBillingKeys(t *testing.T) {
	t.Setenv("APP_ENV", "production")
	t.Setenv("APP_PUBLIC_URL", "https://studio.example")
	t.Setenv("SESSION_COOKIE_SECURE", "true")
	t.Setenv("PROVIDER_MODE", "live")
	t.Setenv("LEGNEXT_API_KEY", "")
	t.Setenv("OPENROUTER_API_KEY", "")
	t.Setenv("BFL_API_KEY", "")
	t.Setenv("LEGNEXT_API_KEY_FILE", t.TempDir()+"/must-not-be-read")
	t.Setenv("OPENROUTER_API_KEY_FILE", t.TempDir()+"/must-not-be-read")
	t.Setenv("BFL_API_KEY_FILE", t.TempDir()+"/must-not-be-read")
	t.Setenv("PROVIDER_CALLBACK_SECRET", strings.Repeat("c", 32))
	t.Setenv("PROVIDER_URL_SIGNING_SECRET", strings.Repeat("u", 32))
	setDatabasePasswordFile(t, "db-password-000000000000000000000000")

	cfg, err := LoadAPI()
	if err != nil {
		t.Fatalf("API configuration unexpectedly required billing keys: %v", err)
	}
	if cfg.LegnextAPIKey != "" || cfg.OpenRouterAPIKey != "" || cfg.BFLAPIKey != "" {
		t.Fatal("API configuration contains provider billing keys")
	}
}

func TestLoadRejectsUnsafeProductionURL(t *testing.T) {
	t.Setenv("APP_ENV", "production")
	t.Setenv("APP_PUBLIC_URL", "http://studio.example")
	t.Setenv("SESSION_COOKIE_SECURE", "true")
	t.Setenv("PROVIDER_MODE", "mock")
	setDatabasePasswordFile(t, "db-password-000000000000000000000000")

	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "https") {
		t.Fatalf("expected production https error, got %v", err)
	}
}

func TestLoadAcceptsProductionLiveConfiguration(t *testing.T) {
	t.Setenv("APP_ENV", "production")
	t.Setenv("APP_PUBLIC_URL", "https://studio.example")
	t.Setenv("SESSION_COOKIE_SECURE", "true")
	t.Setenv("PROVIDER_MODE", "live")
	t.Setenv("LEGNEXT_API_KEY", "legnext-test-key")
	t.Setenv("OPENROUTER_API_KEY", "sk-or-v1-test")
	t.Setenv("BFL_API_KEY", "bfl-test-key")
	t.Setenv("PROVIDER_CALLBACK_SECRET", strings.Repeat("c", 32))
	t.Setenv("PROVIDER_URL_SIGNING_SECRET", strings.Repeat("u", 32))
	setDatabasePasswordFile(t, "db-password-000000000000000000000000")

	if _, err := Load(); err != nil {
		t.Fatalf("expected valid production configuration, got %v", err)
	}
}

func TestDatabaseURLFromEnvReadsPasswordFile(t *testing.T) {
	t.Setenv("APP_ENV", "production")
	t.Setenv("DATABASE_URL", "postgres://studio_api@postgres:5432/studio?sslmode=disable")
	setDatabasePasswordFile(t, "p@ssword:/?#with-encoding-0000000000")

	value, err := DatabaseURLFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := url.Parse(value)
	if err != nil {
		t.Fatal(err)
	}
	password, ok := parsed.User.Password()
	if !ok || password != "p@ssword:/?#with-encoding-0000000000" {
		t.Fatal("database password was not safely injected into the URL")
	}
}

func TestDatabaseURLFromEnvRejectsProductionEmbeddedPassword(t *testing.T) {
	t.Setenv("APP_ENV", "production")
	t.Setenv("DATABASE_URL", "postgres://studio_api:unsafe@postgres:5432/studio?sslmode=disable")
	t.Setenv("DATABASE_PASSWORD_FILE", "")
	if _, err := DatabaseURLFromEnv(); err == nil || !strings.Contains(err.Error(), "must not embed") {
		t.Fatalf("expected embedded password rejection, got %v", err)
	}
}

func setDatabasePasswordFile(t *testing.T, value string) {
	t.Helper()
	path := t.TempDir() + "/database-password"
	if err := os.WriteFile(path, []byte(value), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DATABASE_URL", "postgres://studio_owner@postgres:5432/studio?sslmode=disable")
	t.Setenv("DATABASE_PASSWORD", "")
	t.Setenv("DATABASE_PASSWORD_FILE", path)
}
