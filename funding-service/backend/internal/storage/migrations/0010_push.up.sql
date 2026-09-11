-- Push-уведомления: подписки браузеров и очередь будильников (11.09.2026).
--
-- Сигналы по времени и звук о публикации фандинга жили целиком в браузере, и
-- этого хватало ровно до того момента, когда браузер замораживает вкладку — в
-- замороженной странице не работают ни таймеры, ни звук, поставленный в очередь
-- заранее. Теперь расписание, посчитанное страницей, отправляется сюда, а сервис
-- будит браузер push-ом: сервис-воркер просыпается, даже если вкладка заморожена,
-- окно свёрнуто или браузер закрыт.
--
-- Подписка привязана к сессии браузера, а не к аккаунту: расписание сигналов у
-- каждого браузера своё (оно лежит в его localStorage), и «те же сигналы на
-- рабочем и домашнем компьютере» — это не то, что человек просил.
CREATE TABLE IF NOT EXISTS push_subscriptions (
    id         BIGSERIAL   PRIMARY KEY,
    session    TEXT        NOT NULL REFERENCES sessions (token) ON DELETE CASCADE,
    endpoint   TEXT        NOT NULL UNIQUE,
    p256dh     TEXT        NOT NULL,
    auth       TEXT        NOT NULL,
    -- funding — слать ли этому браузеру уведомление о публикации курса ЦБ.
    -- Это отдельная галочка в настройках («Проигрывать звук при публикации
    -- фандинга»), и она про звук, а не про сигналы по времени.
    funding    BOOLEAN     NOT NULL DEFAULT TRUE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    seen_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_push_subscriptions_session ON push_subscriptions (session);

-- Очередь будильников: что и когда разбудить. Страница кладёт сюда ближайшие
-- отметки своего расписания и перекладывает их заново при каждой правке, поэтому
-- строки живут часами, а не днями.
CREATE TABLE IF NOT EXISTS push_wakeups (
    id           BIGSERIAL   PRIMARY KEY,
    subscription BIGINT      NOT NULL REFERENCES push_subscriptions (id) ON DELETE CASCADE,
    fire_at      TIMESTAMPTZ NOT NULL,
    title        TEXT        NOT NULL,
    body         TEXT        NOT NULL DEFAULT '',
    tag          TEXT        NOT NULL DEFAULT '',
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Диспетчер спрашивает базу «что пора» раз в секунду: без индекса это был бы
-- полный проход по очереди каждую секунду.
CREATE INDEX IF NOT EXISTS idx_push_wakeups_fire_at ON push_wakeups (fire_at);
