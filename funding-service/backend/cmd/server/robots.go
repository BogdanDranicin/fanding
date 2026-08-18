package main

import (
	"context"
	"time"

	"github.com/rs/zerolog"

	"github.com/funding-service/backend/internal/api"
	"github.com/funding-service/backend/internal/config"
	"github.com/funding-service/backend/internal/robots"
	"github.com/funding-service/backend/internal/source/moexiss"
	"github.com/funding-service/backend/internal/source/tinvest"
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

	// Быстрый источник — поток обезличенных сделок брокера: биржевая лента ISS
	// приходит с задержкой ровно в пятнадцать минут, а поиску роботов важна
	// свежесть принта. Без токена сбор работает как раньше, только по ISS.
	if cfg.TInvestToken != "" {
		client, err := tinvest.Dial(context.Background(), tinvest.Config{
			Token:   cfg.TInvestToken,
			AppName: "funding-service/robots",
		})
		if err != nil {
			// Не валим сервис: запасной источник на месте, робот просто будет
			// виден на четверть часа позже.
			log.Error().Err(err).Msg("robots: быстрый источник недоступен, работаю по ISS")
		} else {
			opts.Stream = newTInvestSource(client)
			opts.StreamRetry = tinvest.ReconnectDelay
		}
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
