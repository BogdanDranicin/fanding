import { useCallback, useEffect, useMemo, useState } from 'react';
import {
  ACTION_LABEL,
  deletePosition,
  directionLabel,
  fetchEvents,
  fetchPositions,
  price,
  savePosition,
  when,
  type TradeEvent,
  type TradePosition,
  type TradePositionInput,
} from '../api/trades';

// POLL_MS — как часто вкладка перечитывает позиции. Сделки приходят из
// Telegram за секунды, а чаще раза в пять секунд таблица глазом не читается.
const POLL_MS = 5000;

const EMPTY: TradePositionInput = {
  author: '', book: '', ticker: '', direction: 'long', size: '', entry_price: null,
  stop: '', targets: '', status: 'open', close_price: null, note: '',
};

function toInput(p: TradePosition): TradePositionInput {
  return {
    author: p.author, book: p.book, ticker: p.ticker, direction: p.direction, size: p.size,
    entry_price: p.entry_price, stop: p.stop, targets: p.targets, status: p.status,
    close_price: p.close_price, note: p.note,
  };
}

function parsePrice(s: string): number | null {
  const v = Number(s.replace(/\s/g, '').replace(',', '.'));
  return s.trim() === '' || !Number.isFinite(v) ? null : v;
}

function DirBadge({ d }: { d: string }) {
  return <span className={`trd-dir trd-dir-${d}`}>{directionLabel(d)}</span>;
}

function PositionForm({ initial, authors, onSave, onCancel }: {
  initial: TradePositionInput;
  authors: string[];
  onSave: (v: TradePositionInput) => Promise<void>;
  onCancel: () => void;
}) {
  const [v, setV] = useState(initial);
  const [entry, setEntry] = useState(initial.entry_price?.toString() ?? '');
  const [close, setClose] = useState(initial.close_price?.toString() ?? '');
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const set = <K extends keyof TradePositionInput>(k: K, val: TradePositionInput[K]) =>
    setV((old) => ({ ...old, [k]: val }));

  const submit = async (e: React.FormEvent) => {
    e.preventDefault();
    setBusy(true);
    setError(null);
    try {
      await onSave({ ...v, entry_price: parsePrice(entry), close_price: parsePrice(close) });
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
      setBusy(false);
    }
  };

  return (
    <form className="trd-form" onSubmit={submit}>
      <label className="trd-field">
        <span>Автор</span>
        <input className="trd-input" list="trd-authors" value={v.author} required
          onChange={(e) => set('author', e.target.value)} />
        <datalist id="trd-authors">
          {authors.map((a) => <option key={a} value={a} />)}
        </datalist>
      </label>
      <label className="trd-field">
        <span>Тикер</span>
        <input className="trd-input" value={v.ticker} required onChange={(e) => set('ticker', e.target.value)} />
      </label>
      <label className="trd-field">
        <span>Направление</span>
        <select className="trd-input" value={v.direction}
          onChange={(e) => set('direction', e.target.value as TradePositionInput['direction'])}>
          <option value="long">Лонг</option>
          <option value="short">Шорт</option>
        </select>
      </label>
      <label className="trd-field">
        <span>Размер</span>
        <input className="trd-input" value={v.size} placeholder="25% / 6500 шт"
          onChange={(e) => set('size', e.target.value)} />
      </label>
      <label className="trd-field">
        <span>Цена входа</span>
        <input className="trd-input" inputMode="decimal" value={entry} onChange={(e) => setEntry(e.target.value)} />
      </label>
      <label className="trd-field">
        <span>Стоп</span>
        <input className="trd-input" value={v.stop} onChange={(e) => set('stop', e.target.value)} />
      </label>
      <label className="trd-field">
        <span>Цели</span>
        <input className="trd-input" value={v.targets} onChange={(e) => set('targets', e.target.value)} />
      </label>
      <label className="trd-field">
        <span>Портфель</span>
        <input className="trd-input" value={v.book} placeholder="МП1" onChange={(e) => set('book', e.target.value)} />
      </label>
      <label className="trd-field">
        <span>Статус</span>
        <select className="trd-input" value={v.status}
          onChange={(e) => set('status', e.target.value as TradePositionInput['status'])}>
          <option value="open">Открыта</option>
          <option value="closed">Закрыта</option>
        </select>
      </label>
      {v.status === 'closed' && (
        <label className="trd-field">
          <span>Цена закрытия</span>
          <input className="trd-input" inputMode="decimal" value={close} onChange={(e) => setClose(e.target.value)} />
        </label>
      )}
      <label className="trd-field trd-field-wide">
        <span>Заметка</span>
        <input className="trd-input" value={v.note} onChange={(e) => set('note', e.target.value)} />
      </label>
      <div className="trd-form-actions">
        <button type="submit" className="btn-plain" disabled={busy}>{busy ? 'Сохраняю…' : 'Сохранить'}</button>
        <button type="button" className="btn-plain" onClick={onCancel} disabled={busy}>Отмена</button>
        {error && <span className="trd-error">{error}</span>}
      </div>
    </form>
  );
}

function PositionCard({ p, authors, onChanged }: {
  p: TradePosition;
  authors: string[];
  onChanged: () => void;
}) {
  const [editing, setEditing] = useState(false);
  const [confirmDelete, setConfirmDelete] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const run = async (fn: () => Promise<unknown>) => {
    setError(null);
    try {
      await fn();
      onChanged();
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    }
  };

  const closed = p.status === 'closed';
  return (
    <details className={`jrn-card trd-card${closed ? ' trd-card-closed' : ''}`}>
      <summary className="jrn-summary trd-summary">
        <div className="trd-who">
          <span className="trd-author">{p.author}{p.book ? ` · ${p.book}` : ''}</span>
          <span className="trd-ticker">{p.ticker}</span>
        </div>
        <DirBadge d={p.direction} />
        <div className="trd-facts">
          <span className="jrn-rate"><i>РАЗМЕР</i>{p.size || '—'}</span>
          <span className="jrn-rate"><i>ВХОД</i>{price(p.entry_price)}</span>
          {closed
            ? <span className="jrn-rate"><i>ВЫХОД</i>{price(p.close_price)}</span>
            : <span className="jrn-rate"><i>СТОП</i>{p.stop || '—'}</span>}
          <span className="jrn-rate"><i>{closed ? 'ЗАКРЫТА' : 'ОТКРЫТА'}</i>{when(closed ? p.closed_at : p.opened_at)}</span>
        </div>
        {p.manual && <span className="trd-manual" title="Правилась вручную">✎</span>}
        <span className="jrn-chevron" aria-hidden="true">▸</span>
      </summary>

      <div className="trd-details">
        {editing ? (
          <PositionForm
            initial={toInput(p)}
            authors={authors}
            onCancel={() => setEditing(false)}
            onSave={async (v) => {
              await savePosition(v, p.id);
              setEditing(false);
              onChanged();
            }}
          />
        ) : (
          <>
            <div className="trd-grid">
              <div><span className="trd-label">Цели</span>{p.targets || '—'}</div>
              <div><span className="trd-label">Открыта</span>{when(p.opened_at)}</div>
              <div><span className="trd-label">Обновлена</span>{when(p.updated_at)}</div>
              {p.stop && closed && <div><span className="trd-label">Стоп</span>{p.stop}</div>}
              {p.note && <div className="trd-wide"><span className="trd-label">Заметка</span>{p.note}</div>}
            </div>
            {p.last_text && (
              <div className="trd-source">
                <span className="trd-label">Последнее сообщение</span>
                <p>{p.last_text.length > 700 ? `${p.last_text.slice(0, 700)}…` : p.last_text}</p>
              </div>
            )}
            <div className="trd-form-actions">
              <button type="button" className="btn-plain" onClick={() => setEditing(true)}>Изменить</button>
              {!closed && (
                <button type="button" className="btn-plain"
                  onClick={() => run(() => savePosition({ ...toInput(p), status: 'closed' }, p.id))}>
                  Закрыть
                </button>
              )}
              {closed && (
                <button type="button" className="btn-plain"
                  onClick={() => run(() => savePosition({ ...toInput(p), status: 'open' }, p.id))}>
                  Открыть снова
                </button>
              )}
              <button type="button" className={`btn-plain${confirmDelete ? ' trd-danger' : ''}`}
                onClick={() => (confirmDelete ? run(() => deletePosition(p.id)) : setConfirmDelete(true))}
                onBlur={() => setConfirmDelete(false)}>
                {confirmDelete ? 'Точно удалить?' : 'Удалить'}
              </button>
              {error && <span className="trd-error">{error}</span>}
            </div>
          </>
        )}
      </div>
    </details>
  );
}

function EventRow({ e }: { e: TradeEvent }) {
  const label = ACTION_LABEL[e.action] ?? e.action;
  return (
    <div className={`trd-event${e.manual ? ' trd-event-manual' : ''}`}>
      <span className="trd-event-time">{when(e.at)}</span>
      <span className="trd-event-who">{e.author}</span>
      <span className="trd-event-what">
        {label} <b>{e.ticker || '—'}</b> {directionLabel(e.direction)}
        {e.size ? ` · ${e.size}` : ''}{e.price != null ? ` · ${price(e.price)}` : ''}
      </span>
      {e.clause && <span className="trd-event-clause">«{e.clause}»</span>}
    </div>
  );
}

export function TradesPage() {
  const [open, setOpen] = useState<TradePosition[]>([]);
  const [closed, setClosed] = useState<TradePosition[]>([]);
  const [events, setEvents] = useState<TradeEvent[]>([]);
  const [error, setError] = useState<string | null>(null);
  const [loaded, setLoaded] = useState(false);
  const [author, setAuthor] = useState('');
  const [creating, setCreating] = useState(false);

  const load = useCallback(async () => {
    try {
      const [o, c, e] = await Promise.all([fetchPositions('open'), fetchPositions('closed', 60), fetchEvents(0, 60)]);
      setOpen(o);
      setClosed(c);
      setEvents(e);
      setError(null);
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    } finally {
      setLoaded(true);
    }
  }, []);

  // Первичная загрузка и опрос. load() меняет состояние только после ответа
  // сервера, правило не видит это через async-границу.
  // eslint-disable-next-line react-hooks/set-state-in-effect
  useEffect(() => { void load(); }, [load]);
  useEffect(() => {
    const id = setInterval(() => { if (!document.hidden) void load(); }, POLL_MS);
    return () => clearInterval(id);
  }, [load]);

  const authors = useMemo(
    () => [...new Set([...open, ...closed].map((p) => p.author))].sort((a, b) => a.localeCompare(b, 'ru')),
    [open, closed],
  );
  const byAuthor = (list: TradePosition[]) => (author ? list.filter((p) => p.author === author) : list);
  const shownOpen = byAuthor(open);
  const shownClosed = byAuthor(closed);
  const shownEvents = author ? events.filter((e) => e.author === author) : events;

  return (
    <div className="race-page">
      <div className="race-header">
        <h2 className="race-title">Сделки авторов</h2>
        <button type="button" className="btn-plain" onClick={() => setCreating((c) => !c)}>
          {creating ? 'Отмена' : '+ Позиция'}
        </button>
      </div>

      <p className="race-subtitle">
        Позиции собираются из сообщений каналов по ключевым словам: «покупка», «взял шорт», «добрал»,
        «закрыл 50%», «закрыл остаток», стоп и цели. Что понято не так — поправьте в карточке, правка
        помечается ✎. В ленте ниже видно, из какой фразы взято каждое действие.
      </p>

      {creating && (
        <div className="race-card">
          <div className="race-card-title">Новая позиция</div>
          <PositionForm
            initial={{ ...EMPTY, author }}
            authors={authors}
            onCancel={() => setCreating(false)}
            onSave={async (v) => {
              await savePosition(v);
              setCreating(false);
              await load();
            }}
          />
        </div>
      )}

      {authors.length > 1 && (
        <div className="trd-filter">
          <button type="button" className={`trd-chip${author === '' ? ' trd-chip-on' : ''}`} onClick={() => setAuthor('')}>
            Все
          </button>
          {authors.map((a) => (
            <button key={a} type="button" className={`trd-chip${author === a ? ' trd-chip-on' : ''}`}
              onClick={() => setAuthor(a)}>
              {a}
            </button>
          ))}
        </div>
      )}

      {error && <div className="trd-error">Не удалось загрузить: {error}</div>}

      <div className="race-card-title">Открытые позиции · {shownOpen.length}</div>
      {loaded && shownOpen.length === 0 && <p className="race-subtitle">Открытых позиций нет.</p>}
      {shownOpen.length > 0 && (
        <div className="jrn-list">
          {shownOpen.map((p) => <PositionCard key={p.id} p={p} authors={authors} onChanged={() => void load()} />)}
        </div>
      )}

      <details className="trd-section">
        <summary className="race-card-title trd-section-title">Закрытые · последние {shownClosed.length}</summary>
        {shownClosed.length > 0 && (
          <div className="jrn-list">
            {shownClosed.map((p) => <PositionCard key={p.id} p={p} authors={authors} onChanged={() => void load()} />)}
          </div>
        )}
      </details>

      <details className="trd-section" open>
        <summary className="race-card-title trd-section-title">Лента разбора</summary>
        {shownEvents.length === 0
          ? <p className="race-subtitle">Событий пока нет.</p>
          : <div className="trd-events">{shownEvents.map((e) => <EventRow key={e.id} e={e} />)}</div>}
      </details>
    </div>
  );
}
