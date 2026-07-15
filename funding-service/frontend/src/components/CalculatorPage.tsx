import { useState, useEffect, useRef, useMemo, useCallback } from 'react';
import type { InstrumentInfo } from '../types/funding';
import { useFundingStore } from '../store/fundingStore';

// ─── persistence ────────────────────────────────────────────────────────────

const POS_KEY     = 'funding_positions_v1';
const FAV_KEY     = 'funding_favorites_v1';
const PRICE_KEY   = 'funding_prices_v1';
const DEPOSIT_KEY = 'funding_deposit_v1';
const LEVER_KEY   = 'funding_leverage_v1';

function loadPositions(): Record<string, number> {
  try { return JSON.parse(localStorage.getItem(POS_KEY) ?? '{}'); }
  catch { return {}; }
}
function savePositions(p: Record<string, number>) {
  localStorage.setItem(POS_KEY, JSON.stringify(p));
}
function loadFavorites(): Set<string> {
  try { return new Set(JSON.parse(localStorage.getItem(FAV_KEY) ?? '[]')); }
  catch { return new Set(); }
}
function saveFavorites(f: Set<string>) {
  localStorage.setItem(FAV_KEY, JSON.stringify([...f]));
}
function loadUserPrices(): Record<string, string> {
  try { return JSON.parse(localStorage.getItem(PRICE_KEY) ?? '{}'); }
  catch { return {}; }
}
function saveUserPrices(p: Record<string, string>) {
  localStorage.setItem(PRICE_KEY, JSON.stringify(p));
}

// ─── formatters ─────────────────────────────────────────────────────────────

const fmtRub = new Intl.NumberFormat('ru-RU', { minimumFractionDigits: 2, maximumFractionDigits: 2 });
const fmtInt = new Intl.NumberFormat('ru-RU', { maximumFractionDigits: 0 });

const API_BASE = (import.meta.env.VITE_API_BASE as string) ?? '';

const LIVE_FUNDING_SYMS = ['USDRUBF', 'EURRUBF', 'CNYRUBF'] as const;
type LiveSym = (typeof LIVE_FUNDING_SYMS)[number];

// ─── instrument specs fallback (direct MOEX ISS) ─────────────────────────────

async function fetchAllSpecsDirect(): Promise<InstrumentInfo[]> {
  const parseMoex = (
    raw: { securities: { columns: string[]; data: unknown[][] } },
    marketType: 'future' | 'stock',
    lotField: string
  ): InstrumentInfo[] => {
    const idx: Record<string, number> = {};
    raw.securities.columns.forEach((c, i) => { idx[c] = i; });
    return raw.securities.data.map((row) => ({
      symbol:         (row[idx['SECID']] as string) ?? '',
      short_name:     (row[idx['SHORTNAME']] as string) ?? '',
      market_type:    marketType,
      initial_margin: (row[idx['INITIALMARGIN']] as number) ?? 0,
      lot_size:       (row[idx[lotField]] as number) ?? 0,
      step_price:     (row[idx['STEPPRICE']] as number) ?? 0,
      min_step:       (row[idx['MINSTEP']] as number) ?? 0,
    })).filter((i) => i.symbol);
  };
  const base = 'https://iss.moex.com/iss';
  const [fr, sr] = await Promise.all([
    fetch(`${base}/engines/futures/markets/forts/securities.json?iss.meta=off&iss.only=securities&securities.columns=SECID,SHORTNAME,INITIALMARGIN,LOTVOLUME,STEPPRICE,MINSTEP`).then((r) => r.json()),
    fetch(`${base}/engines/stock/markets/shares/boards/TQBR/securities.json?iss.meta=off&iss.only=securities&securities.columns=SECID,SHORTNAME,LOTSIZE,MINSTEP`).then((r) => r.json()),
  ]);
  return [
    ...parseMoex(fr as Parameters<typeof parseMoex>[0], 'future', 'LOTVOLUME'),
    ...parseMoex(sr as Parameters<typeof parseMoex>[0], 'stock', 'LOTSIZE'),
  ];
}

// ─── price fetching ──────────────────────────────────────────────────────────

async function parseMoexMD(raw: { marketdata: { columns: string[]; data: unknown[][] } }): Promise<Record<string, number>> {
  const idx: Record<string, number> = {};
  raw.marketdata.columns.forEach((c, i) => { idx[c] = i; });
  const result: Record<string, number> = {};
  for (const row of raw.marketdata.data) {
    const sym = (row as unknown[])[idx['SECID']] as string;
    if (!sym) continue;
    const price =
      ((row as unknown[])[idx['LAST']] as number) ||
      ((row as unknown[])[idx['SETTLEPRICE']] as number) ||
      ((row as unknown[])[idx['PREVPRICE']] as number) ||
      0;
    if (price > 0) result[sym] = price;
  }
  return result;
}

async function fetchPricesDirect(): Promise<Record<string, number>> {
  const base = 'https://iss.moex.com/iss';
  const [fr, sr] = await Promise.all([
    fetch(`${base}/engines/futures/markets/forts/securities.json?iss.meta=off&iss.only=marketdata&marketdata.columns=SECID,LAST,SETTLEPRICE,PREVPRICE`).then((r) => r.json()),
    fetch(`${base}/engines/stock/markets/shares/boards/TQBR/securities.json?iss.meta=off&iss.only=marketdata&marketdata.columns=SECID,LAST,PREVPRICE`).then((r) => r.json()),
  ]);
  const [a, b] = await Promise.all([parseMoexMD(fr), parseMoexMD(sr)]);
  return { ...a, ...b };
}

async function fetchPrices(): Promise<Record<string, number>> {
  try {
    const r = await fetch(`${API_BASE}/api/v1/prices`);
    if (r.ok) return r.json();
  } catch { /* fallthrough */ }
  return fetchPricesDirect();
}

// fetchSwapRates pulls the MOEX funding rate (SWAPRATE) per perpetual future from
// OUR backend (/api/v1/swap-rates), not directly from ISS: the site CSP allows
// connect-src 'self' only, so a browser call to iss.moex.com is blocked. The
// backend returns SECID→SWAPRATE, quarterly futures (null SWAPRATE) already dropped.
// Funding per lot in ₽ = SWAPRATE × lot_size (validated: the backend's moex_funding
// for CNYRUBF equals its SWAPRATE exactly, so this matches the existing currency path).
async function fetchSwapRates(): Promise<Record<string, number>> {
  try {
    const r = await fetch(`${API_BASE}/api/v1/swap-rates`);
    if (r.ok) return (await r.json()) as Record<string, number>;
  } catch { /* ignore — funding just won't show for perpetuals */ }
  return {};
}

// ─── sub-components ──────────────────────────────────────────────────────────

interface ActiveRowProps {
  inst: InstrumentInfo;
  lots: number;
  price: string;      // effective price (auto or user override)
  priceAuto: boolean; // true when price is auto-fetched (not user-typed)
  isFav: boolean;
  onLotsChange: (v: number) => void;
  onPriceChange: (v: string) => void;
  onDelete: () => void;
  onToggleFav: () => void;
  fundingPerLot: number | null;
}

function ActiveRow({ inst, lots, price, priceAuto, isFav, onLotsChange, onPriceChange, onDelete, onToggleFav, fundingPerLot }: ActiveRowProps) {
  const [rawLots, setRawLots] = useState(() => lots !== 0 ? String(lots) : '');
  const lastExternalLots = useRef(lots);

  useEffect(() => {
    if (lastExternalLots.current !== lots) {
      lastExternalLots.current = lots;
      const parsed = parseInt(rawLots, 10) || 0;
      if (parsed !== lots) setRawLots(lots !== 0 ? String(lots) : '');
    }
  }, [lots, rawLots]);

  const priceNum = parseFloat(price.replace(',', '.')) || 0;
  // Фьючерс — по ГО (гарантийное обеспечение, цена не нужна); акция — по полной стоимости позиции.
  const isFuture = inst.market_type === 'future';
  const posValue = lots === 0
    ? null
    : isFuture
      ? (inst.initial_margin > 0 ? Math.abs(lots) * inst.initial_margin : null)
      : (priceNum > 0 && inst.lot_size > 0 ? lots * inst.lot_size * priceNum : null);

  const fund = fundingPerLot != null && lots !== 0
    ? (lots * fundingPerLot >= 0 ? '+' : '') + fmtRub.format(lots * fundingPerLot) + ' ₽'
    : null;

  const valueTip = posValue != null
    ? (isFuture
        ? `${Math.abs(lots)} лот × ${inst.initial_margin} ₽/лот ГО`
        : `${lots} лот × ${inst.lot_size} шт./лот × ${priceNum} ₽`)
    : '';

  return (
    <div className="pos-row pos-row-active">
      <span className={`pos-dot pos-dot-${inst.market_type}`} title={inst.market_type === 'future' ? 'Фьючерс' : 'Акция'} />
      <span className="pos-sym">{inst.symbol}</span>
      <span className="pos-name">{inst.short_name}</span>
      <div className="pos-row-bottom">
        <input
          className="calc-input calc-input-lots"
          type="text"
          inputMode="numeric"
          placeholder="лоты"
          autoComplete="off"
          value={rawLots}
          onChange={(e) => {
            // allow leading minus for short positions
            const raw = e.target.value.replace(/[^\d-]/g, '');
            const clean = raw.startsWith('-') ? '-' + raw.slice(1).replace(/-/g, '') : raw.replace(/-/g, '');
            setRawLots(clean);
            lastExternalLots.current = parseInt(clean, 10) || 0;
            onLotsChange(lastExternalLots.current);
          }}
          onKeyDown={(e) => { if (e.key === 'ArrowUp' || e.key === 'ArrowDown') e.preventDefault(); }}
        />
        <input
          className={`calc-input calc-input-price${priceAuto ? ' calc-input-auto' : ''}`}
          type="text"
          inputMode="decimal"
          placeholder="цена"
          autoComplete="off"
          title={priceAuto ? 'Цена MOEX (авто). Введите своё значение для ручного режима.' : 'Цена введена вручную'}
          value={price}
          onChange={(e) => onPriceChange(e.target.value)}
          onKeyDown={(e) => { if (e.key === 'ArrowUp' || e.key === 'ArrowDown') e.preventDefault(); }}
        />
        <span
          className={`pos-pos-value${posValue == null ? ' pos-pos-value--empty' : ''}`}
          title={valueTip}
        >
          {posValue != null ? fmtRub.format(posValue) + ' ₽' : '—'}
        </span>
        <span className={`pos-funding${fund != null ? (lots * (fundingPerLot ?? 0) >= 0 ? ' income-pos' : ' income-neg') : ''}`}>
          {fund ?? ''}
        </span>
      </div>
      <button className={`pos-fav ${isFav ? 'pos-fav-active' : ''}`} onClick={onToggleFav} title="Избранное">★</button>
      <button className="pos-delete" onClick={onDelete} title="Удалить позицию">×</button>
    </div>
  );
}

interface SearchRowProps {
  inst: InstrumentInfo;
  isFav: boolean;
  onAdd: () => void;
  onToggleFav: () => void;
}

function SearchRow({ inst, isFav, onAdd, onToggleFav }: SearchRowProps) {
  const hint = inst.market_type === 'stock'
    ? (inst.lot_size > 0 ? inst.lot_size + ' шт./лот' : '—')
    : (inst.initial_margin > 0 ? fmtRub.format(inst.initial_margin) + ' ₽/лот' : '—');
  return (
    <div className="pos-row pos-row-search">
      <span className={`pos-dot pos-dot-${inst.market_type}`} title={inst.market_type === 'future' ? 'Фьючерс' : 'Акция'} />
      <span className="pos-sym">{inst.symbol}</span>
      <span className="pos-name">{inst.short_name}</span>
      <span className="pos-margin-hint">{hint}</span>
      <button className={`pos-fav ${isFav ? 'pos-fav-active' : ''}`} onClick={onToggleFav} title="Избранное">★</button>
      <button className="pos-add" onClick={onAdd}>+</button>
    </div>
  );
}

// ─── main component ──────────────────────────────────────────────────────────

type SortKey    = 'symbol' | 'margin' | 'name';
type FilterType = 'all' | 'future' | 'stock' | 'favorites';

export function CalculatorPage() {
  const current = useFundingStore((s) => s.current);

  const [deposit,     setDeposit]     = useState(() => localStorage.getItem(DEPOSIT_KEY) ?? '');
  const [leverage,    setLeverage]    = useState(() => localStorage.getItem(LEVER_KEY) ?? '');
  const [positions,   setPositions]   = useState<Record<string, number>>(loadPositions);
  const [favorites,   setFavorites]   = useState<Set<string>>(loadFavorites);
  const [userPrices,  setUserPrices]  = useState<Record<string, string>>(loadUserPrices);
  const [moexPrices,  setMoexPrices]  = useState<Record<string, number>>({});
  const [swapRates,   setSwapRates]   = useState<Record<string, number>>({});
  const [instruments, setInstruments] = useState<InstrumentInfo[]>([]);
  const [loading,     setLoading]     = useState(true);
  const [loadError,   setLoadError]   = useState(false);
  const [search,      setSearch]      = useState('');
  const [sortBy,      setSortBy]      = useState<SortKey>('symbol');
  const [filterType,  setFilterType]  = useState<FilterType>('all');

  // ── fetch instruments ────────────────────────────────────────────────────
  useEffect(() => {
    let cancelled = false;
    const load = (attempt = 0) => {
      fetch(`${API_BASE}/api/v1/all-specs`)
        .then((r) => {
          if (r.status === 503 && attempt < 10) {
            setTimeout(() => { if (!cancelled) load(attempt + 1); }, 5000);
            return;
          }
          if (!r.ok) throw new Error(r.status.toString());
          return r.json();
        })
        .then((d) => {
          if (!d || cancelled) return;
          setInstruments(d as InstrumentInfo[]);
          setLoading(false);
        })
        .catch(() =>
          fetchAllSpecsDirect()
            .then((d) => { if (!cancelled) { setInstruments(d); setLoading(false); } })
            .catch(() => { if (!cancelled) { setLoadError(true); setLoading(false); } })
        );
    };
    load();
    return () => { cancelled = true; };
  }, []);

  // ── auto-fetch market prices + funding rates (refresh every 60s) ─────────
  useEffect(() => {
    let cancelled = false;
    const refresh = () => {
      fetchPrices().then((p) => { if (!cancelled) setMoexPrices(p); }).catch(() => {});
      fetchSwapRates().then((s) => { if (!cancelled) setSwapRates(s); }).catch(() => {});
    };
    refresh();
    const id = setInterval(refresh, 60_000);
    return () => { cancelled = true; clearInterval(id); };
  }, []);

  // ── positions ────────────────────────────────────────────────────────────
  const setLots = useCallback((sym: string, lots: number) => {
    setPositions((prev) => {
      const next = { ...prev, [sym]: lots };
      savePositions(next);
      return next;
    });
  }, []);

  const addPosition = useCallback((sym: string) => {
    setPositions((prev) => {
      if (sym in prev) return prev;
      const next = { ...prev, [sym]: 1 };
      savePositions(next);
      return next;
    });
  }, []);

  const deletePosition = useCallback((sym: string) => {
    setPositions((prev) => {
      const next = { ...prev };
      delete next[sym];
      savePositions(next);
      return next;
    });
  }, []);

  const resetAll = useCallback(() => { setPositions({}); savePositions({}); }, []);

  // ── user price overrides ─────────────────────────────────────────────────
  const handlePriceChange = useCallback((sym: string, val: string) => {
    setUserPrices((prev) => {
      const next = val === '' ? { ...prev } : { ...prev, [sym]: val };
      if (val === '') delete next[sym];
      saveUserPrices(next);
      return next;
    });
  }, []);

  // ── favorites ────────────────────────────────────────────────────────────
  const toggleFav = useCallback((sym: string) => {
    setFavorites((prev) => {
      const next = new Set(prev);
      if (next.has(sym)) next.delete(sym); else next.add(sym);
      saveFavorites(next);
      return next;
    });
  }, []);

  // ── derived ──────────────────────────────────────────────────────────────
  const bySymbol = useMemo(() => {
    const m: Record<string, InstrumentInfo> = {};
    for (const i of instruments) m[i.symbol] = i;
    return m;
  }, [instruments]);

  // Live prices from WebSocket (most real-time for currency futures)
  const livePrices: Record<string, string> = {};
  if (current) {
    for (const sym of LIVE_FUNDING_SYMS) {
      const lp = current[sym as LiveSym]?.last_price;
      if (lp) livePrices[sym] = String(lp);
    }
  }

  const effectivePrice = (sym: string): string => {
    if (userPrices[sym]) return userPrices[sym];
    if (livePrices[sym]) return livePrices[sym];
    const mp = moexPrices[sym];
    return mp ? String(mp) : '';
  };

  const isPriceAuto = (sym: string): boolean =>
    !userPrices[sym] && (sym in livePrices || sym in moexPrices);

  const depositVal  = parseFloat(deposit.replace(/[\s]/g, '').replace(',', '.')) || 0;
  const leverageVal = parseFloat(leverage.replace(',', '.')) || 0;
  const maxPosition = depositVal * leverageVal;

  const activeSymbols = Object.keys(positions);

  const totalValue = activeSymbols.reduce((sum, sym) => {
    const inst = bySymbol[sym];
    const lots = positions[sym] ?? 0;
    if (!inst || lots === 0) return sum;
    // Фьючерсы учитываются по ГО (initial_margin), а не по полной стоимости контракта:
    // для удержания фьючерсной позиции блокируется только гарантийное обеспечение.
    if (inst.market_type === 'future') {
      if (inst.initial_margin <= 0) return sum;
      return sum + Math.abs(lots) * inst.initial_margin;
    }
    // Акции — по полной стоимости (lot_size × цена).
    const p = parseFloat(effectivePrice(sym).replace(',', '.')) || 0;
    if (p <= 0 || inst.lot_size <= 0) return sum;
    return sum + Math.abs(lots) * inst.lot_size * p;
  }, 0);

  const fundingBySymbol: Record<string, number> = {};
  // Live backend funding (real-time over WS) for the currency perpetuals.
  if (current) {
    for (const sym of LIVE_FUNDING_SYMS) {
      const rate = current[sym as LiveSym]?.moex_funding;
      const inst = bySymbol[sym];
      if (rate != null && inst?.lot_size) {
        fundingBySymbol[sym] = rate * inst.lot_size;
      }
    }
  }
  // MOEX SWAPRATE funding for every other perpetual future (equity perpetuals like
  // GAZPF, SBERF, …). Same formula and sign as the currency path above. The WS value
  // wins when present because it is fresher than the 60 s ISS poll.
  for (const [sym, sr] of Object.entries(swapRates)) {
    if (sym in fundingBySymbol) continue;
    const inst = bySymbol[sym];
    if (inst?.lot_size) fundingBySymbol[sym] = sr * inst.lot_size;
  }

  const totalFunding = activeSymbols.reduce((sum, sym) => {
    const perLot = fundingBySymbol[sym];
    const lots   = positions[sym] ?? 0;
    if (perLot == null || lots === 0) return sum;
    return sum + lots * perLot;
  }, 0);

  const hasFunding = activeSymbols.some((s) => fundingBySymbol[s] != null && (positions[s] ?? 0) !== 0);

  const limitOk   = maxPosition > 0 && totalValue > 0 && totalValue <= maxPosition;
  const limitOver = maxPosition > 0 && totalValue > maxPosition;

  const pricesLoaded = Object.keys(moexPrices).length > 0;

  // ── search results ───────────────────────────────────────────────────────
  const q = search.trim().toLowerCase();

  const filtered = useMemo(() => {
    let list = instruments.filter((i) => {
      if (i.symbol in positions) return false;
      if (filterType === 'future'    && i.market_type !== 'future') return false;
      if (filterType === 'stock'     && i.market_type !== 'stock')  return false;
      if (filterType === 'favorites' && !favorites.has(i.symbol))   return false;
      if (filterType !== 'favorites' && !q) return false;
      if (q && !i.symbol.toLowerCase().includes(q) && !i.short_name.toLowerCase().includes(q)) return false;
      return true;
    });

    list = [...list].sort((a, b) => {
      if (sortBy === 'margin') return b.initial_margin - a.initial_margin;
      if (sortBy === 'name')   return a.short_name.localeCompare(b.short_name, 'ru');
      return a.symbol.localeCompare(b.symbol);
    });

    return filterType === 'favorites' ? list : list.slice(0, 50);
  }, [instruments, positions, q, sortBy, filterType, favorites]);

  // ─── render ─────────────────────────────────────────────────────────────
  return (
    <div className="calc-page">

      {/* Block 1: deposit + leverage */}
      <section className="calc-section">
        <h2 className="calc-title">Лимит ночного переноса</h2>
        <div className="calc-row">
          <label className="calc-label">Депозит, ₽</label>
          <input className="calc-input" type="text" inputMode="numeric"
            placeholder="1 000 000" value={deposit}
            onChange={(e) => { setDeposit(e.target.value); localStorage.setItem(DEPOSIT_KEY, e.target.value); }} />
        </div>
        <div className="calc-row">
          <label className="calc-label">Плечо (макс. ночное)</label>
          <input className="calc-input" type="text" inputMode="decimal"
            placeholder="5" value={leverage}
            onChange={(e) => { setLeverage(e.target.value); localStorage.setItem(LEVER_KEY, e.target.value); }} />
        </div>
        {maxPosition > 0 && (
          <div className="calc-result">
            <span className="calc-result-label">Доступно к переносу (депозит × плечо)</span>
            <span className="calc-result-value">{fmtRub.format(maxPosition)} ₽</span>
          </div>
        )}
      </section>

      {/* Block 2: active positions */}
      <section className="calc-section">
        <div className="calc-section-header">
          <h2 className="calc-title">Позиции</h2>
          <div style={{ display: 'flex', alignItems: 'center', gap: 12 }}>
            {!pricesLoaded && activeSymbols.length > 0 && (
              <span className="calc-note" style={{ margin: 0 }}>загрузка цен…</span>
            )}
            {activeSymbols.length > 0 && (
              <button className="btn-reset" onClick={resetAll}>Сбросить все</button>
            )}
          </div>
        </div>

        {activeSymbols.length === 0 ? (
          <p className="calc-note">Добавьте инструменты через поиск ниже.</p>
        ) : (
          <>
            <div className="pos-header-row">
              <span /><span /><span />
              <span className="pos-col-label">лоты</span>
              <span className="pos-col-label">цена ₽</span>
              <span className="pos-col-label">позиция ₽</span>
              <span className="pos-col-label">фандинг</span>
              <span /><span />
            </div>
            <div className="pos-list">
              {activeSymbols.map((sym) => {
                const inst = bySymbol[sym];
                if (!inst) return null;
                return (
                  <ActiveRow
                    key={sym}
                    inst={inst}
                    lots={positions[sym] ?? 0}
                    price={effectivePrice(sym)}
                    priceAuto={isPriceAuto(sym)}
                    isFav={favorites.has(sym)}
                    onLotsChange={(v) => setLots(sym, v)}
                    onPriceChange={(v) => handlePriceChange(sym, v)}
                    onDelete={() => deletePosition(sym)}
                    onToggleFav={() => toggleFav(sym)}
                    fundingPerLot={fundingBySymbol[sym] ?? null}
                  />
                );
              })}
            </div>

            {totalValue > 0 && (
              <div className={`calc-result calc-result-value-total${limitOver ? ' calc-result-over' : limitOk ? ' calc-result-ok' : ''}`}>
                <span className="calc-result-label">Объём позиций</span>
                <span className="calc-result-value">{fmtRub.format(totalValue)} ₽</span>
              </div>
            )}

            {maxPosition > 0 && totalValue > 0 && (
              <div className={`calc-result ${limitOver ? 'calc-result-over' : 'calc-result-ok'}`}>
                <span className="calc-result-label">Остаток лимита на перенос</span>
                <span className="calc-result-value">{fmtRub.format(maxPosition - totalValue)} ₽</span>
              </div>
            )}

            {maxPosition > 0 && totalValue > 0 && (
              <div className={`calc-verdict ${limitOver ? 'verdict-over' : 'verdict-ok'}`}>
                {limitOver
                  ? `Превышение лимита на ${fmtInt.format(totalValue - maxPosition)} ₽ — перенос платный`
                  : 'В пределах лимита — перенос бесплатный'}
              </div>
            )}

            {hasFunding && (
              <div className={`calc-result ${totalFunding >= 0 ? 'calc-result-ok' : 'calc-result-over'}`}>
                <span className="calc-result-label">Итого фандинг (MOEX)</span>
                <span className="calc-result-value">
                  {totalFunding >= 0 ? '+' : ''}{fmtRub.format(totalFunding)} ₽
                </span>
              </div>
            )}
          </>
        )}
      </section>

      {/* Block 3: search + add */}
      <section className="calc-section">
        <h2 className="calc-title">Добавить инструмент</h2>

        {loadError && <p className="calc-note calc-note-warn">Не удалось загрузить список инструментов с MOEX.</p>}
        {loading && <p className="calc-note">Загрузка инструментов с MOEX… (может занять до 30 сек при первом старте)</p>}

        {!loading && !loadError && (
          <>
            <div className="search-bar">
              <input
                className="calc-input search-input"
                type="text"
                placeholder="Поиск по тикеру или названию…"
                value={search}
                onChange={(e) => setSearch(e.target.value)}
                autoComplete="off"
              />
              <div className="search-filters">
                {(['all', 'future', 'stock', 'favorites'] as FilterType[]).map((t) => (
                  <button
                    key={t}
                    className={`filter-btn ${filterType === t ? 'filter-btn-active' : ''}`}
                    onClick={() => setFilterType(t)}
                  >
                    {t === 'all' ? 'Все' : t === 'future' ? 'Фьючерсы' : t === 'stock' ? 'Акции' : '★ Избранное'}
                  </button>
                ))}
                <span className="search-sort-label">Сортировка:</span>
                {(['symbol', 'name', 'margin'] as SortKey[]).map((s) => (
                  <button
                    key={s}
                    className={`filter-btn ${sortBy === s ? 'filter-btn-active' : ''}`}
                    onClick={() => setSortBy(s)}
                  >
                    {s === 'symbol' ? 'Тикер' : s === 'name' ? 'Название' : 'ГО'}
                  </button>
                ))}
              </div>
            </div>

            {filterType !== 'favorites' && !q && (
              <p className="calc-note">Начните вводить тикер или название…</p>
            )}
            {(q || filterType === 'favorites') && filtered.length === 0 && (
              <p className="calc-note">
                {filterType === 'favorites' ? 'В избранном пока ничего нет. Нажмите ★ рядом с инструментом.' : `Ничего не найдено по «${search}».`}
              </p>
            )}

            {filtered.length > 0 && (
              <div className="pos-list pos-list-search">
                {filtered.map((inst) => (
                  <SearchRow
                    key={inst.symbol}
                    inst={inst}
                    isFav={favorites.has(inst.symbol)}
                    onAdd={() => addPosition(inst.symbol)}
                    onToggleFav={() => toggleFav(inst.symbol)}
                  />
                ))}
              </div>
            )}
          </>
        )}
      </section>
    </div>
  );
}
