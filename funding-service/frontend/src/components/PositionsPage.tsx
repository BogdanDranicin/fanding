import { useEffect, useState, useRef } from 'react';
import type { Position } from '../types/funding';
import { fetchPositions } from '../api/positions';

const fmtProfit = new Intl.NumberFormat('ru-RU', {
  minimumFractionDigits: 2,
  maximumFractionDigits: 2,
});

interface Props {
  onGoToSettings: () => void;
}

export function PositionsPage({ onGoToSettings }: Props) {
  const [positions, setPositions] = useState<Position[]>([]);
  const [status, setStatus] = useState<'loading' | 'ok' | 'not_configured' | 'error'>('loading');
  const [updatedAt, setUpdatedAt] = useState<Date | null>(null);
  const intervalRef = useRef<ReturnType<typeof setInterval> | null>(null);

  const load = async () => {
    try {
      const data = await fetchPositions();
      setPositions(data);
      setUpdatedAt(new Date());
      setStatus('ok');
    } catch (e: unknown) {
      if (e instanceof Error && e.message === 'not_configured') {
        setStatus('not_configured');
      } else {
        setStatus('error');
      }
    }
  };

  useEffect(() => {
    load();
    intervalRef.current = setInterval(load, 30_000);
    return () => {
      if (intervalRef.current) clearInterval(intervalRef.current);
    };
  }, []);

  const fmtTime = updatedAt
    ? updatedAt.toLocaleTimeString('ru-RU', { hour: '2-digit', minute: '2-digit', second: '2-digit' })
    : null;

  return (
    <div className="positions-page">
      {status === 'loading' && (
        <p style={{ color: 'var(--text-muted)', padding: 24 }}>Загрузка…</p>
      )}

      {status === 'not_configured' && (
        <div style={{ padding: 24, textAlign: 'center' }}>
          <p style={{ color: 'var(--text-muted)', marginBottom: 12 }}>
            Подключение не настроено
          </p>
          <button className="nav-link" onClick={onGoToSettings}>
            Перейти в настройки →
          </button>
        </div>
      )}

      {status === 'error' && (
        <p style={{ color: 'var(--accent-down)', padding: 24 }}>
          Ошибка загрузки позиций. Проверьте sso_session в настройках.
        </p>
      )}

      {status === 'ok' && (
        <>
          <div className="positions-header">
            <span className="positions-title">Активные позиции</span>
            {fmtTime && (
              <span style={{ color: 'var(--text-muted)', fontSize: 12 }}>
                обновлено {fmtTime}
              </span>
            )}
          </div>

          {positions.length === 0 ? (
            <p style={{ color: 'var(--text-muted)', padding: '12px 0' }}>
              Нет открытых позиций
            </p>
          ) : (
            <table className="funding-table">
              <thead>
                <tr>
                  <th>Символ</th>
                  <th>Биржа</th>
                  <th>Сторона</th>
                  <th className="cell">Кол-во</th>
                  <th className="cell">P&amp;L (₽)</th>
                  <th className="cell">P&amp;L %</th>
                  <th className="cell">Дата</th>
                </tr>
              </thead>
              <tbody>
                {positions.map((p, i) => (
                  <tr key={i}>
                    <th className="row-label">{p.symbol}</th>
                    <td className="cell" style={{ color: 'var(--text-muted)', fontSize: 12 }}>
                      {p.exchange}
                    </td>
                    <td className="cell">
                      <span className={p.side === 'buy' ? 'side-buy' : 'side-sell'}>
                        {p.side === 'buy' ? 'BUY' : 'SELL'}
                      </span>
                    </td>
                    <td className="cell">{p.pos}</td>
                    <td className={`cell ${p.unrealized_profit != null && p.unrealized_profit >= 0 ? 'funding-positive' : 'funding-negative'}`}>
                      {p.unrealized_profit != null ? fmtProfit.format(p.unrealized_profit) : '—'}
                    </td>
                    <td className={`cell ${p.unrealized_profit_pct != null && p.unrealized_profit_pct >= 0 ? 'funding-positive' : 'funding-negative'}`}>
                      {p.unrealized_profit_pct != null
                        ? `${p.unrealized_profit_pct >= 0 ? '+' : ''}${p.unrealized_profit_pct.toFixed(2)}%`
                        : '—'}
                    </td>
                    <td className="cell" style={{ color: 'var(--text-muted)', fontSize: 12 }}>
                      {p.date} {p.time}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          )}
        </>
      )}
    </div>
  );
}
