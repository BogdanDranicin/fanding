package config

import (
	"fmt"

	"github.com/kelseyhightower/envconfig"
)

type Config struct {
	DatabaseURL      string `envconfig:"DATABASE_URL"`
	PostgresUser     string `envconfig:"POSTGRES_USER"         default:"funding"`
	PostgresPassword string `envconfig:"POSTGRES_PASSWORD"     default:"changeme"`
	PostgresDB       string `envconfig:"POSTGRES_DB"           default:"funding"`
	PostgresHost     string `envconfig:"POSTGRES_HOST"         default:"postgres"`
	PostgresPort     int    `envconfig:"POSTGRES_PORT"         default:"5432"`
	TelegramToken    string `envconfig:"TELEGRAM_BOT_TOKEN"`
	TelegramBotName  string `envconfig:"TELEGRAM_BOT_USERNAME"`
	TwelveDataAPIKey string `envconfig:"TWELVEDATA_API_KEY"`
	MOEXPollMs       int    `envconfig:"MOEX_POLL_INTERVAL_MS" default:"250"`
	Port             int    `envconfig:"BACKEND_PORT"          default:"8080"`
	LogLevel         string `envconfig:"LOG_LEVEL"             default:"info"`
}

func Load() (*Config, error) {
	var cfg Config
	if err := envconfig.Process("", &cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}

func (c *Config) DSN() string {
	if c.DatabaseURL != "" {
		return c.DatabaseURL
	}
	return fmt.Sprintf("postgres://%s:%s@%s:%d/%s?sslmode=disable",
		c.PostgresUser, c.PostgresPassword, c.PostgresHost, c.PostgresPort, c.PostgresDB)
}
