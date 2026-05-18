package config

import "github.com/kelseyhightower/envconfig"

type Config struct {
	PostgresUser     string `envconfig:"POSTGRES_USER"         default:"funding"`
	PostgresPassword string `envconfig:"POSTGRES_PASSWORD"     default:"changeme"`
	PostgresDB       string `envconfig:"POSTGRES_DB"           default:"funding"`
	TelegramToken      string `envconfig:"TELEGRAM_BOT_TOKEN"`
	TwelveDataAPIKey   string `envconfig:"TWELVEDATA_API_KEY"`
	MOEXPollMs         int    `envconfig:"MOEX_POLL_INTERVAL_MS" default:"250"`
	Port             int    `envconfig:"BACKEND_PORT"          default:"8080"`
	LogLevel         string `envconfig:"LOG_LEVEL"             default:"info"`
	// Auth (future): APIKey string `envconfig:"API_KEY"`
}

func Load() (*Config, error) {
	var cfg Config
	if err := envconfig.Process("", &cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}
