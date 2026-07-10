import { useCallback, useEffect, useState } from 'react';
import type { CBPublication } from '../types/funding';

const API_BASE = (import.meta.env.VITE_API_BASE as string | undefined) ?? '';

const fmtRate = new Intl.NumberFormat('ru-RU', { minimumFractionDigits: 4, maximumFractionDigits: 4 });
const fmtFund = new Intl.NumberFormat('ru-RU', { minimumFractionDigits: 6, maximumFractionDigits: 6 });

const fmtDay = new Intl.DateTimeFormat('ru-RU', {
  timeZone: 'UTC', day: '2-digit', month: '2-digit', year: 'numeric',
});
const fmtClock = new Intl.DateTimeFormat('ru-RU', {
  timeZone: 'Europe/Moscow', hour: '2-digit', minute: '2-digit', second: '2-digit',
});

function rate(v: number | null): string {
  return v != null ? fmtRate.format(v) : '—';
}
function fund(v: number | null): string {
  return v != null ? fmtFund.format(v) : '—';
}
function day(iso: string): string {
  try { return fmtDay.format(new Date(iso)); } catch { return iso; }
}
function clock(iso: string | null): string {
  if (!iso) return '—';
  try { return `${fmtClock.format(new Date(iso))} МСК`; } catch { return iso; }
}
// predErr — предсказанный курс минус фактический, в копейках и процентах.
function predErr(pred: number | null, actual: number | null): string {
  if (pred == null || actual == null) return '—';
  const d = pred - actual;
  const pct = actual !== 0 ? (d / actual) * 100 : 0;
  return `${d >= 0 ? '+' : ''}${fmtRate.format(d)} (${pct >= 0 ? '+' : ''}${pct.toFixed(3)}%)`;
}

export function JournalPage() {
  const [rows, setRows] = useState<CBPublication[]>([]);
  const [loading, setLoading] = useState(true); // we fetch on mount
  const [error, setError] = useState<string | null>(null);

  // load performs no synchronous setState: the first state update happens only
  // after the fetch resolves, so it is safe to call directly from an effect.
  const load = useCallback(async () => {
    try {
      const resp = await fetch(`${API_BASE}/api/v1/cb-publications?days=90`);
      if (!resp.ok) throw new Error(`HTTP ${resp.status}`);
      const data = (await resp.json()) as CBPublication[] | null;
      setRows(data ?? []);
      setError(null);
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    } finally {
      setLoading(false);
    }
  }, []);

  // One-shot fetch on mount. load() only updates state after the fetch resolves;
  // the rule can't see through the async boundary, so it is suppressed here.
  // eslint-disable-next-line react-hooks/set-state-in-effect
  useEffect(() => { void load(); }, [load]);

  const refresh = () => { setLoading(true); void load(); };

  return (
    <div className="race-page">
      <div className="race-header">
        <h2 className="race-title">Журнал публикаций курсов ЦБ</h2>
        <button className="race-btn-run" onClick={refresh} disabled={loading}>
          {loading ? '⏳ Загрузка…' : '↻ Обновить'}
        </button>
      </div>

      <p className="race-subtitle">
        Аудит каждой публикации: во сколько (до секунды по МСК) наш сервис получил новый курс,
        какие курсы пришли, какой фандинг по ним рассчитан, каким был прогноз до публикации и
        какой канал ЦБ оказался первым. Данные накапливаются день за днём.
      </p>

      {error && <p className="race-error">Ошибка загрузки: {error}</p>}

      {!error && rows.length === 0 && !loading && (
        <p className="race-empty">
          Пока нет ни одной записи. Строка появляется в день публикации курса ЦБ (≈16:00–16:30 МСК буднего дня).
        </p>
      )}

      {rows.length > 0 && (
        <div className="race-table-wrap">
          <table className="race-table journal-table">
            <thead>
              <tr>
                <th>Дата</th>
                <th>Опубликовано у нас</th>
                <th>USD/RUB</th>
                <th>EUR/RUB</th>
                <th>CNY/RUB</th>
                <th>Фандинг USD</th>
                <th>Фандинг EUR</th>
                <th>Фандинг CNY</th>
                <th>Прогноз ф. USD</th>
                <th>Прогноз ф. EUR</th>
                <th>Прогноз курса USD → ошибка</th>
                <th>Первый канал</th>
                <th>Задержка</th>
              </tr>
            </thead>
            <tbody>
              {rows.map((r) => (
                <tr key={r.date}>
                  <td>{day(r.date)}</td>
                  <td className="race-timestamp">{clock(r.detected_at)}</td>
                  <td className="race-value">{rate(r.usd_rate)}</td>
                  <td className="race-value">{rate(r.eur_rate)}</td>
                  <td className="race-value">{rate(r.cny_rate)}</td>
                  <td className="race-value">{fund(r.cb_funding_usd)}</td>
                  <td className="race-value">{fund(r.cb_funding_eur)}</td>
                  <td className="race-value">{fund(r.cny_funding)}</td>
                  <td className="race-value">{fund(r.predicted_funding_usd)}</td>
                  <td className="race-value">{fund(r.predicted_funding_eur)}</td>
                  <td className="race-value">{predErr(r.predicted_cb_rate_usd, r.usd_rate)}</td>
                  <td className="race-src-label">{r.winner_channel ?? '—'}</td>
                  <td className="race-time">{r.winner_latency_ms != null ? `${r.winner_latency_ms} мс` : '—'}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </div>
  );
}
