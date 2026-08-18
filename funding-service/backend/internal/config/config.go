package config

import (
	"fmt"
	"os"
	"strconv"

	"github.com/kelseyhightower/envconfig"
)

type Config struct {
	DatabaseURL      string `envconfig:"DATABASE_URL"`
	PostgresUser     string `envconfig:"POSTGRES_USER"         default:"funding"`
	PostgresPassword string `envconfig:"POSTGRES_PASSWORD"`
	PostgresDB       string `envconfig:"POSTGRES_DB"           default:"funding"`
	PostgresHost     string `envconfig:"POSTGRES_HOST"         default:"postgres"`
	PostgresPort     int    `envconfig:"POSTGRES_PORT"         default:"5432"`
	AllowedOrigin    string `envconfig:"ALLOWED_ORIGIN"        default:"*"`
	TelegramToken    string `envconfig:"TELEGRAM_BOT_TOKEN"`
	TelegramBotName  string `envconfig:"TELEGRAM_BOT_USERNAME"`
	// TelegramProxyURLs is an optional comma-separated list of proxies used ONLY for
	// the Telegram bot (api.telegram.org is unreachable from some networks, e.g. RU).
	// Each entry is user:pass@host:port or a full scheme URL; the bot tries them in
	// order until one authorises. Empty = connect directly.
	TelegramProxyURLs []string `envconfig:"TELEGRAM_PROXY_URL"`
	// TelegramAdmins — кому доступны админские вкладки «Журнал» и «Скорость».
	// Список записей вида 123456789 (chat_id) или @username, через запятую.
	// Роль выдаётся при /start в боте и пересчитывается при каждом /start.
	TelegramAdmins []string `envconfig:"TELEGRAM_ADMINS"`
	MOEXPollMs     int      `envconfig:"MOEX_POLL_INTERVAL_MS" default:"250"`
	// RobotsEnabled включает поиск роботов в ленте сделок (страница «Роботы»).
	RobotsEnabled bool `envconfig:"ROBOTS_ENABLED" default:"true"`
	// RobotsSymbols — за какими тикерами следить, через запятую. Запись без префикса
	// это акция основного режима (TQBR), с префиксом futures: — контракт FORTS.
	// Пусто — список по умолчанию (ликвидные бумаги + валютные фьючерсы).
	RobotsSymbols string `envconfig:"ROBOTS_SYMBOLS"`
	// RobotsPollMs — как часто опрашивается лента каждого тикера. На точность
	// тайминга не влияет (период считается по биржевым меткам сделок), влияет
	// только на скорость появления робота на странице и на нагрузку на ISS.
	RobotsPollMs int    `envconfig:"ROBOTS_POLL_MS" default:"3000"`
	// TInvestToken — токен T-Invest API. Пусто — поиск роботов работает по
	// публичной ленте MOEX ISS, а она приходит с задержкой в 15 минут.
	TInvestToken string `envconfig:"TINVEST_TOKEN"`
	Port         int    `envconfig:"BACKEND_PORT"          default:"8080"`
	LogLevel     string `envconfig:"LOG_LEVEL"             default:"info"`
}

func Load() (*Config, error) {
	var cfg Config
	if err := envconfig.Process("", &cfg); err != nil {
		return nil, err
	}
	// PaaS hosts (Render, Railway, Fly) inject the listen port via $PORT.
	// Prefer it over BACKEND_PORT so the same image runs unchanged in prod.
	if p := os.Getenv("PORT"); p != "" {
		if n, err := strconv.Atoi(p); err == nil {
			cfg.Port = n
		}
	}
	if cfg.PostgresPassword == "" && cfg.DatabaseURL == "" {
		return nil, fmt.Errorf("POSTGRES_PASSWORD or DATABASE_URL must be set")
	}
	if cfg.AllowedOrigin == "*" {
		fmt.Fprintln(os.Stderr, "WARNING: ALLOWED_ORIGIN=* — set to your frontend URL in production")
	}
	return &cfg, nil
}

func (c *Config) DSN() string {
	if c.DatabaseURL != "" {
		return c.DatabaseURL
	}
	return fmt.Sprintf("postgres://%s:%s@%s:%d/%s?sslmode=prefer",
		c.PostgresUser, c.PostgresPassword, c.PostgresHost, c.PostgresPort, c.PostgresDB)
}
