import { useCallback, useEffect, useState } from 'react';
import type { CBPublication } from '../types/funding';

const API_BASE = (import.meta.env.VITE_API_BASE as string | undefined) ?? '';

const fmtRate = new Intl.NumberFormat('ru-RU', { minimumFractionDigits: 4, maximumFractionDigits: 4 });
const fmtFund = new Intl.NumberFormat('ru-RU', { minimumFractionDigits: 6, maximumFractionDigits: 6 });

const fmtDay = new Intl.DateTimeFormat('ru-RU', {
  timeZone: 'UTC', day: '2-digit', month: '2-digit', year: 'numeric',
});
const fmtWeekday = new Intl.DateTimeFormat('ru-RU', { timeZone: 'UTC', weekday: 'short' });
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
function weekday(iso: string): string {
  try { return fmtWeekday.format(new Date(iso)); } catch { return ''; }
}
function clock(iso: string | null): string {
  if (!iso) return '—';
  try { return fmtClock.format(new Date(iso)); } catch { return iso; }
}
// predErr — предсказанный курс минус фактический, в копейках и процентах.
function predErr(pred: number | null, actual: number | null): string {
  if (pred == null || actual == null) return '—';
  const d = pred - actual;
  const pct = actual !== 0 ? (d / actual) * 100 : 0;
  return `${d >= 0 ? '+' : ''}${fmtRate.format(d)} (${pct >= 0 ? '+' : ''}${pct.toFixed(3)}%)`;
}
// dev — отклонение ноги фьючерса на 15:30 от курса ЦБ (settlVWAP − курс).
function dev(vwap: number | null, rateV: number | null): string {
  if (vwap == null || rateV == null) return '—';
  const d = vwap - rateV;
  return `${d >= 0 ? '+' : ''}${fmtRate.format(d)}`;
}
// diff — расхождение нашего расчёта с биржей (наш − факт SWAPRATE).
function diff(ours: number | null, actual: number | null): string {
  if (ours == null || actual == null) return '—';
  const d = ours - actual;
  return `${d >= 0 ? '+' : ''}${fmtFund.format(d)}`;
}

// ReconBlock — сверка НАШЕЙ реконструкции CBFunding с фактическим SWAPRATE биржи
// по одной валюте: видно ногу фьючерса, отклонение, наш расчёт (и без мёртвой зоны K1)
// и биржевой факт, чтобы точно локализовать расхождение.
function ReconBlock({ ccy, settlVwap, rateV, cb, cbNoDb, moex }: {
  ccy: string;
  settlVwap: number | null;
  rateV: number | null;
  cb: number | null;
  cbNoDb: number | null;
  moex: number | null;
}) {
  return (
    <div className="jrn-detail-group">
      <div className="jrn-detail-title">Сверка {ccy}: расчёт vs биржа</div>
      <Detail label="Нога фьючерса 15:30" value={rate(settlVwap)} muted={settlVwap == null} />
      <Detail label="Отклонение d (нога − курс)" value={dev(settlVwap, rateV)} muted={settlVwap == null || rateV == null} />
      <Detail label="CB funding (наш)" value={fund(cb)} muted={cb == null} />
      <Detail label="Без мёртвой зоны K1" value={fund(cbNoDb)} muted={cbNoDb == null} />
      <Detail label="MOEX SWAPRATE (факт)" value={fund(moex)} muted={moex == null} />
      <Detail label="Расхождение (наш − факт)" value={diff(cb, moex)} muted={cb == null || moex == null} />
    </div>
  );
}

// Detail — одна строка «метка / значение» в раскрывающейся части карточки.
function Detail({ label, value, muted }: { label: string; value: string; muted?: boolean }) {
  return (
    <div className="jrn-detail">
      <span className="jrn-detail-label">{label}</span>
      <span className={`jrn-detail-value${muted ? ' muted' : ''}`}>{value}</span>
    </div>
  );
}

// JournalCard — одна публикация. Самое важное (дата, когда курс пришёл, курсы)
// видно сразу; фандинг, прогноз и диагностика канала раскрываются по клику.
function JournalCard({ r }: { r: CBPublication }) {
  const published = r.detected_at != null;
  const hasRate = r.usd_rate != null || r.eur_rate != null || r.cny_rate != null;
  // Три состояния времени публикации:
  //  • есть detected_at → знаем момент до секунды;
  //  • курс есть, но времени нет (напр. после чистки фантомных строк) → «время неизвестно»;
  //  • ни курса, ни времени → строка ещё только с прогнозом.
  let caption: string;
  let timeVal: string;
  if (published) {
    caption = 'получено';
    timeVal = `${clock(r.detected_at)} МСК`;
  } else if (hasRate) {
    caption = 'получено';
    timeVal = 'время неизвестно';
  } else {
    caption = 'только прогноз';
    timeVal = 'курс ещё не пришёл';
  }
  return (
    <details className="jrn-card">
      <summary className="jrn-summary">
        <div className="jrn-date">
          <span className="jrn-weekday">{weekday(r.date)}</span>
          <span className="jrn-daymon">{day(r.date)}</span>
        </div>

        <div className={`jrn-time${published ? '' : ' jrn-time-pending'}`}>
          <span className="jrn-time-caption">{caption}</span>
          <span className="jrn-time-val">{timeVal}</span>
        </div>

        <div className="jrn-rates">
          <span className="jrn-rate"><i>USD</i>{rate(r.usd_rate)}</span>
          <span className="jrn-rate"><i>EUR</i>{rate(r.eur_rate)}</span>
          <span className="jrn-rate"><i>CNY</i>{rate(r.cny_rate)}</span>
        </div>

        <span className="jrn-chevron" aria-hidden="true">▸</span>
      </summary>

      <div className="jrn-details">
        <div className="jrn-detail-group">
          <div className="jrn-detail-title">Фандинг по факту</div>
          <Detail label="USD" value={fund(r.cb_funding_usd)} muted={r.cb_funding_usd == null} />
          <Detail label="EUR" value={fund(r.cb_funding_eur)} muted={r.cb_funding_eur == null} />
          <Detail label="CNY" value={fund(r.cny_funding)} muted={r.cny_funding == null} />
        </div>

        <div className="jrn-detail-group">
          <div className="jrn-detail-title">Прогноз до публикации</div>
          <Detail label="Фандинг USD" value={fund(r.predicted_funding_usd)} muted={r.predicted_funding_usd == null} />
          <Detail label="Фандинг EUR" value={fund(r.predicted_funding_eur)} muted={r.predicted_funding_eur == null} />
          <Detail label="Курс USD → ошибка" value={predErr(r.predicted_cb_rate_usd, r.usd_rate)} muted={r.predicted_cb_rate_usd == null} />
        </div>

        <ReconBlock ccy="USD" settlVwap={r.settl_vwap_usd} rateV={r.usd_rate}
          cb={r.cb_funding_usd} cbNoDb={r.cb_funding_no_deadband_usd} moex={r.moex_funding_usd} />
        <ReconBlock ccy="EUR" settlVwap={r.settl_vwap_eur} rateV={r.eur_rate}
          cb={r.cb_funding_eur} cbNoDb={r.cb_funding_no_deadband_eur} moex={r.moex_funding_eur} />

        <div className="jrn-detail-group">
          <div className="jrn-detail-title">Гонка каналов ЦБ</div>
          <Detail label="Первый канал" value={r.winner_channel ?? '—'} muted={r.winner_channel == null} />
          <Detail label="Задержка" value={r.winner_latency_ms != null ? `${r.winner_latency_ms} мс` : '—'} muted={r.winner_latency_ms == null} />
        </div>
      </div>
    </details>
  );
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
        какой канал ЦБ оказался первым. В блоке «Сверка» — нога фьючерса на 15:30, наш расчёт
        (и без мёртвой зоны K1) против фактического SWAPRATE биржи, чтобы видеть расхождения.
        Нажмите на карточку, чтобы раскрыть подробности.
      </p>

      {error && <p className="race-error">Ошибка загрузки: {error}</p>}

      {!error && rows.length === 0 && !loading && (
        <p className="race-empty">
          Пока нет ни одной записи. Строка появляется в день публикации курса ЦБ (≈16:00–16:30 МСК буднего дня).
        </p>
      )}

      {rows.length > 0 && (
        <div className="jrn-list">
          {rows.map((r) => <JournalCard key={r.date} r={r} />)}
        </div>
      )}
    </div>
  );
}
