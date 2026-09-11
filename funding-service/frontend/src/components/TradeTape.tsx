import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import type { Instrument, TapePrint, TapeResponse } from '../types/robots';
import { authFetch } from '../api/auth';

// Лента обезличенных сделок — то же, что в терминале, с одной разницей: подряд
// идущие сделки одного приказа сложены в один принт. Биржа печатает не приказы,
// а сделки, и рыночная заявка на 372 лота выходит в ленту пятью строчками —
// собрать из них приказ глазами нельзя. Складывает их сервер тем же кодом,
// которым кормится поиск роботов (mergeAggressors), так что «объём за раз» в
// ленте и у робота означает одно и то же.

const SYMBOL_KEY = 'tape.symbol';
const MIN_QTY_KEY = 'tape.min-qty';
const REFRESH_MS = 1000;
const LIMIT = 500;

const fmtLots = new Intl.NumberFormat('ru-RU');
const fmtTime = new Intl.DateTimeFormat('ru-RU', {
  timeZone: 'Europe/Moscow',
  hour: '2-digit',
  minute: '2-digit',
  second: '2-digit',
  fractionalSecondDigits: 3,
});

function clock(iso: string): string {
  try { return fmtTime.format(new Date(iso)); } catch { return iso; }
}

// Цена рисуется с тем же числом знаков, что держит биржа: у Si это целые рубли
// с копейками, у акций — четыре знака, и общий формат врал бы обоим.
function priceFmt(inst: Instrument | undefined): Intl.NumberFormat {
  const digits = inst?.decimals ?? 2;
  return new Intl.NumberFormat('ru-RU', {
    minimumFractionDigits: digits,
    maximumFractionDigits: digits,
  });
}

// Контракт на доллар: SI + буква месяца + цифра года. Он же тикер по умолчанию —
// за ним на эту страницу и приходят.
const SI_RE = /^SI[FGHJKMNQUVXZ]\d$/;

function defaultSymbol(symbols: string[]): string {
  return symbols.find((s) => SI_RE.test(s)) ?? symbols[0] ?? '';
}

interface Totals {
  buy: number;
  sell: number;
  prints: number;
}

function totalsOf(prints: TapePrint[]): Totals {
  return prints.reduce<Totals>((acc, p) => {
    if (p.side === 'B') acc.buy += p.qty;
    else acc.sell += p.qty;
    acc.prints += 1;
    return acc;
  }, { buy: 0, sell: 0, prints: 0 });
}

function lots(v: number): string {
  return `${fmtLots.format(Math.round(v))} л`;
}

interface Props {
  symbols: string[];
  instruments: Map<string, Instrument>;
}

export function TradeTape({ symbols, instruments }: Props) {
  const [symbol, setSymbol] = useState(() => localStorage.getItem(SYMBOL_KEY) ?? '');
  const [merged, setMerged] = useState(true);
  const [minQtyDraft, setMinQtyDraft] = useState(() => localStorage.getItem(MIN_QTY_KEY) ?? '');
  const [paused, setPaused] = useState(false);
  const [resp, setResp] = useState<TapeResponse | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [loading, setLoading] = useState(true);

  // Тикер выбирается один раз — когда приходит список наблюдения. Дальше его
  // меняет только пользователь: подставлять сишку поверх выбранной бумаги на
  // каждом обновлении списка значило бы отбирать выбор.
  const picked = useRef(false);
  useEffect(() => {
    if (picked.current || symbols.length === 0) return;
    picked.current = true;
    setSymbol((cur) => (cur && symbols.includes(cur) ? cur : defaultSymbol(symbols)));
  }, [symbols]);

  useEffect(() => {
    if (symbol) localStorage.setItem(SYMBOL_KEY, symbol);
  }, [symbol]);

  useEffect(() => { localStorage.setItem(MIN_QTY_KEY, minQtyDraft); }, [minQtyDraft]);

  const load = useCallback(async (sym: string, mergePrints: boolean) => {
    if (!sym) return;
    try {
      const q = `symbol=${encodeURIComponent(sym)}&limit=${LIMIT}${mergePrints ? '' : '&raw=1'}`;
      const r = await authFetch(`/api/v1/robots/tape?${q}`);
      if (!r.ok) throw new Error(`HTTP ${r.status}`);
      setResp((await r.json()) as TapeResponse);
      setError(null);
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    } finally {
      setLoading(false);
    }
  }, []);

  // eslint-disable-next-line react-hooks/set-state-in-effect
  useEffect(() => { setLoading(true); void load(symbol, merged); }, [load, symbol, merged]);

  // Лента обновляется сама раз в секунду. На паузе и в скрытой вкладке не
  // обновляется вовсе: читать её всё равно некому, а каждый запрос — это сотни
  // строк по сети.
  useEffect(() => {
    if (paused || !symbol) return;
    const id = setInterval(() => {
      if (document.hidden) return;
      void load(symbol, merged);
    }, REFRESH_MS);
    return () => clearInterval(id);
  }, [load, symbol, merged, paused]);

  const inst = instruments.get(symbol) ?? resp?.instrument;
  const price = useMemo(() => priceFmt(inst), [inst]);

  const minQty = Number(minQtyDraft);
  const shown = useMemo(() => {
    const all = resp?.prints ?? [];
    if (!Number.isFinite(minQty) || minQty <= 0) return all;
    return all.filter((p) => p.qty >= minQty);
  }, [resp, minQty]);

  const totals = useMemo(() => totalsOf(shown), [shown]);
  const delta = totals.buy - totals.sell;

  return (
    <div className="tape">
      <div className="rb-controls">
        <label className="rb-filter">
          Инструмент
          <select value={symbol} onChange={(e) => setSymbol(e.target.value)}>
            {/* Выбранный тикер остаётся в списке, даже когда список наблюдения
                ещё не пришёл: иначе поле показывает пустоту вместо бумаги,
                лента по которой уже грузится. */}
            {!symbols.includes(symbol) && <option value={symbol}>{symbol || '—'}</option>}
            {symbols.map((s) => <option key={s} value={s}>{s}</option>)}
          </select>
        </label>

        <label className="rb-filter" title="Показывать только принты не меньше этого объёма">
          Объём от
          <input
            className="rb-threshold"
            type="number"
            min={0}
            step={1}
            inputMode="numeric"
            placeholder="все"
            value={minQtyDraft}
            onChange={(e) => setMinQtyDraft(e.target.value)}
          />
          лотов
        </label>

        <label
          className="rb-checkbox"
          title="Подряд идущие сделки одного приказа складываются в один принт: в ленте виден приказ, а не его осколки"
        >
          <input type="checkbox" checked={merged} onChange={(e) => setMerged(e.target.checked)} />
          складывать принты
        </label>

        <label className="rb-checkbox" title="Остановить обновление, чтобы разглядеть строку">
          <input type="checkbox" checked={paused} onChange={(e) => setPaused(e.target.checked)} />
          пауза
        </label>
      </div>

      {inst && (
        <div className="tape-about">
          <span className="rb-paper-name">{inst.name}</span>
          {inst.min_step > 0 && (
            <span>шаг {inst.min_step.toLocaleString('ru-RU', { maximumFractionDigits: 6 })}</span>
          )}
          {inst.lot_size > 0 && (
            <span>{inst.lot_size === 1 ? 'лот — 1 бумага' : `${fmtLots.format(inst.lot_size)} бумаг в лоте`}</span>
          )}
        </div>
      )}

      {/* Итог по показанному куску ленты: сколько прошло в каждую сторону и чей
          перевес. Ради него фильтр по объёму и заведён — видно, кем набран
          оборот: крупными приказами или мелочью. */}
      <div className="tape-totals">
        <span>принтов: {fmtLots.format(totals.prints)}</span>
        <span className="rb-long">▲ {lots(totals.buy)}</span>
        <span className="rb-short">▼ {lots(totals.sell)}</span>
        <span className={delta >= 0 ? 'rb-long' : 'rb-short'}>
          перевес {delta >= 0 ? '+' : '−'}{lots(Math.abs(delta))}
        </span>
      </div>

      {error && <p className="race-error">Ошибка загрузки: {error}</p>}

      {!error && shown.length === 0 && !loading && (
        <p className="race-empty">
          {symbol
            ? 'По этому инструменту сделок в окне нет. Лента держится за последние полчаса торгов.'
            : 'Инструмент не выбран.'}
        </p>
      )}

      {shown.length > 0 && (
        <div className="tape-table">
          <div className="tape-row tape-head" aria-hidden="true">
            <span>время</span>
            <span>цена</span>
            <span>объём</span>
            <span>сделок</span>
          </div>
          {shown.map((p, i) => (
            <div
              key={`${p.time}-${i}-${p.price}-${p.qty}`}
              className={`tape-row ${p.side === 'B' ? 'tape-buy' : 'tape-sell'}`}
            >
              <span className="tape-time">{clock(p.time)}</span>
              <span className="tape-price">{price.format(p.price)}</span>
              <span className="tape-qty">
                <span className="tape-side" aria-hidden="true">{p.side === 'B' ? '▲' : '▼'}</span>
                {fmtLots.format(Math.round(p.qty))}
              </span>
              <span className="tape-trades" title={p.trades > 1
                ? `Приказ собрал ${p.trades} заявок стакана`
                : 'Приказ забрал одну заявку целиком'}>
                {p.trades > 1 ? `×${p.trades}` : ''}
              </span>
            </div>
          ))}
        </div>
      )}
    </div>
  );
}
