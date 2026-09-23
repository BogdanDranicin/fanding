import { useCallback, useEffect, useMemo, useState } from 'react';
import {
  ACTION_LABEL,
  alignEntry,
  deletePosition,
  directionLabel,
  fetchEvents,
  fetchPositions,
  fetchPrices,
  loadPercent,
  pnlPercent,
  price,
  priceLoading,
  quoteFor,
  savePosition,
  signedPercent,
  when,
  type Prices,
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

// MsgPrice — цена входа или выхода: из сообщения как есть, подгруженная с
// биржи — со знаком ≈, пока подгружается — песочные часы.
function MsgPrice({ value, auto, loading }: { value: number | null; auto: boolean; loading: boolean }) {
  if (value == null && loading) {
    return <span className="trd-pending" title="Цены в сообщении нет — подгружается с биржи на момент сообщения">⏳</span>;
  }
  if (auto && value != null) {
    return <span className="trd-auto" title="Цены в сообщении не было — взята с биржи на момент сообщения">≈{price(value)}</span>;
  }
  return <>{price(value)}</>;
}

function PnL({ v }: { v: number | null }) {
  const cls = v == null ? '' : v > 0 ? ' trd-pnl-up' : v < 0 ? ' trd-pnl-down' : '';
  return <span className={`trd-num${cls}`}>{signedPercent(v)}</span>;
}

// PositionRow — строка таблицы позиций. Раскрывается по клику: подробности,
// исходное сообщение и правка — всё, что не влезает в колонки.
function PositionRow({ p, current, showAuthor, authors, onChanged }: {
  p: TradePosition;
  current: number | null;
  showAuthor: boolean;
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
  const result = closed ? pnlPercent(p, p.close_price) : pnlPercent(p, current);
  const against = closed ? p.close_price : current;
  const entry = p.entry_price != null && against != null ? alignEntry(p.entry_price, against) : p.entry_price;
  return (
    <details className={`trd-row${closed ? ' trd-row-closed' : ''}`}>
      <summary className="trd-tr">
        <span className="trd-td trd-td-ticker">
          {showAuthor && <span className="trd-author">{p.author}{p.book ? ` · ${p.book}` : ''}</span>}
          <span className="trd-ticker">{p.ticker}{p.manual && <span className="trd-manual" title="Правилась вручную"> ✎</span>}</span>
        </span>
        <span className="trd-td"><DirBadge d={p.direction} /></span>
        <span className="trd-td trd-num" data-label="Доля">{p.size || '—'}</span>
        <span className="trd-td trd-num" data-label="Вход">
          <MsgPrice value={entry} auto={p.entry_auto} loading={priceLoading(p.entry_price, p.opened_at, p.ticker)} />
        </span>
        <span className="trd-td trd-num" data-label={closed ? 'Выход' : 'Текущая'}>
          {closed
            ? <MsgPrice value={p.close_price} auto={p.close_auto}
                loading={priceLoading(p.close_price, p.closed_at, p.ticker)} />
            : price(current)}
        </span>
        <span className="trd-td" data-label={closed ? 'Результат' : 'Прибыль/убыток'}><PnL v={result} /></span>
        <span className="trd-td trd-td-muted" data-label="Стоп">{p.stop || '—'}</span>
        <span className="trd-td trd-td-muted trd-num" data-label={closed ? 'Закрыта' : 'Открыта'}>
          {when(closed ? p.closed_at : p.opened_at)}
        </span>
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

function TableHead({ closed, showAuthor }: { closed?: boolean; showAuthor?: boolean }) {
  return (
    <div className="trd-tr trd-thead" aria-hidden="true">
      <span className="trd-td">{showAuthor ? 'Автор · инструмент' : 'Инструмент'}</span>
      <span className="trd-td">Направление</span>
      <span className="trd-td trd-num">Доля</span>
      <span className="trd-td trd-num">Цена входа</span>
      <span className="trd-td trd-num">{closed ? 'Цена выхода' : 'Текущая цена'}</span>
      <span className="trd-td">{closed ? 'Результат' : 'Прибыль/убыток'}</span>
      <span className="trd-td">Стоп</span>
      <span className="trd-td trd-num">{closed ? 'Закрыта' : 'Открыта'}</span>
    </div>
  );
}

// Portfolio — блок одного автора (у Profit King — одного модельного
// портфеля): шапка с загрузкой, как в его «Текущих позициях», и таблица.
function Portfolio({ title, list, prices, authors, onChanged }: {
  title: string;
  list: TradePosition[];
  prices: Prices;
  authors: string[];
  onChanged: () => void;
}) {
  const load = loadPercent(list);
  return (
    <section className="trd-portfolio">
      <div className="trd-portfolio-head">
        <span className="trd-portfolio-title">{title}</span>
        <span className="trd-portfolio-fact"><i>ПОЗИЦИЙ</i>{list.length}</span>
        {load != null && <span className="trd-portfolio-fact"><i>ЗАГРУЗКА</i>{load.toLocaleString('ru-RU')}%</span>}
      </div>
      <div className="trd-table">
        <TableHead />
        {list.map((p) => (
          <PositionRow key={p.id} p={p} current={quoteFor(p.ticker, prices)} showAuthor={false}
            authors={authors} onChanged={onChanged} />
        ))}
      </div>
    </section>
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

// PRICES_MS — котировки для колонки «текущая цена». Бэкенд кэширует их на
// минуту, чаще спрашивать незачем.
const PRICES_MS = 30000;

const BOOK_TITLE: Record<string, string> = { МП1: 'Модельный портфель 1', МП2: 'Модельный портфель 2' };

export function TradesPage() {
  const [open, setOpen] = useState<TradePosition[]>([]);
  const [closed, setClosed] = useState<TradePosition[]>([]);
  const [events, setEvents] = useState<TradeEvent[]>([]);
  const [prices, setPrices] = useState<Prices>({});
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

  const loadPrices = useCallback(async () => {
    try {
      setPrices(await fetchPrices());
    } catch {
      // Без котировок в колонке «текущая цена» останутся прочерки — это не ошибка страницы.
    }
  }, []);

  // Первичная загрузка и опрос. Состояние меняется только после ответа
  // сервера, правило не видит это через async-границу.
  // eslint-disable-next-line react-hooks/set-state-in-effect
  useEffect(() => { void load(); void loadPrices(); }, [load, loadPrices]);
  useEffect(() => {
    const id = setInterval(() => { if (!document.hidden) void load(); }, POLL_MS);
    const pid = setInterval(() => { if (!document.hidden) void loadPrices(); }, PRICES_MS);
    return () => {
      clearInterval(id);
      clearInterval(pid);
    };
  }, [load, loadPrices]);

  const authors = useMemo(
    () => [...new Set([...open, ...closed].map((p) => p.author))].sort((a, b) => a.localeCompare(b, 'ru')),
    [open, closed],
  );

  // Портфели: автор, а у Profit King — ещё и номер модельного портфеля.
  const portfolios = useMemo(() => {
    const groups = new Map<string, { title: string; list: TradePosition[] }>();
    for (const p of open) {
      if (author && p.author !== author) continue;
      const key = `${p.author}|${p.book}`;
      const title = p.book ? `${p.author} · ${BOOK_TITLE[p.book] ?? p.book}` : p.author;
      const g = groups.get(key) ?? { title, list: [] };
      g.list.push(p);
      groups.set(key, g);
    }
    return [...groups.entries()]
      .sort(([a], [b]) => a.localeCompare(b, 'ru'))
      .map(([key, g]) => ({ key, ...g }));
  }, [open, author]);

  const shownClosed = author ? closed.filter((p) => p.author === author) : closed;
  const shownEvents = author ? events.filter((e) => e.author === author) : events;
  const reload = () => void load();

  return (
    <div className="race-page trd-page">
      <div className="race-header">
        <h2 className="race-title">Сделки авторов</h2>
        <button type="button" className="btn-plain" onClick={() => setCreating((c) => !c)}>
          {creating ? 'Отмена' : '+ Позиция'}
        </button>
      </div>

      <p className="race-subtitle">
        Позиции собираются из сообщений каналов по ключевым словам: «покупка», «взял шорт», «добрал»,
        «закрыл 50%», «закрыл остаток», стоп и цели. Текущая цена — последняя сделка на Мосбирже, для
        общих названий фьючерсов («Микс», «Si») — ближайший контракт. Что понято не так — нажмите на
        строку и поправьте, правка помечается ✎.
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

      {loaded && portfolios.length === 0 && <p className="race-subtitle">Открытых позиций нет.</p>}
      {portfolios.map((g) => (
        <Portfolio key={g.key} title={g.title} list={g.list} prices={prices} authors={authors} onChanged={reload} />
      ))}

      <details className="trd-section">
        <summary className="race-card-title trd-section-title">Закрытые · последние {shownClosed.length}</summary>
        {shownClosed.length > 0 && (
          <div className="trd-table">
            <TableHead closed showAuthor />
            {shownClosed.map((p) => (
              <PositionRow key={p.id} p={p} current={null} showAuthor authors={authors} onChanged={reload} />
            ))}
          </div>
        )}
      </details>

      <details className="trd-section">
        <summary className="race-card-title trd-section-title">Лента разбора</summary>
        {shownEvents.length === 0
          ? <p className="race-subtitle">Событий пока нет.</p>
          : <div className="trd-events">{shownEvents.map((e) => <EventRow key={e.id} e={e} />)}</div>}
      </details>
    </div>
  );
}
