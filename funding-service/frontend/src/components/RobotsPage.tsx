import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import type { DayVolume, RobotSession, RobotsResponse, StreamStatus } from '../types/robots';
import { authFetch } from '../api/auth';

const fmtClock = new Intl.DateTimeFormat('ru-RU', {
  timeZone: 'Europe/Moscow', hour: '2-digit', minute: '2-digit', second: '2-digit',
});
const fmtDayClock = new Intl.DateTimeFormat('ru-RU', {
  timeZone: 'Europe/Moscow', day: '2-digit', month: '2-digit',
  hour: '2-digit', minute: '2-digit', second: '2-digit',
});
const fmtLots = new Intl.NumberFormat('ru-RU');

// TICK_MS — с этим шагом пересчитываются обратные отсчёты и проверяется, не пора
// ли пикнуть. Мельче не нужно: предупреждения выдаются посекундно.
const TICK_MS = 200;
// ALARM_LEAD — за сколько секунд до удара начинают звучать предупреждения.
const ALARM_LEAD = 3;
// Порог силы. Ключ хранения с версией: сила теперь считается от часового оборота,
// а не от дневного, и старое сохранённое значение означало бы совсем другое.
const THRESHOLD_KEY = 'robots.strength-threshold.v2';
const DEFAULT_THRESHOLD = 1;

function clock(iso: string): string {
  try { return fmtClock.format(new Date(iso)); } catch { return iso; }
}
function dayClock(iso: string): string {
  try { return fmtDayClock.format(new Date(iso)); } catch { return iso; }
}

// lots — лотовка: одно число, если робот печатает строго один размер, иначе диапазон.
function lots(r: RobotSession): string {
  const lo = Math.round(r.qty_min);
  const hi = Math.round(r.qty_max);
  return lo === hi ? `${fmtLots.format(lo)} л` : `${fmtLots.format(lo)}–${fmtLots.format(hi)} л`;
}

// period — тайминг. Секунды до десятых, дальше минуты: «11.2 с», «2 мин 05 с».
function period(sec: number): string {
  if (sec < 60) return `${sec.toFixed(1)} с`;
  const m = Math.floor(sec / 60);
  const s = Math.round(sec - m * 60);
  return `${m} мин ${String(s).padStart(2, '0')} с`;
}

// duration — сколько робот работает, от первого принта серии до последнего.
function duration(r: RobotSession): string {
  const ms = new Date(r.last_seen).getTime() - new Date(r.first_seen).getTime();
  if (!Number.isFinite(ms) || ms < 0) return '—';
  const total = Math.round(ms / 1000);
  const h = Math.floor(total / 3600);
  const m = Math.floor((total - h * 3600) / 60);
  const s = total - h * 3600 - m * 60;
  if (h > 0) return `${h} ч ${String(m).padStart(2, '0')} мин`;
  if (m > 0) return `${m} мин ${String(s).padStart(2, '0')} с`;
  return `${s} с`;
}

function price(v: number): string {
  return v.toLocaleString('ru-RU', { maximumFractionDigits: 4 });
}

// printOf — объём робота за раз: сколько лотов проходит один его принт. Живой
// срез считает его на сервере, строки истории приходят из базы без досчитанных
// полей — там берём типичный размер принта.
function printOf(r: RobotSession): number {
  return r.print_lots || r.qty_typical;
}

// volumeOf — суммарный объём серии. Тот же порядок: сервер, иначе из лотовки.
function volumeOf(r: RobotSession): number {
  return r.volume_lots || r.qty_typical * r.prints;
}

function volumeLabel(lotsCount: number): string {
  return `${fmtLots.format(Math.round(lotsCount))} л`;
}

// strengthLabel — сила робота: один его принт в доле часового оборота бумаги.
// Знаменатель большой, доли идут долями процента — на слабых роботах нужны сотые,
// иначе половина списка показывает «0.0%».
function strengthLabel(pct: number): string {
  if (!pct) return '—';
  if (pct >= 10) return `${pct.toFixed(0)}%`;
  if (pct >= 1) return `${pct.toFixed(1)}%`;
  return `${pct.toFixed(2)}%`;
}

// confidenceLabel — уверенность словами: цифра 0.62 читателю ничего не говорит.
function confidenceLabel(c: number): string {
  if (c >= 0.75) return 'высокая';
  if (c >= 0.5) return 'средняя';
  return 'низкая';
}

// nextBeatAt — когда робот ударит в следующий раз, в миллисекундах.
//
// Фаза продолжается вперёд от последнего принта, а не берётся как «последний
// принт плюс период»: лента запаздывает (у потока брокера — на секунды, у фида
// ISS — на четверть часа), и к моменту отрисовки робот успевает отработать
// такты, которых страница ещё не видела.
function nextBeatAt(r: RobotSession, nowMs: number): number {
  const periodMs = r.period_sec * 1000;
  const last = new Date(r.last_seen).getTime();
  if (!Number.isFinite(periodMs) || periodMs <= 0 || !Number.isFinite(last)) return 0;
  const beats = Math.floor((nowMs - last) / periodMs) + 1;
  return last + Math.max(1, beats) * periodMs;
}

// countdown — «через 7.4 с», «через 2:05».
function countdown(ms: number): string {
  if (!Number.isFinite(ms) || ms < 0) return '—';
  const sec = ms / 1000;
  if (sec < 10) return `${sec.toFixed(1)} с`;
  if (sec < 60) return `${Math.round(sec)} с`;
  const m = Math.floor(sec / 60);
  const s = Math.round(sec - m * 60);
  return `${m}:${String(s).padStart(2, '0')}`;
}

// lagLabel — отставание ленты по-человечески: «1.7 с», «15 мин».
function lagLabel(ms: number): string {
  if (ms < 1000) return `${Math.round(ms)} мс`;
  if (ms < 60_000) return `${(ms / 1000).toFixed(1)} с`;
  return `${Math.round(ms / 60_000)} мин`;
}

// SourceNote — откуда берутся принты и насколько они свежие.
//
// Пишется по факту, а не заранее: у сервиса два источника. Поток брокера
// приносит сделку за секунду-другую, публичный фид MOEX ISS — ровно через
// пятнадцать минут, и на этих двух источниках «до удара» означает разное.
function SourceNote({ stream }: { stream: StreamStatus | null }) {
  if (!stream || !stream.enabled) {
    return (
      <>
        Лента идёт из публичного фида MOEX ISS, а он приходит с задержкой ровно
        в 15 минут: время до удара здесь — продолжение такта вперёд по известному
        периоду, а не наблюдение.
      </>
    );
  }
  if (!stream.connected) {
    return (
      <>
        Поток брокера сейчас оборван, лента идёт из публичного фида MOEX ISS —
        это задержка в 15 минут, и время до удара пока чистая экстраполяция.
        Соединение восстанавливается само.
      </>
    );
  }
  return (
    <>
      Принты приходят потоком брокера
      {stream.symbols > 0 && <> по {stream.symbols} инструментам</>}
      {stream.lag_ms > 0 && <>, отставание от биржи {lagLabel(stream.lag_ms)}</>}
      {' '}— время до удара считается почти по наблюдению. Бумаги вне потока
      идут из фида MOEX ISS с задержкой в 15 минут.
    </>
  );
}

// beep — короткий сигнал через WebAudio. Готовых звуковых файлов не держим:
// страница отдаётся под строгим CSP, а осциллятор не требует внешних ресурсов.
function beep(ctx: AudioContext, freq: number, durationSec: number) {
  const osc = ctx.createOscillator();
  const gain = ctx.createGain();
  osc.type = 'sine';
  osc.frequency.value = freq;
  // Огибающая со сглаженными краями: прямоугольный импульс щёлкает в динамике.
  const t0 = ctx.currentTime;
  gain.gain.setValueAtTime(0.0001, t0);
  gain.gain.exponentialRampToValueAtTime(0.25, t0 + 0.01);
  gain.gain.exponentialRampToValueAtTime(0.0001, t0 + durationSec);
  osc.connect(gain).connect(ctx.destination);
  osc.start(t0);
  osc.stop(t0 + durationSec + 0.02);
}

// Range — границы фильтра по графе. Держим строками, а не числами: пустое поле
// означает «без ограничения», и отличить его от осмысленного нуля можно только так.
interface Range { min: string; max: string }

const EMPTY_RANGE: Range = { min: '', max: '' };

// inRange — проходит ли значение границы. Незаполненная граница не ограничивает,
// как и та, где ввели не число (пользователь ещё печатает).
function inRange(value: number, r: Range): boolean {
  const lo = Number(r.min);
  const hi = Number(r.max);
  if (r.min !== '' && Number.isFinite(lo) && value < lo) return false;
  if (r.max !== '' && Number.isFinite(hi) && value > hi) return false;
  return true;
}

function rangeActive(r: Range): boolean {
  return r.min !== '' || r.max !== '';
}

// RangeFilter — пара полей «от» и «до» для одной графы.
function RangeFilter({ label, unit, value, onChange }: {
  label: string;
  unit: string;
  value: Range;
  onChange: (r: Range) => void;
}) {
  return (
    <label className={`rb-range${rangeActive(value) ? ' rb-range-on' : ''}`}>
      <span className="rb-range-label">{label}</span>
      <input
        type="number"
        className="rb-range-input"
        inputMode="decimal"
        placeholder="от"
        value={value.min}
        onChange={(e) => onChange({ ...value, min: e.target.value })}
      />
      <span className="rb-range-dash">–</span>
      <input
        type="number"
        className="rb-range-input"
        inputMode="decimal"
        placeholder="до"
        value={value.max}
        onChange={(e) => onChange({ ...value, max: e.target.value })}
      />
      <span className="rb-range-unit">{unit}</span>
    </label>
  );
}

function Detail({ label, value }: { label: string; value: string }) {
  return (
    <div className="jrn-detail">
      <span className="jrn-detail-label">{label}</span>
      <span className="jrn-detail-value">{value}</span>
    </div>
  );
}

interface RowProps {
  r: RobotSession;
  nowMs: number;
  live: boolean;
  threshold: number;
  armed: boolean;
  onToggleAlarm: (id: number) => void;
  day?: DayVolume;
}

// RobotRow — один робот. В свёрнутом виде — графы таблицы, по клику раскрываются
// подробности серии.
function RobotRow({ r, nowMs, live, threshold, armed, onToggleAlarm, day }: RowProps) {
  const long = r.side === 'B';
  const once = printOf(r);
  const volume = volumeOf(r);
  const beatMs = live && r.active ? nextBeatAt(r, nowMs) : 0;
  const left = beatMs ? beatMs - nowMs : NaN;

  // Сильный робот подсвечивается в свою сторону: зелёный — набирает, красный — льёт.
  const strong = r.strength_pct >= threshold && r.strength_pct > 0;
  const classes = ['jrn-card', 'rb-card'];
  // Живая строка шире на колонку кнопки сигнала — в истории её нет, и сетка
  // строки должна отличаться, иначе справа зияет пустая графа.
  if (live) classes.push('rb-card-live');
  if (r.misses > 0) classes.push('rb-missed');
  if (strong) classes.push(long ? 'rb-strong-long' : 'rb-strong-short');

  return (
    <details className={classes.join(' ')}>
      <summary className="jrn-summary rb-summary">
        <div className="rb-col rb-symbol">
          <span className="rb-caption">тикер</span>
          <span className="rb-symbol-val">{r.symbol}</span>
        </div>

        <div className="rb-col">
          <span className="rb-caption">направление</span>
          <span className={`rb-side ${long ? 'rb-long' : 'rb-short'}`}>
            {long ? 'ЛОНГ' : 'ШОРТ'}
          </span>
        </div>

        <div className="rb-col">
          <span className="rb-caption">объём за раз</span>
          <span className="rb-val">{volumeLabel(once)}</span>
        </div>

        <div className="rb-col">
          <span className="rb-caption">тайминг</span>
          <span className="rb-val rb-period">{period(r.period_sec)}</span>
        </div>

        <div className="rb-col">
          <span className="rb-caption">до удара</span>
          <span className={`rb-val rb-beat${Number.isFinite(left) && left <= ALARM_LEAD * 1000 ? ' rb-beat-soon' : ''}`}>
            {live && r.active ? countdown(left) : '—'}
          </span>
        </div>

        <div className="rb-col">
          <span className="rb-caption">сила</span>
          <span
            className={`rb-val rb-strength${strong ? (long ? ' rb-strength-long' : ' rb-strength-short') : ''}`}
            title={r.hour_lots > 0
              ? `Принт ${volumeLabel(once)} к обороту ${volumeLabel(r.hour_lots)} за час ${r.hour_from}–${r.hour_to}`
              : 'Часового оборота по бумаге ещё нет'}
          >
            {strengthLabel(r.strength_pct)}
          </span>
        </div>

        <div className="rb-col">
          <span className="rb-caption">объём серии</span>
          <span className="rb-val rb-dim">{volumeLabel(volume)}</span>
        </div>

        <div className="rb-col rb-status-col">
          <span className="rb-caption">статус</span>
          {r.misses > 0
            ? <span className="rb-status rb-warn">пропустил такт</span>
            : r.active
              ? <span className="rb-status rb-active">{r.provisional ? 'предварительно' : 'работает'}</span>
              : <span className="rb-status rb-stopped">замолчал в {clock(r.last_seen)}</span>}
        </div>

        {live && (
          <button
            type="button"
            className={`rb-alarm${armed ? ' rb-alarm-on' : ''}`}
            title={armed ? 'Выключить сигнал' : `Сигнал за ${ALARM_LEAD} с до удара`}
            aria-pressed={armed}
            onClick={(e) => { e.preventDefault(); onToggleAlarm(r.id); }}
          >
            {armed ? '🔔' : '🔕'}
          </button>
        )}

        <span className="jrn-chevron" aria-hidden="true">▸</span>
      </summary>

      <div className="jrn-details">
        <div className="jrn-detail-group">
          <div className="jrn-detail-title">Серия</div>
          <Detail label="Первый принт" value={dayClock(r.first_seen)} />
          <Detail label="Последний принт" value={dayClock(r.last_seen)} />
          <Detail label="Работает" value={duration(r)} />
          <Detail label="Тактов периода" value={String(r.beats)} />
          <Detail label="Пропущено тактов подряд" value={String(r.misses)} />
        </div>

        <div className="jrn-detail-group">
          <div className="jrn-detail-title">Лотовка и объём</div>
          <Detail label="Объём за раз" value={volumeLabel(once)} />
          <Detail label="Границы размера" value={lots(r)} />
          <Detail label="Суммарный объём серии" value={volumeLabel(volume)} />
          {r.hour_lots > 0 && (
            <Detail
              label={`Оборот бумаги за час ${r.hour_from}–${r.hour_to}`}
              value={volumeLabel(r.hour_lots)}
            />
          )}
          {r.hour_since && (
            <Detail label="Часовой оборот считается с" value={`${r.hour_since} МСК`} />
          )}
          {r.day_side_lots > 0 && (
            <Detail
              label={long ? 'Куплено за день' : 'Продано за день'}
              value={volumeLabel(r.day_side_lots)}
            />
          )}
          <Detail label="Сила: принт к часовому обороту" value={strengthLabel(r.strength_pct)} />
        </div>

        <div className="jrn-detail-group">
          <div className="jrn-detail-title">Тайминг</div>
          <Detail label="Период" value={period(r.period_sec)} />
          <Detail label="Разброс интервалов" value={`±${(r.jitter * 100).toFixed(1)}%`} />
          <Detail label="Уверенность" value={`${confidenceLabel(r.confidence)} (${r.confidence.toFixed(2)})`} />
          {beatMs > 0 && <Detail label="Следующий удар" value={clock(new Date(beatMs).toISOString())} />}
        </div>

        <div className="jrn-detail-group">
          <div className="jrn-detail-title">Цена</div>
          <Detail label="На первом принте" value={price(r.price_first)} />
          <Detail label="На последнем" value={price(r.price_last)} />
          <Detail label="Замечен сервисом" value={dayClock(r.detected_at)} />
          {day && <Detail label="Оборот считается с" value={`${day.since} МСК`} />}
        </div>
      </div>
    </details>
  );
}

// SymbolGroup — все роботы одной бумаги под общей строкой-итогом.
interface GroupProps extends Omit<RowProps, 'r' | 'armed'> {
  symbol: string;
  rows: RobotSession[];
  armedIds: Set<number>;
}

function SymbolGroup({ symbol, rows, nowMs, live, threshold, armedIds, onToggleAlarm, day }: GroupProps) {
  // Одинокого робота показываем сразу раскрытым, но дальше решает пользователь:
  // страница перерисовывается несколько раз в секунду из-за обратных отсчётов,
  // и раскрытие, заданное выражением, было бы во власти этих перерисовок.
  const [open, setOpen] = useState(rows.length === 1);
  const longs = rows.filter((r) => r.side === 'B');
  const shorts = rows.filter((r) => r.side === 'S');
  const sum = (xs: RobotSession[]) => xs.reduce((acc, r) => acc + volumeOf(r), 0);
  const longVol = sum(longs);
  const shortVol = sum(shorts);

  // Часовой оборот бумаги считается на сервере от последнего принта робота, у
  // роботов одной бумаги он практически совпадает; берём максимум как наиболее
  // свежее окно.
  const hourLots = Math.max(...rows.map((r) => r.hour_lots), 0);

  // Итоговая сила берётся как сила самого крупного робота стороны, а не как
  // сумма: сила — это вес одного принта, а такты роботов не синхронны, и складывать
  // принты, приходящие в разные секунды, значило бы придумывать несуществующий залп.
  const strongest = (xs: RobotSession[]) => Math.max(...xs.map((r) => r.strength_pct), 0);
  const longPct = strongest(longs);
  const shortPct = strongest(shorts);
  const leadLong = longPct >= shortPct;
  const leadPct = Math.max(longPct, shortPct);

  // Ближайший удар по бумаге — самый скорый среди работающих роботов.
  const nextMs = live
    ? rows
      .filter((r) => r.active)
      .map((r) => nextBeatAt(r, nowMs))
      .filter((ms) => ms > 0)
      .reduce((min, ms) => (min === 0 || ms < min ? ms : min), 0)
    : 0;

  const missed = rows.some((r) => r.misses > 0);
  const strong = leadPct >= threshold && leadPct > 0;
  const classes = ['rb-group'];
  if (missed) classes.push('rb-missed');
  if (strong) classes.push(leadLong ? 'rb-strong-long' : 'rb-strong-short');

  return (
    <details
      className={classes.join(' ')}
      open={open}
      onToggle={(e) => setOpen(e.currentTarget.open)}
    >
      <summary className="rb-group-summary">
        <div className="rb-col rb-symbol">
          <span className="rb-caption">тикер</span>
          <span className="rb-symbol-val">
            {symbol}
            <span className="rb-badge">{rows.length}</span>
          </span>
        </div>

        <div className="rb-col">
          <span className="rb-caption">направление</span>
          <span className="rb-val rb-mix">
            {longs.length > 0 && <span className="rb-long">{longs.length} Л</span>}
            {longs.length > 0 && shorts.length > 0 && <span className="rb-mix-sep">/</span>}
            {shorts.length > 0 && <span className="rb-short">{shorts.length} Ш</span>}
          </span>
        </div>

        <div className="rb-col">
          <span className="rb-caption">оборот за час</span>
          <span className="rb-val rb-dim">{hourLots > 0 ? volumeLabel(hourLots) : '—'}</span>
        </div>

        <div className="rb-col">
          <span className="rb-caption">ближайший удар</span>
          <span className={`rb-val rb-beat${nextMs > 0 && nextMs - nowMs <= ALARM_LEAD * 1000 ? ' rb-beat-soon' : ''}`}>
            {nextMs > 0 ? countdown(nextMs - nowMs) : '—'}
          </span>
        </div>

        <div className="rb-col">
          <span className="rb-caption">сила</span>
          <span
            className={`rb-val rb-strength${strong ? (leadLong ? ' rb-strength-long' : ' rb-strength-short') : ''}`}
            title="Сила самого крупного робота бумаги: его принт в доле часового оборота"
          >
            {leadPct > 0 ? strengthLabel(leadPct) : '—'}
          </span>
        </div>

        <div className="rb-col">
          <span className="rb-caption">суммарный объём</span>
          <span className="rb-val rb-dim" title={`лонг ${volumeLabel(longVol)} · шорт ${volumeLabel(shortVol)}`}>
            {volumeLabel(longVol + shortVol)}
          </span>
        </div>

        <span className="jrn-chevron" aria-hidden="true">▸</span>
      </summary>

      <div className="rb-group-body">
        {rows.map((r) => (
          <RobotRow
            key={`${r.id}-${r.first_seen}`}
            r={r}
            nowMs={nowMs}
            live={live}
            threshold={threshold}
            armed={armedIds.has(r.id)}
            onToggleAlarm={onToggleAlarm}
            day={day}
          />
        ))}
      </div>
    </details>
  );
}

type Tab = 'live' | 'history';

export function RobotsPage() {
  const [tab, setTab] = useState<Tab>('live');
  const [rows, setRows] = useState<RobotSession[]>([]);
  const [watching, setWatching] = useState<string[]>([]);
  const [tapes, setTapes] = useState<string[]>([]);
  const [watchRule, setWatchRule] = useState('');
  const [days, setDays] = useState<DayVolume[]>([]);
  const [stream, setStream] = useState<StreamStatus | null>(null);
  const [symbol, setSymbol] = useState('');
  const [confirmedOnly, setConfirmedOnly] = useState(false);
  // Фильтры по графам таблицы. Намеренно не сохраняются между сеансами: пустая
  // страница назавтра из-за забытого фильтра выглядит как сломанный сбор.
  const [filtersOpen, setFiltersOpen] = useState(false);
  const [dirFilter, setDirFilter] = useState<'' | 'B' | 'S'>('');
  const [periodRange, setPeriodRange] = useState<Range>(EMPTY_RANGE);
  const [qtyRange, setQtyRange] = useState<Range>(EMPTY_RANGE);
  const [volumeRange, setVolumeRange] = useState<Range>(EMPTY_RANGE);
  const [strengthRange, setStrengthRange] = useState<Range>(EMPTY_RANGE);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [armedIds, setArmedIds] = useState<Set<number>>(new Set());
  const [threshold, setThreshold] = useState<number>(() => {
    const saved = Number(localStorage.getItem(THRESHOLD_KEY));
    return Number.isFinite(saved) && saved > 0 ? saved : DEFAULT_THRESHOLD;
  });
  // Черновик поля порога. Отдельно от самого порога: набирая «0.5», пользователь
  // проходит через пустую строку и через «0.», а фильтровать по ним нельзя —
  // раньше поле просто отказывалось стираться, и число нельзя было перебить.
  const [thresholdDraft, setThresholdDraft] = useState(() => String(threshold));

  // Часы страницы: один таймер на всю таблицу вместо таймера в каждой строке.
  const [nowMs, setNowMs] = useState(() => Date.now());
  // skew — поправка на расхождение часов браузера и сервера. Без неё сбитые
  // локальные часы сдвинули бы весь обратный отсчёт и звук вместе с ним.
  const skewRef = useRef(0);
  const audioRef = useRef<AudioContext | null>(null);
  const firedRef = useRef<Set<string>>(new Set());

  const live = tab === 'live';

  const load = useCallback(async (which: Tab) => {
    try {
      const path = which === 'live' ? '/api/v1/robots' : '/api/v1/robots/history?days=7&limit=500';
      const resp = await authFetch(path);
      if (!resp.ok) throw new Error(`HTTP ${resp.status}`);
      if (which === 'live') {
        const data = (await resp.json()) as RobotsResponse;
        setRows(data.robots ?? []);
        setWatching(data.watching ?? []);
        setTapes(data.tapes ?? []);
        setWatchRule(data.watch_rule ?? '');
        setDays(data.day_volumes ?? []);
        setStream(data.stream ?? null);
        const asOf = Date.parse(data.as_of);
        if (Number.isFinite(asOf)) skewRef.current = asOf - Date.now();
      } else {
        setRows(((await resp.json()) as RobotSession[] | null) ?? []);
        setDays([]);
      }
      setError(null);
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    } finally {
      setLoading(false);
    }
  }, []);

  // Первая загрузка и смена вкладки. load() трогает состояние только после ответа
  // сервера, поэтому правило про setState в эффекте здесь неприменимо.
  // eslint-disable-next-line react-hooks/set-state-in-effect
  useEffect(() => { void load(tab); }, [load, tab]);

  // Живая вкладка обновляется сама: робот появляется и замолкает без перезагрузки.
  useEffect(() => {
    if (!live) return;
    const id = setInterval(() => { void load('live'); }, 15000);
    return () => clearInterval(id);
  }, [load, live]);

  // Ход часов: обратный отсчёт идёт между опросами сервера, а не рывками по ним.
  useEffect(() => {
    if (!live) return;
    const id = setInterval(() => setNowMs(Date.now() + skewRef.current), TICK_MS);
    return () => clearInterval(id);
  }, [live]);

  useEffect(() => { localStorage.setItem(THRESHOLD_KEY, String(threshold)); }, [threshold]);

  const toggleAlarm = useCallback((id: number) => {
    // Звук в браузере можно завести только из обработчика клика — поэтому
    // контекст создаётся здесь, а не при загрузке страницы.
    if (!audioRef.current) {
      const Ctor = window.AudioContext ?? (window as unknown as { webkitAudioContext?: typeof AudioContext }).webkitAudioContext;
      if (Ctor) audioRef.current = new Ctor();
    }
    void audioRef.current?.resume();
    setArmedIds((prev) => {
      const next = new Set(prev);
      if (next.has(id)) next.delete(id);
      else next.add(id);
      return next;
    });
  }, []);

  const shown = useMemo(
    () => rows.filter((r) => (!symbol || r.symbol === symbol)
      && (!confirmedOnly || !r.provisional)
      && (!dirFilter || r.side === dirFilter)
      && inRange(r.period_sec, periodRange)
      && inRange(printOf(r), qtyRange)
      && inRange(volumeOf(r), volumeRange)
      && inRange(r.strength_pct, strengthRange)),
    [rows, symbol, confirmedOnly, dirFilter, periodRange, qtyRange, volumeRange, strengthRange],
  );

  const filtersOn = Boolean(symbol) || confirmedOnly || Boolean(dirFilter)
    || rangeActive(periodRange) || rangeActive(qtyRange)
    || rangeActive(volumeRange) || rangeActive(strengthRange);

  const resetFilters = () => {
    setSymbol('');
    setFiltersOpen(false);
    setConfirmedOnly(false);
    setDirFilter('');
    setPeriodRange(EMPTY_RANGE);
    setQtyRange(EMPTY_RANGE);
    setVolumeRange(EMPTY_RANGE);
    setStrengthRange(EMPTY_RANGE);
  };

  // Сколько предварительных сейчас скрыто/показано — чтобы низкий порог
  // обнаружения не выглядел как поломка: на валюте пара случайных сделок
  // одного размера заведомо попадает в список.
  const provisionalCount = useMemo(
    () => rows.filter((r) => r.provisional && (!symbol || r.symbol === symbol)).length,
    [rows, symbol],
  );

  const dayBySymbol = useMemo(() => {
    const m = new Map<string, DayVolume>();
    days.forEach((d) => m.set(d.symbol, d));
    return m;
  }, [days]);

  // Три предупреждения перед ударом, по одному на секунду. Проверяем на каждом
  // тике часов и помним уже отыгранные, чтобы один и тот же такт не пикал дважды.
  useEffect(() => {
    const ctx = audioRef.current;
    if (!live || !ctx || armedIds.size === 0) return;

    const fired = firedRef.current;
    for (const r of shown) {
      if (!armedIds.has(r.id) || !r.active) continue;
      const beatMs = nextBeatAt(r, nowMs);
      if (!beatMs) continue;
      const secLeft = Math.ceil((beatMs - nowMs) / 1000);
      if (secLeft < 1 || secLeft > ALARM_LEAD) continue;
      const key = `${r.id}:${beatMs}:${secLeft}`;
      if (fired.has(key)) continue;
      fired.add(key);
      // Последнее предупреждение выше и длиннее — на слух отличимо от первых двух.
      beep(ctx, secLeft === 1 ? 990 : 660, secLeft === 1 ? 0.18 : 0.08);
    }
    if (fired.size > 256) fired.clear();
  }, [nowMs, armedIds, shown, live]);

  // Группировка по бумаге: несколько роботов на одном тикере сводятся в строку-итог.
  const groups = useMemo(() => {
    const bySymbol = new Map<string, RobotSession[]>();
    shown.forEach((r) => {
      const list = bySymbol.get(r.symbol);
      if (list) list.push(r);
      else bySymbol.set(r.symbol, [r]);
    });
    return [...bySymbol.entries()]
      .map(([sym, list]) => ({
        symbol: sym,
        rows: [...list].sort((a, b) => b.strength_pct - a.strength_pct || b.confidence - a.confidence),
      }))
      .sort((a, b) => {
        const strength = (g: { rows: RobotSession[] }) => Math.max(...g.rows.map((r) => r.strength_pct), 0);
        return strength(b) - strength(a) || a.symbol.localeCompare(b.symbol);
      });
  }, [shown]);

  const symbols = useMemo(() => {
    const set = new Set<string>(watching);
    rows.forEach((r) => set.add(r.symbol));
    return [...set].sort();
  }, [rows, watching]);

  const refresh = () => { setLoading(true); void load(tab); };

  return (
    <div className="race-page">
      <div className="race-header">
        <h2 className="race-title">Поиск роботов</h2>
        <button className="race-btn-run" onClick={refresh} disabled={loading}>
          {loading ? '⏳ Загрузка…' : '↻ Обновить'}
        </button>
      </div>

      <p className="race-subtitle">
        Сервис пишет каждый принт по каждому тикеру и ищет повторы: если сделка одного
        размера (±1 лот) проходит через ровные промежутки времени — это робот. На валюте
        робот заявляется с третьего повторяющегося принта и до шестого помечается
        предварительным; на остальных инструментах серия должна дорасти до шести принтов,
        иначе на ленте всего рынка список тонет в случайных совпадениях. Пропустил такт —
        строка желтеет, пропустил второй подряд — робот уходит со страницы в историю.
        {' '}<SourceNote stream={stream} />
      </p>

      <div className="rb-controls">
        <div className="rb-tabs">
          <button
            className={`rb-tab${live ? ' rb-tab-active' : ''}`}
            onClick={() => { setTab('live'); setLoading(true); }}
          >
            Сейчас
          </button>
          <button
            className={`rb-tab${!live ? ' rb-tab-active' : ''}`}
            onClick={() => { setTab('history'); setLoading(true); }}
          >
            История за неделю
          </button>
        </div>

        <label className="rb-filter">
          Тикер
          <select value={symbol} onChange={(e) => setSymbol(e.target.value)}>
            <option value="">все</option>
            {symbols.map((s) => <option key={s} value={s}>{s}</option>)}
          </select>
        </label>

        <label className="rb-filter" title="Доля одного принта робота в часовом обороте бумаги, с которой он считается сильным">
          Сильный робот от
          <input
            className="rb-threshold"
            type="number"
            min={0.01}
            max={100}
            step={0.1}
            inputMode="decimal"
            value={thresholdDraft}
            onChange={(e) => {
              setThresholdDraft(e.target.value);
              const v = Number(e.target.value);
              if (Number.isFinite(v) && v > 0) setThreshold(v);
            }}
            onBlur={() => setThresholdDraft(String(threshold))}
          />
          % часового оборота
        </label>

        <label
          className="rb-checkbox"
          title="Предварительная находка — серия короче шести принтов: периодичность в ней ещё не отличима от случайного совпадения"
        >
          <input
            type="checkbox"
            checked={confirmedOnly}
            onChange={(e) => setConfirmedOnly(e.target.checked)}
          />
          только подтверждённые
          {provisionalCount > 0 && <span className="rb-badge">{provisionalCount}</span>}
        </label>
      </div>

      {/* Фильтры по графам таблицы: сужают список строк, не трогая сам поиск роботов. */}
      <details
        className="rb-colfilters"
        open={filtersOpen}
        onToggle={(e) => setFiltersOpen(e.currentTarget.open)}
      >
        <summary className="rb-colfilters-summary">
          Фильтр по графам
          {filtersOn && <span className="rb-badge">{shown.length} из {rows.length}</span>}
          <span className="jrn-chevron" aria-hidden="true">▸</span>
        </summary>

        <div className="rb-colfilters-body">
          <label className="rb-filter">
            Направление
            <select value={dirFilter} onChange={(e) => setDirFilter(e.target.value as '' | 'B' | 'S')}>
              <option value="">все</option>
              <option value="B">лонг</option>
              <option value="S">шорт</option>
            </select>
          </label>

          <RangeFilter label="Тайминг" unit="с" value={periodRange} onChange={setPeriodRange} />
          <RangeFilter label="Объём за раз" unit="л" value={qtyRange} onChange={setQtyRange} />
          <RangeFilter label="Суммарный объём" unit="л" value={volumeRange} onChange={setVolumeRange} />
          <RangeFilter label="Сила" unit="%" value={strengthRange} onChange={setStrengthRange} />

          <button
            type="button"
            className="rb-reset"
            onClick={resetFilters}
            disabled={!filtersOn}
          >
            Сбросить
          </button>
        </div>
      </details>

      {error && <p className="race-error">Ошибка загрузки: {error}</p>}

      {(tapes.length > 0 || watching.length > 0) && (
        <p className="rb-watching">
          {tapes.length > 0 && <>Опрашиваем ленты: {tapes.join(', ')}. </>}
          {watchRule && <>Отбор: {watchRule}. </>}
          {watching.length > 0 && <>Сейчас в ленте {watching.length} инструментов.</>}
        </p>
      )}

      {!error && groups.length === 0 && !loading && (
        <p className="race-empty">
          {rows.length > 0
            ? 'Под фильтр не попал ни один робот — найдено ' + rows.length + ', показано 0.'
            : live
              ? 'Сейчас закономерностей не видно. Роботы появляются в активные часы торгов; лента анализируется за последние 20 минут.'
              : 'За неделю ничего не сохранено.'}
        </p>
      )}

      {groups.length > 0 && (
        <div className="jrn-list rb-list">
          {groups.map((g) => (
            <SymbolGroup
              key={g.symbol}
              symbol={g.symbol}
              rows={g.rows}
              nowMs={nowMs}
              live={live}
              threshold={threshold}
              armedIds={armedIds}
              onToggleAlarm={toggleAlarm}
              day={dayBySymbol.get(g.symbol)}
            />
          ))}
        </div>
      )}
    </div>
  );
}
