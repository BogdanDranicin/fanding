import { useState, useCallback, useEffect, useRef } from 'react';
import { useIsMobile } from '../hooks/useIsMobile';
import { useRaceStore, parseDateNum, type CBRRaceResult } from '../store/raceStore';

const fmtRate = new Intl.NumberFormat('ru-RU', { minimumFractionDigits: 4, maximumFractionDigits: 4 });
const fmtTime = new Intl.DateTimeFormat('ru-RU', { hour: '2-digit', minute: '2-digit', second: '2-digit' });
const MEDALS = ['🥇', '🥈', '🥉', '4️⃣'];
const API_BASE = (import.meta.env.VITE_API_BASE as string | undefined) ?? '';

function fmtTimestamp(ts?: string): string {
  if (!ts) return '';
  try {
    return new Intl.DateTimeFormat('ru-RU', {
      day: '2-digit', month: '2-digit',
      hour: '2-digit', minute: '2-digit', second: '2-digit',
    }).format(new Date(ts));
  } catch {
    return ts;
  }
}

function secDelta(isoA: string, isoBase: string): string {
  const diff = Math.round((new Date(isoA).getTime() - new Date(isoBase).getTime()) / 1000);
  if (diff <= 0) return '';
  return `+${diff} с`;
}

export function RacePage() {
  const [running, setRunning] = useState(false);
  const runningRef = useRef(false);
  const [autoSec, setAutoSec] = useState(0);
  const { rounds, priorDate, latestDate, detections, addRound, clearHistory } = useRaceStore();
  const isMobile = useIsMobile();

  const runRace = useCallback(async () => {
    if (runningRef.current) return;
    runningRef.current = true;
    setRunning(true);
    const startedAt = new Date();
    try {
      const resp = await fetch(`${API_BASE}/api/v1/cbr-race`);
      if (!resp.ok) throw new Error(`HTTP ${resp.status}`);
      const results: CBRRaceResult[] = await resp.json();
      addRound(startedAt, results);
    } catch (e) {
      const msg = e instanceof Error ? e.message : String(e);
      addRound(startedAt, [{
        source_id: 'error', name: 'Ошибка запроса', desc: '', rate_date: '',
        usd_rub: 0, eur_rub: 0, cny_rub: 0, latency_ms: 0, error: msg,
      }]);
    } finally {
      runningRef.current = false;
      setRunning(false);
    }
  }, [addRound]);

  useEffect(() => {
    if (!autoSec) return;
    const timer = setInterval(runRace, autoSec * 1000);
    return () => clearInterval(timer);
  }, [autoSec, runRace]);

  const latest = rounds[0];

  // True only when latestDate has advanced past the baseline (priorDate)
  const newPublicationDetected =
    !!latestDate && !!priorDate && parseDateNum(latestDate) > parseDateNum(priorDate);

  // Detection timeline sorted by firstSeenAt, then latency as tiebreaker
  const sortedDetections = Object.values(detections).sort((a, b) => {
    const ta = new Date(a.firstSeenAt).getTime();
    const tb = new Date(b.firstSeenAt).getTime();
    return ta !== tb ? ta - tb : a.latency_ms - b.latency_ms;
  });

  // Sources in the latest round that haven't shown latestDate yet
  const pendingSources = (latest?.results ?? []).filter(
    (r) => !r.error && !detections[r.source_id]
  );

  const baseTime = sortedDetections[0]?.firstSeenAt;

  return (
    <div className="race-page">
      <div className="race-header">
        <h2 className="race-title">Гонка источников курсов ЦБ РФ</h2>
      </div>

      <p className="race-subtitle">
        Параллельный опрос всех источников курса ЦБ РФ. Запускайте в 16:30–18:00 МСК каждые 10–30 секунд —
        страница запоминает, в какой момент каждый источник впервые показал новую дату курса.
        Данные сохраняются в браузере между перезагрузками.
      </p>

      {/* ── Controls ── */}
      <div className="race-controls">
        <button className="race-btn-run" onClick={runRace} disabled={running}>
          {running ? '⏳ Запрос…' : '▶ Запустить'}
        </button>
        <span className="race-auto-label">Авто:</span>
        {[0, 10, 30, 60].map((s) => (
          <button
            key={s}
            className={`filter-btn${autoSec === s ? ' filter-btn-active' : ''}`}
            onClick={() => setAutoSec(s)}
          >
            {s === 0 ? 'выкл' : `${s}с`}
          </button>
        ))}
        <button
          className="race-btn-clear"
          onClick={() => { clearHistory(); setAutoSec(0); }}
        >
          Очистить
        </button>
      </div>

      {/* ── Status / Publication timeline ── */}
      {priorDate && (
        <div className="race-card">
          {newPublicationDetected ? (
            <>
              <div className="race-card-title">
                Новый курс опубликован
                <span className="race-log-count">📅 {latestDate}</span>
              </div>
              <div className="race-detection-list">
                {sortedDetections.map((det, i) => (
                  <div key={det.source_id} className="race-detection-item race-detection-found">
                    <span className="race-medal">{MEDALS[i] ?? String(i + 1)}</span>
                    <span className="race-src-label">{det.name}</span>
                    <span className="race-detection-time">
                      {fmtTime.format(new Date(det.firstSeenAt))}
                      {i > 0 && baseTime && (
                        <span className="race-detection-delta"> {secDelta(det.firstSeenAt, baseTime)}</span>
                      )}
                    </span>
                  </div>
                ))}
                {pendingSources.map((r) => (
                  <div key={r.source_id} className="race-detection-item race-detection-pending">
                    <span className="race-medal">—</span>
                    <span className="race-src-label">{r.name}</span>
                    <span className="race-detection-time race-time">ещё не обновился</span>
                  </div>
                ))}
              </div>
            </>
          ) : (
            <>
              <div className="race-card-title">
                Ожидаем публикацию
                <span className="race-log-count">📅 базовая дата: {priorDate}</span>
              </div>
              <p className="race-empty">
                Новый курс ещё не вышел ни в одном источнике.
                Запускайте каждые 10–30 с в 16:30–18:00 МСК.
              </p>
            </>
          )}
          <p className="race-empty" style={{ marginTop: 4 }}>
            запросов: {rounds.length} · данные сохранены в браузере
          </p>
        </div>
      )}

      {/* ── Latest round raw results ── */}
      {latest && (
        <div className="race-card">
          <div className="race-card-title">
            Последний запрос
            <span className="race-log-count">{fmtTime.format(new Date(latest.startedAt))}</span>
          </div>

          {isMobile ? (
            <div className="race-result-list">
              {latest.results.map((res) => {
                const detIdx = sortedDetections.findIndex((d) => d.source_id === res.source_id);
                const isDetected = detIdx >= 0;
                return (
                  <div
                    key={res.source_id}
                    className={`race-result-item${isDetected ? ' race-result-winner' : ''}${res.error ? ' race-result-error' : ''}`}
                  >
                    <div className="race-result-row1">
                      <span className="race-medal">
                        {res.error ? '❌' : isDetected ? (MEDALS[detIdx] ?? String(detIdx + 1)) : ''}
                      </span>
                      <span className="race-src-label">{res.name}</span>
                      <span className={`race-date${isDetected ? ' race-date-new' : ''}`}>
                        {res.rate_date || '—'}
                      </span>
                      <span className="race-time">{res.latency_ms} мс</span>
                    </div>
                    {res.error ? (
                      <div className="race-result-sub race-error" title={res.error}>{res.error}</div>
                    ) : (
                      <div className="race-result-sub">
                        <span>USD {res.usd_rub ? fmtRate.format(res.usd_rub) : '—'}</span>
                        <span>EUR {res.eur_rub ? fmtRate.format(res.eur_rub) : '—'}</span>
                        <span>CNY {res.cny_rub ? fmtRate.format(res.cny_rub) : '—'}</span>
                        {res.timestamp && (
                          <span className="race-timestamp">{fmtTimestamp(res.timestamp)}</span>
                        )}
                      </div>
                    )}
                  </div>
                );
              })}
            </div>
          ) : (
            <div className="race-table-wrap">
              <table className="race-table">
                <thead>
                  <tr>
                    <th>Место</th>
                    <th>Источник</th>
                    <th>Описание</th>
                    <th>Дата курса</th>
                    <th>Зеркало обновлено</th>
                    <th>USD/RUB</th>
                    <th>EUR/RUB</th>
                    <th>CNY/RUB</th>
                    <th>Задержка</th>
                  </tr>
                </thead>
                <tbody>
                  {latest.results.map((res) => {
                    const detIdx = sortedDetections.findIndex((d) => d.source_id === res.source_id);
                    const isDetected = detIdx >= 0;
                    return (
                      <tr
                        key={res.source_id}
                        className={res.error ? 'race-row-error' : isDetected ? 'race-row-winner' : ''}
                      >
                        <td className="race-medal">
                          {res.error ? '❌' : isDetected ? (MEDALS[detIdx] ?? String(detIdx + 1)) : ''}
                        </td>
                        <td className="race-src-label">{res.name}</td>
                        <td className="race-src-desc">{res.desc}</td>
                        <td className={`race-date${isDetected ? ' race-date-new' : ''}`}>
                          {res.rate_date || '—'}
                        </td>
                        <td className="race-timestamp">{fmtTimestamp(res.timestamp)}</td>
                        <td className="race-value">
                          {res.usd_rub
                            ? fmtRate.format(res.usd_rub)
                            : res.error
                              ? <span className="race-error" title={res.error}>ошибка</span>
                              : '—'}
                        </td>
                        <td className="race-value">{res.eur_rub ? fmtRate.format(res.eur_rub) : '—'}</td>
                        <td className="race-value">{res.cny_rub ? fmtRate.format(res.cny_rub) : '—'}</td>
                        <td className="race-time">{res.latency_ms} мс</td>
                      </tr>
                    );
                  })}
                </tbody>
              </table>
            </div>
          )}
        </div>
      )}

      {/* ── History ── */}
      {rounds.length > 1 && (
        <div className="race-card">
          <div className="race-card-title">
            История
            <span className="race-log-count">{rounds.length} запусков</span>
            <button
              className="race-btn-clear"
              style={{ marginLeft: 'auto' }}
              onClick={() => { clearHistory(); setAutoSec(0); }}
            >
              Очистить
            </button>
          </div>
          <div className="race-log">
            {rounds.slice(1).map((round) => {
              // Sources that were first detected for latestDate in this specific round
              const newlyDetected = round.results.filter(
                (r) => !r.error && detections[r.source_id]?.firstSeenAt === round.startedAt
              );
              return (
                <div key={round.id} className="race-round">
                  <div className="race-round-header">
                    <span className="race-time">{fmtTime.format(new Date(round.startedAt))}</span>
                    {newlyDetected.map((r) => {
                      const detIdx = sortedDetections.findIndex((d) => d.source_id === r.source_id);
                      return (
                        <span key={r.source_id} className="race-round-winner">
                          {MEDALS[detIdx] ?? '✓'} {r.name}
                        </span>
                      );
                    })}
                  </div>
                  {isMobile ? (
                    <div className="race-result-list race-result-list-sm">
                      {round.results.map((res) => {
                        const detIdx = sortedDetections.findIndex((d) => d.source_id === res.source_id);
                        const isDetected = detIdx >= 0;
                        return (
                          <div
                            key={res.source_id}
                            className={`race-result-item race-result-item-sm${isDetected ? ' race-result-winner' : ''}`}
                          >
                            <span className="race-medal">
                              {res.error ? '❌' : isDetected ? (MEDALS[detIdx] ?? '') : ''}
                            </span>
                            <span className="race-src-label">{res.name}</span>
                            <span className="race-date">{res.rate_date || '—'}</span>
                            <span className="race-time">{res.latency_ms} мс</span>
                          </div>
                        );
                      })}
                    </div>
                  ) : (
                    <table className="race-table race-table-round">
                      <tbody>
                        {round.results.map((res) => {
                          const detIdx = sortedDetections.findIndex((d) => d.source_id === res.source_id);
                          const isDetected = detIdx >= 0;
                          return (
                            <tr key={res.source_id} className={isDetected ? 'race-row-winner' : ''}>
                              <td className="race-medal">
                                {res.error ? '❌' : isDetected ? (MEDALS[detIdx] ?? '') : ''}
                              </td>
                              <td className="race-src-label">{res.name}</td>
                              <td className="race-date">{res.rate_date || '—'}</td>
                              <td className="race-time">{res.latency_ms} мс</td>
                              <td className="race-value">
                                {res.usd_rub ? fmtRate.format(res.usd_rub) : (res.error ?? '—')}
                              </td>
                            </tr>
                          );
                        })}
                      </tbody>
                    </table>
                  )}
                </div>
              );
            })}
          </div>
        </div>
      )}

      {rounds.length === 0 && !running && (
        <p className="race-empty">
          Нажмите «Запустить» чтобы опросить все источники.
          Запускайте в 16:30–18:00 МСК каждые 10–30 с — страница поймает момент,
          когда каждый источник впервые покажет новую дату курса.
        </p>
      )}
    </div>
  );
}
