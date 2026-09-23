import { useCallback, useEffect, useRef, useState } from 'react';
import { ACTION_LABEL, directionLabel, fetchEvents, fetchLastEventId, price, type TradeEvent } from '../api/trades';

const POLL_MS = 4000;
const SHOW_MS = 15000;
// FRESH_MS — событие старше этого не всплывает: после сна ноутбука или
// разрыва сети страница догоняет ленту, и пачка вчерашних сделок на экране
// никому не нужна — они есть на вкладке «Сделки».
const FRESH_MS = 10 * 60 * 1000;
const NOTIFY: ReadonlySet<string> = new Set(['open', 'add', 'reduce', 'close']);

function Toast({ e, dismiss }: { e: TradeEvent; dismiss: (id: number) => void }) {
  useEffect(() => {
    const id = setTimeout(() => dismiss(e.id), SHOW_MS);
    return () => clearTimeout(id);
  }, [dismiss, e.id]);

  const parts = [
    `${ACTION_LABEL[e.action] ?? e.action} ${directionLabel(e.direction).toLowerCase()}`.trim(),
    e.size,
    e.price != null ? price(e.price) : '',
  ].filter(Boolean);

  return (
    <div className={`alm-toast trd-toast trd-toast-${e.action === 'close' ? 'close' : e.direction}`} role="status">
      <span className="alm-toast-mark">{e.ticker}</span>
      <span className="alm-toast-body">
        <span className="alm-toast-title">{e.author}</span>
        <span className="alm-toast-sub">{parts.join(' · ')}</span>
      </span>
      <button type="button" className="alm-toast-close" aria-label="Скрыть" onClick={() => dismiss(e.id)}>✕</button>
    </div>
  );
}

/** Всплывающие уведомления о новых сделках авторов. Рисуются поверх любой страницы. */
export function TradeToasts() {
  const [toasts, setToasts] = useState<TradeEvent[]>([]);
  const lastId = useRef<number | null>(null);
  const dismiss = useCallback((id: number) => setToasts((old) => old.filter((t) => t.id !== id)), []);

  useEffect(() => {
    let stopped = false;
    const poll = async () => {
      try {
        if (lastId.current == null) {
          lastId.current = await fetchLastEventId();
          return;
        }
        const events = await fetchEvents(lastId.current, 50);
        if (stopped || events.length === 0) return;
        lastId.current = events[events.length - 1].id;
        const now = Date.now();
        const fresh = events.filter((e) =>
          NOTIFY.has(e.action) && !e.silent && !e.manual && now - new Date(e.created_at).getTime() < FRESH_MS);
        if (fresh.length > 0) setToasts((old) => [...old, ...fresh].slice(-6));
      } catch {
        // Сеть мигнула — следующий опрос доберёт пропущенное по lastId.
      }
    };
    void poll();
    const id = setInterval(() => void poll(), POLL_MS);
    return () => {
      stopped = true;
      clearInterval(id);
    };
  }, []);

  if (toasts.length === 0) return null;
  return (
    <div className="alm-toasts trd-toasts">
      {toasts.map((e) => (
        <Toast key={e.id} e={e} dismiss={dismiss} />
      ))}
    </div>
  );
}
