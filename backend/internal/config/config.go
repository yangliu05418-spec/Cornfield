package config

import (
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	Environment              string
	PublicURL                string
	DatabaseURL              string
	ModelConfigPath          string
	AssetRoot                string
	CookieSecure             bool
	SessionTTL               time.Duration
	SessionIdleTTL           time.Duration
	LegnextAPIKey            string
	OpenRouterAPIKey         string
	BFLAPIKey                string
	ProviderCallbackSecret   string
	ProviderURLSigningSecret string
	ProviderMode             string
	LogLevel                 slog.Level
}

// Load keeps the strict Worker contract for callers and tests that need every
// live-mode secret. Public-facing processes should use LoadAPI so provider
// billing credentials never need to enter their container.
func Load() (Config, error) { return load(true) }

func LoadAPI() (Config, error) { return load(false) }

func LoadWorker() (Config, error) { return load(true) }

func load(requireProviderKeys bool) (Config, error) {
	databaseURL, err := DatabaseURLFromEnv()
	if err != nil {
		return Config{}, err
	}
	logLevel, err := parseLogLevel(env("LOG_LEVEL", "info"))
	if err != nil {
		return Config{}, err
	}
	cfg := Config{
		Environment:     env("APP_ENV", "development"),
		PublicURL:       env("APP_PUBLIC_URL", "http://localhost:8080"),
		DatabaseURL:     databaseURL,
		ModelConfigPath: env("MODEL_CONFIG_PATH", "../config/models.yaml"),
		AssetRoot:       env("ASSET_ROOT", "../data"),
		ProviderMode:    env("PROVIDER_MODE", "mock"),
		LogLevel:        logLevel,
	}
	if cfg.CookieSecure, err = strconv.ParseBool(env("SESSION_COOKIE_SECURE", "false")); err != nil {
		return Config{}, fmt.Errorf("parse SESSION_COOKIE_SECURE: %w", err)
	}
	if cfg.SessionTTL, err = time.ParseDuration(env("SESSION_TTL", "12h")); err != nil {
		return Config{}, fmt.Errorf("parse SESSION_TTL: %w", err)
	}
	if cfg.SessionIdleTTL, err = time.ParseDuration(env("SESSION_IDLE_TTL", "2h")); err != nil {
		return Config{}, fmt.Errorf("parse SESSION_IDLE_TTL: %w", err)
	}
	if requireProviderKeys {
		if cfg.LegnextAPIKey, err = secret("LEGNEXT_API_KEY"); err != nil {
			return Config{}, err
		}
		if cfg.OpenRouterAPIKey, err = secret("OPENROUTER_API_KEY"); err != nil {
			return Config{}, err
		}
		if cfg.BFLAPIKey, err = secret("BFL_API_KEY"); err != nil {
			return Config{}, err
		}
	}
	if cfg.ProviderCallbackSecret, err = secret("PROVIDER_CALLBACK_SECRET"); err != nil {
		return Config{}, err
	}
	if cfg.ProviderURLSigningSecret, err = secret("PROVIDER_URL_SIGNING_SECRET"); err != nil {
		return Config{}, err
	}
	if err := cfg.validate(requireProviderKeys); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func parseLogLevel(value string) (slog.Level, error) {
	switch value {
	case "debug":
		return slog.LevelDebug, nil
	case "info":
		return slog.LevelInfo, nil
	case "warn":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	default:
		return 0, fmt.Errorf("LOG_LEVEL must be one of debug, info, warn, error")
	}
}

// DatabaseURLFromEnv injects the database password from a file-backed secret
// without placing credentials in Compose configuration or process arguments.
// The returned URL is for in-process use only and must never be logged.
func DatabaseURLFromEnv() (string, error) {
	raw := env("DATABASE_URL", "postgres://studio:studio@localhost:5432/studio?sslmode=disable")
	databaseURL, err := url.Parse(raw)
	if err != nil || (databaseURL.Scheme != "postgres" && databaseURL.Scheme != "postgresql") || databaseURL.Host == "" || databaseURL.User == nil || databaseURL.User.Username() == "" {
		return "", fmt.Errorf("DATABASE_URL must be an absolute PostgreSQL URL with a username")
	}
	_, embeddedPassword := databaseURL.User.Password()
	production := env("APP_ENV", "development") == "production"
	if production && embeddedPassword {
		return "", fmt.Errorf("DATABASE_URL must not embed a password in production; use DATABASE_PASSWORD_FILE")
	}
	if production && strings.TrimSpace(os.Getenv("DATABASE_PASSWORD")) != "" {
		return "", fmt.Errorf("DATABASE_PASSWORD must not be set directly in production; use DATABASE_PASSWORD_FILE")
	}
	password, err := secret("DATABASE_PASSWORD")
	if err != nil {
		return "", err
	}
	if password == "" {
		if production {
			return "", fmt.Errorf("DATABASE_PASSWORD_FILE is required in production")
		}
		return raw, nil
	}
	if production && (len(password) < 32 || strings.ContainsAny(password, " \t\r\n")) {
		return "", fmt.Errorf("DATABASE_PASSWORD_FILE must contain at least 32 characters without whitespace")
	}
	if embeddedPassword {
		return "", fmt.Errorf("DATABASE_URL and DATABASE_PASSWORD_FILE must not both provide a password")
	}
	databaseURL.User = url.UserPassword(databaseURL.User.Username(), password)
	return databaseURL.String(), nil
}

func (c Config) validate(requireProviderKeys bool) error {
	if c.ProviderMode != "mock" && c.ProviderMode != "live" {
		return fmt.Errorf("PROVIDER_MODE must be mock or live")
	}
	if c.SessionTTL <= 0 || c.SessionTTL > 30*24*time.Hour {
		return fmt.Errorf("SESSION_TTL must be greater than zero and no more than 30 days")
	}
	if c.SessionIdleTTL <= 0 || c.SessionIdleTTL > c.SessionTTL {
		return fmt.Errorf("SESSION_IDLE_TTL must be greater than zero and no more than SESSION_TTL")
	}
	publicURL, err := url.Parse(c.PublicURL)
	if err != nil || publicURL.Host == "" || publicURL.User != nil || (publicURL.Scheme != "http" && publicURL.Scheme != "https") || (publicURL.Path != "" && publicURL.Path != "/") || publicURL.RawQuery != "" || publicURL.Fragment != "" {
		return fmt.Errorf("APP_PUBLIC_URL must be an absolute http(s) URL")
	}
	if c.Environment == "production" {
		if publicURL.Scheme != "https" {
			return fmt.Errorf("APP_PUBLIC_URL must use https in production")
		}
		if !c.CookieSecure {
			return fmt.Errorf("SESSION_COOKIE_SECURE must be true in production")
		}
	}
	if c.ProviderMode != "live" {
		return nil
	}
	secrets := make([]struct{ name, value string }, 0, 4)
	if requireProviderKeys {
		secrets = append(secrets,
			struct{ name, value string }{name: "LEGNEXT_API_KEY", value: c.LegnextAPIKey},
			struct{ name, value string }{name: "OPENROUTER_API_KEY", value: c.OpenRouterAPIKey},
			struct{ name, value string }{name: "BFL_API_KEY", value: c.BFLAPIKey},
		)
	}
	secrets = append(secrets,
		struct{ name, value string }{name: "PROVIDER_CALLBACK_SECRET", value: c.ProviderCallbackSecret},
		struct{ name, value string }{name: "PROVIDER_URL_SIGNING_SECRET", value: c.ProviderURLSigningSecret},
	)
	for _, secret := range secrets {
		name, value := secret.name, secret.value
		if value == "" {
			return fmt.Errorf("%s is required in live provider mode", name)
		}
		if strings.ContainsAny(value, " \t\r\n") {
			return fmt.Errorf("%s must contain one raw value without whitespace", name)
		}
	}
	if len(c.ProviderCallbackSecret) < 32 || len(c.ProviderURLSigningSecret) < 32 {
		return fmt.Errorf("provider internal secrets must be at least 32 bytes")
	}
	return nil
}

func env(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}

func secret(name string) (string, error) {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value, nil
	}
	path := strings.TrimSpace(os.Getenv(name + "_FILE"))
	if path == "" {
		return "", nil
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read %s_FILE: %w", name, err)
	}
	return strings.TrimSpace(string(b)), nil
}
