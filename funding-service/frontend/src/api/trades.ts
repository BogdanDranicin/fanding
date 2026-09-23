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
