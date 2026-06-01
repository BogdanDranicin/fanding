package config_test

import (
	"os"
	"testing"

	"github.com/funding-service/backend/internal/config"
)

func unsetConfigEnv() {
	for _, v := range []string{
		"POSTGRES_USER", "POSTGRES_PASSWORD", "POSTGRES_DB",
		"DATABASE_URL", "TELEGRAM_BOT_TOKEN", "MOEX_POLL_INTERVAL_MS",
		"BACKEND_PORT", "LOG_LEVEL",
	} {
		os.Unsetenv(v)
	}
}

func TestConfigDefaults(t *testing.T) {
	unsetConfigEnv()
	t.Cleanup(unsetConfigEnv)

	t.Setenv("POSTGRES_PASSWORD", "testpass")

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if cfg.PostgresUser != "funding" {
		t.Errorf("PostgresUser = %q, want %q", cfg.PostgresUser, "funding")
	}
	if cfg.PostgresPassword != "testpass" {
		t.Errorf("PostgresPassword = %q, want %q", cfg.PostgresPassword, "testpass")
	}
	if cfg.PostgresDB != "funding" {
		t.Errorf("PostgresDB = %q, want %q", cfg.PostgresDB, "funding")
	}
	if cfg.MOEXPollMs != 250 {
		t.Errorf("MOEXPollMs = %d, want %d", cfg.MOEXPollMs, 250)
	}
	if cfg.Port != 8080 {
		t.Errorf("Port = %d, want %d", cfg.Port, 8080)
	}
	if cfg.LogLevel != "info" {
		t.Errorf("LogLevel = %q, want %q", cfg.LogLevel, "info")
	}
}

func TestConfigFromEnv(t *testing.T) {
	unsetConfigEnv()
	t.Cleanup(unsetConfigEnv)

	os.Setenv("POSTGRES_USER", "myuser")
	os.Setenv("POSTGRES_PASSWORD", "mypassword")
	os.Setenv("BACKEND_PORT", "9090")
	os.Setenv("LOG_LEVEL", "debug")

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if cfg.PostgresUser != "myuser" {
		t.Errorf("PostgresUser = %q, want %q", cfg.PostgresUser, "myuser")
	}
	if cfg.Port != 9090 {
		t.Errorf("Port = %d, want %d", cfg.Port, 9090)
	}
	if cfg.LogLevel != "debug" {
		t.Errorf("LogLevel = %q, want %q", cfg.LogLevel, "debug")
	}
}
