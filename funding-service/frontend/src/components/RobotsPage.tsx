import { useCallback, useEffect, useMemo, useState } from 'react';
import type { RobotSession, RobotsResponse } from '../types/robots';
import { authFetch } from '../api/auth';

const fmtClock = new Intl.DateTimeFormat('ru-RU', {
  timeZone: 'Europe/Moscow', hour: '2-digit', minute: '2-digit', second: '2-digit',
});
const fmtDayClock = new Intl.DateTimeFormat('ru-RU', {
  timeZone: 'Europe/Moscow', day: '2-digit', month: '2-digit',
  hour: '2-digit', minute: '2-digit', second: '2-digit',
});
const fmtLots = new Intl.NumberFormat('ru-RU');

function clock(iso: string): string {
  try { return fmtClock.format(new Date(iso)); } catch { return iso; }
}
function dayClock(iso: string): string {
  try { return fmtDayClock.format(new Date(iso)); } catch { return iso; }
}

// lots — лотовка: одно число, если робот печатает строго один размер, иначе диапазон.
function lots(r: RobotSession): string {
  const lo = Math.round(r.qty_min);
  const hi = Math.round(r.qty_max);
  return lo === hi ? `${fmtLots.format(lo)} л` : `${fmtLots.format(lo)}–${fmtLots.format(hi)} л`;
}

// period — тайминг. Секунды до десятых, дальше минуты: «11.2 с», «2 мин 05 с».
function period(sec: number): string {
  if (sec < 60) return `${sec.toFixed(1)} с`;
  const m = Math.floor(sec / 60);
  const s = Math.round(sec - m * 60);
  return `${m} мин ${String(s).padStart(2, '0')} с`;
}

// duration — сколько робот работает, от первого принта серии до последнего.
function duration(r: RobotSession): string {
  const ms = new Date(r.last_seen).getTime() - new Date(r.first_seen).getTime();
  if (!Number.isFinite(ms) || ms < 0) return '—';
  const total = Math.round(ms / 1000);
  const h = Math.floor(total / 3600);
  const m = Math.floor((total - h * 3600) / 60);
  const s = total - h * 3600 - m * 60;
  if (h > 0) return `${h} ч ${String(m).padStart(2, '0')} мин`;
  if (m > 0) return `${m} мин ${String(s).padStart(2, '0')} с`;
  return `${s} с`;
}

function price(v: number): string {
  return v.toLocaleString('ru-RU', { maximumFractionDigits: 4 });
}

// confidenceLabel — уверенность словами: цифра 0.62 читателю ничего не говорит.
function confidenceLabel(c: number): string {
  if (c >= 0.75) return 'высокая';
  if (c >= 0.5) return 'средняя';
  return 'низкая';
}

function Detail({ label, value }: { label: string; value: string }) {
  return (
    <div className="jrn-detail">
      <span className="jrn-detail-label">{label}</span>
      <span className="jrn-detail-value">{value}</span>
    </div>
  );
}

// RobotCard — одна найденная закономерность. В свёрнутом виде — то, что и просили
// видеть сразу: тикер, направление, лотовка и тайминг. Остальное раскрывается.
function RobotCard({ r }: { r: RobotSession }) {
  const long = r.side === 'B';
  return (
    <details className="jrn-card rb-card">
      <summary className="jrn-summary rb-summary">
        <div className="rb-col rb-symbol">
          <span className="rb-caption">тикер</span>
          <span className="rb-symbol-val">{r.symbol}</span>
        </div>

        <div className="rb-col">
          <span className="rb-caption">направление</span>
          <span className={`rb-side ${long ? 'rb-long' : 'rb-short'}`}>
            {long ? 'ЛОНГ' : 'ШОРТ'}
          </span>
        </div>

        <div className="rb-col">
          <span className="rb-caption">лотовка</span>
          <span className="rb-val">{lots(r)}</span>
        </div>

        <div className="rb-col">
          <span className="rb-caption">тайминг</span>
          <span className="rb-val rb-period">{period(r.period_sec)}</span>
        </div>

        <div className="rb-col">
          <span className="rb-caption">повторов</span>
          <span className="rb-val">{r.prints}</span>
        </div>

        <div className="rb-col rb-status-col">
          <span className="rb-caption">статус</span>
          {r.active
            ? <span className="rb-status rb-active">работает</span>
            : <span className="rb-status rb-stopped">замолчал в {clock(r.last_seen)}</span>}
        </div>

        <span className="jrn-chevron" aria-hidden="true">▸</span>
      </summary>

      <div className="jrn-details">
        <div className="jrn-detail-group">
          <div className="jrn-detail-title">Серия</div>
          <Detail label="Первый принт" value={dayClock(r.first_seen)} />
          <Detail label="Последний принт" value={dayClock(r.last_seen)} />
          <Detail label="Работает" value={duration(r)} />
          <Detail label="Тактов периода" value={String(r.beats)} />
        </div>

        <div className="jrn-detail-group">
          <div className="jrn-detail-title">Лотовка и объём</div>
          <Detail label="Типичный размер" value={`${fmtLots.format(Math.round(r.qty_typical))} л`} />
          <Detail label="Границы размера" value={lots(r)} />
          <Detail label="Объём серии" value={`≈ ${fmtLots.format(Math.round(r.qty_typical * r.prints))} л`} />
        </div>

        <div className="jrn-detail-group">
          <div className="jrn-detail-title">Тайминг</div>
          <Detail label="Период" value={period(r.period_sec)} />
          <Detail label="Разброс интервалов" value={`±${(r.jitter * 100).toFixed(1)}%`} />
          <Detail label="Уверенность" value={`${confidenceLabel(r.confidence)} (${r.confidence.toFixed(2)})`} />
        </div>

        <div className="jrn-detail-group">
          <div className="jrn-detail-title">Цена</div>
          <Detail label="На первом принте" value={price(r.price_first)} />
          <Detail label="На последнем" value={price(r.price_last)} />
          <Detail label="Замечен сервисом" value={dayClock(r.detected_at)} />
        </div>
      </div>
    </details>
  );
}

type Tab = 'live' | 'history';

export function RobotsPage() {
  const [tab, setTab] = useState<Tab>('live');
  const [rows, setRows] = useState<RobotSession[]>([]);
  const [watching, setWatching] = useState<string[]>([]);
  const [symbol, setSymbol] = useState('');
  const [activeOnly, setActiveOnly] = useState(false);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);

  const load = useCallback(async (which: Tab) => {
    try {
      const path = which === 'live' ? '/api/v1/robots' : '/api/v1/robots/history?days=7&limit=500';
      const resp = await authFetch(path);
      if (!resp.ok) throw new Error(`HTTP ${resp.status}`);
      if (which === 'live') {
        const data = (await resp.json()) as RobotsResponse;
        setRows(data.robots ?? []);
        setWatching(data.watching ?? []);
      } else {
        setRows(((await resp.json()) as RobotSession[] | null) ?? []);
      }
      setError(null);
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    } finally {
      setLoading(false);
    }
  }, []);

  // Первая загрузка и смена вкладки. load() трогает состояние только после ответа
  // сервера, поэтому правило про setState в эффекте здесь неприменимо.
  // eslint-disable-next-line react-hooks/set-state-in-effect
  useEffect(() => { void load(tab); }, [load, tab]);

  // Живая вкладка обновляется сама: робот появляется и замолкает без перезагрузки.
  useEffect(() => {
    if (tab !== 'live') return;
    const id = setInterval(() => { void load('live'); }, 15000);
    return () => clearInterval(id);
  }, [load, tab]);

  const shown = useMemo(
    () => rows.filter((r) => (!symbol || r.symbol === symbol) && (!activeOnly || r.active)),
    [rows, symbol, activeOnly],
  );

  const symbols = useMemo(() => {
    const set = new Set<string>(watching);
    rows.forEach((r) => set.add(r.symbol));
    return [...set].sort();
  }, [rows, watching]);

  const refresh = () => { setLoading(true); void load(tab); };

  return (
    <div className="race-page">
      <div className="race-header">
        <h2 className="race-title">Поиск роботов</h2>
        <button className="race-btn-run" onClick={refresh} disabled={loading}>
          {loading ? '⏳ Загрузка…' : '↻ Обновить'}
        </button>
      </div>

      <p className="race-subtitle">
        Сервис пишет каждый принт по каждому тикеру и ищет повторы: если сделка одного
        размера проходит через ровные промежутки времени — это робот. В таблице видно, на чём
        он работает, в какую сторону, каким объёмом и с каким периодом. Период считается
        по биржевым меткам сделок, поэтому доли секунды восстанавливаются даже там, где
        биржа отдаёт время с точностью до секунды. Нажмите на строку, чтобы раскрыть подробности.
      </p>

      <div className="rb-controls">
        <div className="rb-tabs">
          <button
            className={`rb-tab${tab === 'live' ? ' rb-tab-active' : ''}`}
            onClick={() => { setTab('live'); setLoading(true); }}
          >
            Сейчас
          </button>
          <button
            className={`rb-tab${tab === 'history' ? ' rb-tab-active' : ''}`}
            onClick={() => { setTab('history'); setLoading(true); }}
          >
            История за неделю
          </button>
        </div>

        <label className="rb-filter">
          Тикер
          <select value={symbol} onChange={(e) => setSymbol(e.target.value)}>
            <option value="">все</option>
            {symbols.map((s) => <option key={s} value={s}>{s}</option>)}
          </select>
        </label>

        {tab === 'live' && (
          <label className="rb-checkbox">
            <input
              type="checkbox"
              checked={activeOnly}
              onChange={(e) => setActiveOnly(e.target.checked)}
            />
            только работающие
          </label>
        )}
      </div>

      {error && <p className="race-error">Ошибка загрузки: {error}</p>}

      {watching.length > 0 && (
        <p className="rb-watching">Следим за лентой: {watching.join(', ')}</p>
      )}

      {!error && shown.length === 0 && !loading && (
        <p className="race-empty">
          {tab === 'live'
            ? 'Сейчас закономерностей не видно. Роботы появляются в активные часы торгов; лента анализируется за последние 20 минут.'
            : 'За неделю ничего не сохранено.'}
        </p>
      )}

      {shown.length > 0 && (
        <div className="jrn-list">
          {shown.map((r) => <RobotCard key={`${r.id}-${r.symbol}-${r.first_seen}`} r={r} />)}
        </div>
      )}
    </div>
  );
}
