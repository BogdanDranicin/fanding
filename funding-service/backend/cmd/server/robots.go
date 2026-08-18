package main

import (
	"time"

	"github.com/rs/zerolog"

	"github.com/funding-service/backend/internal/api"
	"github.com/funding-service/backend/internal/config"
	"github.com/funding-service/backend/internal/robots"
	"github.com/funding-service/backend/internal/source/moexiss"
	"github.com/funding-service/backend/internal/storage"
)

// newRobotCollector собирает поиск роботов по конфигурации. Возвращает nil, если
// сбор выключен; ошибку — только на нечитаемом списке тикеров.
func newRobotCollector(cfg *config.Config, store *storage.Store, log zerolog.Logger) (*robots.Collector, error) {
	if !cfg.RobotsEnabled {
		return nil, nil
	}

	opts := robots.DefaultCollectorOptions()
	// Ленты опрашиваются целиком (весь основной режим акций и весь срочный рынок),
	// а ROBOTS_SYMBOLS лишь сужает, какие инструменты из них берутся в работу.
	watch, err := robots.NewWatchlist(cfg.RobotsSymbols)
	if err != nil {
		return nil, err
	}
	opts.Watch = watch
	if cfg.RobotsPollMs > 0 {
		opts.PollInterval = time.Duration(cfg.RobotsPollMs) * time.Millisecond
	}

	// Свой HTTP-клиент ISS: лента роботов тянет тысячи сделок за опрос и не должна
	// делить пул соединений с опросом котировок, на котором висит фандинг.
	return robots.NewCollector(moexiss.NewClient(), store, opts, log), nil
}

// robotSource оборачивает коллектор в интерфейс для API, аккуратно обходя ловушку
// с nil-указателем в непустом интерфейсе.
func robotSource(c *robots.Collector) api.RobotSource {
	if c == nil {
		return nil
	}
	return c
}
