import { authFetch } from './auth';

export type TradeDirection = 'long' | 'short';
export type TradeStatus = 'open' | 'closed';

export interface TradePosition {
  id: number;
  author: string;
  book: string;
  ticker: string;
  direction: TradeDirection;
  size: string;
  entry_price: number | null;
  stop: string;
  targets: string;
  status: TradeStatus;
  opened_at: string;
  closed_at: string | null;
  close_price: number | null;
  note: string;
  manual: boolean;
  updated_at: string;
  last_text: string;
}

export interface TradeEvent {
  id: number;
  position_id: number | null;
  author: string;
  ticker: string;
  action: string;
  direction: string;
  size: string;
  price: number | null;
  clause: string;
  message: string;
  manual: boolean;
  silent: boolean;
  at: string;
  created_at: string;
  posted_at: string | null;
}

export type TradePositionInput = Pick<
  TradePosition,
  'author' | 'book' | 'ticker' | 'direction' | 'size' | 'entry_price' | 'stop' | 'targets' | 'status' | 'close_price' | 'note'
>;

async function json<T>(res: Response, what: string): Promise<T> {
  if (!res.ok) {
    const text = (await res.text()).trim();
    throw new Error(`${what}: ${text || `HTTP ${res.status}`}`);
  }
  return res.json() as Promise<T>;
}

export async function fetchPositions(status: TradeStatus, limit = 200): Promise<TradePosition[]> {
  const res = await authFetch(`/api/v1/trades/positions?status=${status}&limit=${limit}`);
  return (await json<TradePosition[] | null>(res, 'позиции')) ?? [];
}

export async function fetchEvents(after = 0, limit = 100): Promise<TradeEvent[]> {
  const res = await authFetch(`/api/v1/trades/events?after=${after}&limit=${limit}`);
  return (await json<TradeEvent[] | null>(res, 'события')) ?? [];
}

export async function fetchLastEventId(): Promise<number> {
  const res = await authFetch('/api/v1/trades/last-event');
  return (await json<{ id: number }>(res, 'последнее событие')).id;
}

export async function savePosition(input: TradePositionInput, id?: number): Promise<TradePosition> {
  const res = await authFetch(id ? `/api/v1/trades/positions/${id}` : '/api/v1/trades/positions', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(input),
  });
  return json<TradePosition>(res, 'сохранение');
}

export async function deletePosition(id: number): Promise<void> {
  const res = await authFetch(`/api/v1/trades/positions/${id}/delete`, { method: 'POST' });
  if (!res.ok) throw new Error(`удаление: HTTP ${res.status}`);
}

const fmtPrice = new Intl.NumberFormat('ru-RU', { maximumFractionDigits: 4 });
const fmtWhen = new Intl.DateTimeFormat('ru-RU', {
  timeZone: 'Europe/Moscow', day: '2-digit', month: '2-digit', hour: '2-digit', minute: '2-digit',
});

export function price(v: number | null): string {
  return v == null ? '—' : fmtPrice.format(v);
}

export function when(iso: string | null): string {
  if (!iso) return '—';
  try { return fmtWhen.format(new Date(iso)); } catch { return iso; }
}

export function directionLabel(d: string): string {
  if (d === 'long') return 'ЛОНГ';
  if (d === 'short') return 'ШОРТ';
  return '';
}

export const ACTION_LABEL: Record<string, string> = {
  open: 'открыл',
  add: 'добрал',
  reduce: 'сократил',
  close: 'закрыл',
  stop: 'стоп',
  manual_open: 'добавлено вручную',
  manual_edit: 'изменено вручную',
};

export type Prices = Record<string, number>;

export async function fetchPrices(): Promise<Prices> {
  const res = await authFetch('/api/v1/prices');
  return (await json<Prices | null>(res, 'цены')) ?? {};
}

// Общие названия фьючерсов из сообщений («Микс», «Si») → префикс кода контракта
// на FORTS. Цена берётся у ближайшего по сроку контракта: его и торгуют.
const FUTURES_PREFIX: Record<string, string> = {
  MIX: 'MX', RTS: 'RI', Si: 'Si', BR: 'BR', GOLD: 'GD', SILV: 'SV', PLT: 'PT', CNY: 'CR',
};
const MONTHS = 'FGHJKMNQUVXZ';

function frontContract(prefix: string, prices: Prices): number | null {
  const re = new RegExp(`^${prefix}([${MONTHS}])([0-9])$`);
  let best: { key: number; price: number } | null = null;
  for (const [code, value] of Object.entries(prices)) {
    const m = re.exec(code);
    if (!m) continue;
    const key = Number(m[2]) * 12 + MONTHS.indexOf(m[1]);
    if (!best || key < best.key) best = { key, price: value };
  }
  return best?.price ?? null;
}

/**
 * Текущая цена инструмента позиции: общее название фьючерса — ближайший
 * контракт, иначе акция TQBR или конкретный фьючерс по коду. Фьючерс проверяется
 * первым: на TQBR есть фонд с тикером GOLD, а «золото» у авторов — фьючерс.
 */
export function quoteFor(ticker: string, prices: Prices): number | null {
  const prefix = FUTURES_PREFIX[ticker];
  if (prefix) return frontContract(prefix, prices);
  if (ticker in prices) return prices[ticker];
  if (ticker === 'IMOEX') return prices.IMOEXF ?? null;
  return null;
}

/**
 * Приводит цену входа к масштабу текущей. Авторы пишут фьючерс на индекс то
 * «230 900», то «233,050» — запятая там разделяет тысячи, и разбор читает 233,05.
 * Расхождение ровно на три порядка — это запись, а не движение цены.
 */
export function alignEntry(entry: number, current: number): number {
  for (const k of [1000, 0.001]) {
    const r = (entry * k) / current;
    if (r > 0.7 && r < 1.3) return entry * k;
  }
  return entry;
}

/** Прибыль/убыток позиции в процентах от входа, со знаком стороны. */
export function pnlPercent(p: Pick<TradePosition, 'direction' | 'entry_price'>, current: number | null): number | null {
  if (p.entry_price == null || p.entry_price === 0 || current == null) return null;
  const entry = alignEntry(p.entry_price, current);
  const move = (current - entry) / entry * 100;
  return p.direction === 'short' ? -move : move;
}

/** Загрузка портфеля — сумма долей, если все размеры заданы в процентах. */
export function loadPercent(list: TradePosition[]): number | null {
  let sum = 0;
  for (const p of list) {
    const m = /^(\d+(?:[.,]\d+)?)%$/.exec(p.size.trim());
    if (!m) return null;
    sum += Number(m[1].replace(',', '.'));
  }
  return list.length > 0 ? sum : null;
}

export function signedPercent(v: number | null): string {
  if (v == null) return '—';
  return `${v > 0 ? '+' : ''}${v.toFixed(2).replace('.', ',')}%`;
}
