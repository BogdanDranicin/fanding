import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import type { DayVolume, Instrument, RobotSession, RobotsResponse, StreamStatus } from '../types/robots';
import { authFetch } from '../api/auth';

const fmtClock = new Intl.DateTimeFormat('ru-RU', {
  timeZone: 'Europe/Moscow', hour: '2-digit', minute: '2-digit', second: '2-digit',
});
const fmtDayClock = new Intl.DateTimeFormat('ru-RU', {
  timeZone: 'Europe/Moscow', day: '2-digit', month: '2-digit',
  hour: '2-digit', minute: '2-digit', second: '2-digit',
});
const fmtShort = new Intl.DateTimeFormat('ru-RU', {
  timeZone: 'Europe/Moscow', hour: '2-digit', minute: '2-digit',
});
const fmtLots = new Intl.NumberFormat('ru-RU');

// TICK_MS — с этим шагом пересчитываются обратные отсчёты и проверяется, не пора
// ли пикнуть. Мельче не нужно: предупреждения выдаются посекундно.
const TICK_MS = 200;
// ALARM_LEAD — за сколько секунд до удара начинают звучать предупреждения.
const ALARM_LEAD = 3;
// Порог силы. Ключ хранения с версией: сила теперь считается как доля потока
// бумаги, а не как вес одного принта, и старое сохранённое значение означало бы
// совсем другое — прежняя «единица» соответствует новым десяткам процентов.
const THRESHOLD_KEY = 'robots.strength-threshold.v3';
const DEFAULT_THRESHOLD = 10;

function clock(iso: string): string {
  try { return fmtClock.format(new Date(iso)); } catch { return iso; }
}
// shortClock — часы и минуты. Полное время с секундами уезжает в подсказку и в
// подробности: пара меток «12:13:57–17:40:29» в одной графе не помещается ни на
// каком разумном экране, а на вопрос «давно ли работает» отвечают и минуты.
function shortClock(iso: string): string {
  try { return fmtShort.format(new Date(iso)); } catch { return iso; }
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

// strengthLabel — сила робота: его поток в доле потока бумаги.
function strengthLabel(pct: number): string {
  if (!pct) return '—';
  if (pct >= 10) return `${pct.toFixed(0)}%`;
  if (pct >= 1) return `${pct.toFixed(1)}%`;
  return `${pct.toFixed(2)}%`;
}

// rateOf — поток робота в лотах за минуту. Живой срез считает его на сервере,
// у строки истории берём тот же расчёт: принт, разложенный на такт.
function rateOf(r: RobotSession): number {
  if (r.lots_per_min) return r.lots_per_min;
  return r.period_sec > 0 ? (printOf(r) * 60) / r.period_sec : 0;
}

// rateLabel — «114 л/мин». Медленный робот качает меньше лота в минуту, и там
// нужна десятая доля, иначе весь неликвид показывает ноль.
function rateLabel(lotsPerMin: number): string {
  if (!lotsPerMin) return '—';
  if (lotsPerMin >= 10) return `${fmtLots.format(Math.round(lotsPerMin))} л/мин`;
  return `${lotsPerMin.toFixed(1)} л/мин`;
}

// priceMove — куда уехала цена за жизнь серии, проценты. Это не прибыль робота,
// а движение бумаги за то время, что он работает: по нему видно, продавливает
// он цену или его наливают.
function priceMove(first: number, last: number): number {
  if (!first || !last) return NaN;
  return ((last - first) / first) * 100;
}

function moveLabel(pct: number): string {
  if (!Number.isFinite(pct)) return '—';
  const sign = pct > 0 ? '+' : '';
  return `${sign}${pct.toFixed(2)}%`;
}

// moveClass — движение цены красим по стороне: рост зелёным, падение красным.
function moveClass(pct: number): string {
  if (!Number.isFinite(pct) || Math.abs(pct) < 0.005) return ' rb-dim';
  return pct > 0 ? ' rb-long' : ' rb-short';
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

// SourceChip — состояние ленты одной строкой над таблицей.
//
// Раньше это был абзац на пять предложений вперемешку с методикой обнаружения.
// Читать его каждый раз незачем: в работе нужно ровно одно — чем сейчас
// приходят принты и насколько они свежие. Всё остальное уехало под «Как это
// работает» и открывается, когда действительно понадобится.
function SourceChip({ stream }: { stream: StreamStatus | null }) {
  if (!stream || !stream.enabled || !stream.connected) {
    return (
      <span className="rb-chip rb-chip-warn">
        <span className="rb-chip-dot" />
        лента MOEX ISS · задержка 15 мин
      </span>
    );
  }
  const missing = stream.missing ?? [];
  return (
    <span className="rb-chip rb-chip-ok" title={missing.length > 0
      ? `Мимо потока идут ${missing.join(', ')} — их нет в каталоге брокера`
      : 'Потоком покрыто всё наблюдение'}>
      <span className="rb-chip-dot" />
      поток брокера
      {stream.lag_ms > 0 && <> · {lagLabel(stream.lag_ms)}</>}
      {stream.symbols > 0 && <> · {stream.symbols} инструментов</>}
      {missing.length > 0 && <> · кроме {missing.join(', ')}</>}
    </span>
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

// COLUMNS — графы таблицы роботов. Один список на всё: по нему рисуется шапка,
// по нему же подписываются ячейки на узком экране. Раньше подпись лежала внутри
// каждой ячейки и повторялась столько раз, сколько на странице строк, — на полусотне
// роботов это сотня одинаковых слов «НАПРАВЛЕНИЕ», между которыми терялись числа.
//
// Графы одни и те же в обоих режимах, кроме одной. «До удара» в истории не
// бывает по существу: серия закончилась, бить нечему, и обратный отсчёт для
// робота, замолчавшего вчера, — не пустая графа, а неверная. На её месте стоит
// уверенность находки, которая в истории как раз есть.
//
// Отдельная графа «поток» появилась потому, что по паре «объём за раз» и «тайминг»
// сравнивать роботов глазами невозможно: 26 лотов раз в три секунды и 400 лотов
// раз в три минуты — это 520 и 133 лота в минуту, и на глаз сильнее выглядит
// второй. Поток сводит обе величины в одно число, а «ход цены» показывает, чем
// это давление кончилось для самой бумаги.
//
// А вот силу в истории раньше показать было нечем — часовой оборот бумаги жил
// только в памяти сбора и в базу не писался. Это чинилось в базе (миграция
// 0008), а не пряталось в разметке: теперь оба знаменателя сохраняются вместе
// со строкой, и сила у сохранённых роботов настоящая.
const COLUMNS_LIVE = [
  '', 'объём за раз', 'тайминг', 'поток', 'сила',
  'перв/последн', 'до удара', 'ход цены', 'объём серии', 'статус',
] as const;

const COLUMNS_HISTORY = [
  '', 'объём за раз', 'тайминг', 'поток', 'сила',
  'перв/последн', 'уверенность', 'ход цены', 'объём серии', 'когда',
] as const;

/** Класс сетки строки. Живая строка шире на колонку кнопки сигнала. */
function rowClass(live: boolean): string {
  return `rb-row${live ? ' rb-row-live' : ''}`;
}

// TableHead — шапка списка. На узком экране прячется: там графы раскладываются
// по месту и подписываются каждая своей (см. .rb-col::before в стилях).
function TableHead({ live }: { live: boolean }) {
  const cols = live ? COLUMNS_LIVE : COLUMNS_HISTORY;
  return (
    <div className={`${rowClass(live)} rb-head`} aria-hidden="true">
      {cols.map((c) => <span key={c} className="rb-head-cell">{c}</span>)}
      {live && <span />}
      <span />
    </div>
  );
}

// Cell — одна ячейка строки. Подпись графы уезжает в data-label: на широком
// экране её показывает шапка, на узком — псевдоэлемент над значением.
function Cell({ label, className, children }: {
  label: string;
  className?: string;
  children: React.ReactNode;
}) {
  return (
    <div className={`rb-col${className ? ' ' + className : ''}`} data-label={label}>
      {children}
    </div>
  );
}

// Dash — пустое значение. Прочерк должен читаться как «нечего показать», а не
// как ещё одно число: он приглушён и не тянет на себя взгляд.
const Dash = () => <span className="rb-none">—</span>;

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
  // Подробности строки рисуются только раскрытыми. Содержимое <details> лежит в
  // DOM независимо от того, открыт он или нет, и на истории за неделю это была
  // тысяча строк по полсотни узлов каждая: вкладка вставала на десятки секунд,
  // хотя пользователь не раскрыл ни одной.
  const [open, setOpen] = useState(false);
  const long = r.side === 'B';
  const once = printOf(r);
  const volume = volumeOf(r);
  const move = priceMove(r.price_first, r.price_last);
  const beatMs = live && r.active ? nextBeatAt(r, nowMs) : 0;
  const left = beatMs ? beatMs - nowMs : NaN;

  // Сильный робот подсвечивается в свою сторону: зелёный — набирает, красный — льёт.
  const strong = r.strength_pct >= threshold && r.strength_pct > 0;
  const classes = ['rb-card'];
  if (r.misses > 0) classes.push('rb-missed');
  if (strong) classes.push(long ? 'rb-strong-long' : 'rb-strong-short');

  const beatSoon = Number.isFinite(left) && left <= ALARM_LEAD * 1000;

  return (
    <details className={classes.join(' ')} onToggle={(e) => setOpen(e.currentTarget.open)}>
      {/* Сетка граф живёт на самой строке: у раскрывающегося контейнера внутри
          ещё и подробности, и они не должны попадать в колонки таблицы.
          Живая строка шире на колонку кнопки сигнала — в истории её нет. */}
      <summary className={`rb-summary ${rowClass(live)}`}>
        {/* Сторона — метка, а не слово: тикер теперь стоит в шапке бумаги, а
            «ЛОНГ»/«ШОРТ» занимали целую графу ради одного бита. Цвет и стрелка
            читаются с той же скоростью и оставляют место числам. */}
        <Cell label="сторона" className="rb-side-col">
          <span
            className={`rb-mark ${long ? 'rb-long' : 'rb-short'}`}
            title={long ? 'Агрессор покупает — робот набирает' : 'Агрессор продаёт — робот льёт'}
          >
            {long ? '▲' : '▼'}
          </span>
        </Cell>

        <Cell label="объём за раз">
          <span className="rb-val">{volumeLabel(once)}</span>
        </Cell>

        <Cell label="тайминг">
          <span className="rb-val rb-period">{period(r.period_sec)}</span>
        </Cell>

        <Cell label="поток">
          <span
            className="rb-val rb-dim"
            title={`${volumeLabel(once)} каждые ${period(r.period_sec)}`}
          >
            {rateLabel(rateOf(r))}
          </span>
        </Cell>

        <Cell label="перв/последн">
          <span className="rb-val rb-dim rb-span" title={`${dayClock(r.first_seen)} — ${dayClock(r.last_seen)}`}>
            {shortClock(r.first_seen)}–{shortClock(r.last_seen)}
          </span>
        </Cell>

        {live && (
          <Cell label="до удара">
            {r.active
              ? (
                <span
                  className={`rb-val rb-beat${beatSoon ? ' rb-beat-soon' : ''}`}
                  title={beatMs ? `следующий принт в ${clock(new Date(beatMs).toISOString())}` : ''}
                >
                  {countdown(left)}
                </span>
              )
              : <Dash />}
          </Cell>
        )}

        {!live && (
          <Cell label="уверенность">
            <span className="rb-val rb-dim" title={`ровность такта: ${r.confidence.toFixed(2)}`}>
              {confidenceLabel(r.confidence)}
            </span>
          </Cell>
        )}

        <Cell label="сила">
          {r.strength_pct > 0
            ? (
              <span
                className={`rb-val rb-strength${strong ? (long ? ' rb-strength-long' : ' rb-strength-short') : ''}`}
              title={r.hour_lots > 0
                ? `${rateLabel(rateOf(r))} робота против оборота ${volumeLabel(r.hour_lots)} по бумаге за ${r.hour_from}–${r.hour_to}`
                  : 'Оборота по бумаге для сравнения не сохранилось'}
              >
                {strengthLabel(r.strength_pct)}
              </span>
            )
            : <Dash />}
        </Cell>

        <Cell label="ход цены">
          <span
            className={`rb-val${moveClass(move)}`}
            title={`${price(r.price_first)} → ${price(r.price_last)} за жизнь серии`}
          >
            {moveLabel(move)}
          </span>
        </Cell>

        <Cell label="объём серии">
          <span className="rb-val rb-dim" title={`принтов: ${fmtLots.format(r.prints)}`}>
            {volumeLabel(volume)}
          </span>
        </Cell>

        <Cell label={live ? 'статус' : 'когда'} className="rb-status-col">
          {!live
            ? <span className="rb-status rb-stopped">{dayClock(r.first_seen)} – {clock(r.last_seen)}</span>
            : r.misses > 0
              ? <span className="rb-status rb-warn">пропустил такт</span>
              : r.active
                ? <span className="rb-status rb-active">{r.provisional ? 'предварительно' : 'работает'}</span>
                : <span className="rb-status rb-stopped">замолчал в {clock(r.last_seen)}</span>}
        </Cell>

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

      {open && <div className="jrn-details">
        <div className="jrn-detail-group">
          <div className="jrn-detail-title">Серия</div>
          <Detail label="Первый принт" value={dayClock(r.first_seen)} />
          <Detail label="Последний принт" value={dayClock(r.last_seen)} />
          <Detail label="Работает" value={duration(r)} />
          <Detail
            label="Тактов сыграно"
            value={r.hits > 0 ? `${r.hits} из ${r.beats}` : String(r.beats)}
          />
          <Detail label="Пропущено тактов подряд" value={String(r.misses)} />
        </div>

        <div className="jrn-detail-group">
          <div className="jrn-detail-title">Лотовка и объём</div>
          <Detail label="Объём за раз" value={volumeLabel(once)} />
          <Detail label="Границы размера" value={lots(r)} />
          {r.print_trades > 0 && (
            <Detail
              label="Сделок биржи в одном принте"
              value={r.print_trades <= 1
                ? '1 — приказ забирает одну заявку'
                : `${r.print_trades.toFixed(0)} — приказ сметает несколько заявок`}
            />
          )}
          <Detail label="Поток робота" value={rateLabel(rateOf(r))} />
          <Detail label="Суммарный объём серии" value={volumeLabel(volume)} />
          {r.hour_lots > 0 && (
            <Detail
              label={`Оборот бумаги за ${r.hour_from}–${r.hour_to}`}
              value={volumeLabel(r.hour_lots)}
            />
          )}
          {r.hour_since && (
            <Detail label="Оборот для сравнения считается с" value={`${r.hour_since} МСК`} />
          )}
          {r.day_side_lots > 0 && (
            <Detail
              label={long ? 'Куплено за день' : 'Продано за день'}
              value={volumeLabel(r.day_side_lots)}
            />
          )}
          <Detail label="Сила: поток робота к потоку бумаги" value={strengthLabel(r.strength_pct)} />
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
      </div>}
    </details>
  );
}

// PaperHead — полоса бумаги над её роботами: тикер, имя, справочные величины и
// то, что роботы этой бумаги делают вместе.
//
// Раньше на этом месте была строка-итог по тем же графам, что и у роботов ниже.
// Читалась она плохо: те же колонки, но значения в них означали разное —
// «самый крупный принт», «самый частый такт», — и глаз всё время спотыкался,
// пытаясь сравнить итог со строками под ним. Полоса не притворяется строкой
// таблицы: она подписывает бумагу и сводит роботов одной фразой.
function PaperHead({ symbol, rows, inst, live, threshold }: {
  symbol: string;
  rows: RobotSession[];
  inst?: Instrument;
  live: boolean;
  threshold: number;
}) {
  const longs = rows.filter((r) => r.side === 'B');
  const shorts = rows.filter((r) => r.side === 'S');
  // Сила стороны складывается: это доли одного и того же потока бумаги.
  const sumPct = (xs: RobotSession[]) => xs.reduce((acc, r) => acc + r.strength_pct, 0);
  const longPct = sumPct(longs);
  const shortPct = sumPct(shorts);
  const leadLong = longPct >= shortPct;
  const leadPct = Math.max(longPct, shortPct);
  const strong = leadPct >= threshold && leadPct > 0;

  const active = rows.filter((r) => r.active).length;
  const rate = rows.reduce((acc, r) => acc + (r.active || !live ? rateOf(r) : 0), 0);

  const spec: string[] = [];
  if (inst?.min_step) {
    spec.push(`шаг ${inst.min_step.toLocaleString('ru-RU', { maximumFractionDigits: 6 })}`);
  }
  if (inst?.lot_size) {
    spec.push(inst.lot_size === 1 ? 'лот — 1 бумага' : `${fmtLots.format(inst.lot_size)} бумаг в лоте`);
  }

  return (
    <div className="rb-paper-head">
      <span className={`rb-ticker${strong ? (leadLong ? ' rb-ticker-long' : ' rb-ticker-short') : ''}`}>
        {symbol}
      </span>

      <span className="rb-paper-about">
        {inst?.name && <span className="rb-paper-name">{inst.name}</span>}
        {spec.length > 0 && <span className="rb-paper-spec">{spec.join(' · ')}</span>}
      </span>

      <span className="rb-paper-sums">
        <span className="rb-paper-sum" title="Роботов на бумаге, из них печатают сейчас">
          {live ? `${active} из ${rows.length}` : `${rows.length}`}
          <span className="rb-paper-sum-label">роботов</span>
        </span>
        {rate > 0 && (
          <span className="rb-paper-sum" title="Сколько лотов в минуту качают роботы бумаги вместе">
            {rateLabel(rate)}
          </span>
        )}
        {leadPct > 0 && (
          <span
            className={`rb-paper-sum ${leadLong ? 'rb-long' : 'rb-short'}`}
            title={`Роботы ${leadLong ? 'лонга' : 'шорта'} делают ${strengthLabel(leadPct)} потока бумаги`}
          >
            {leadLong ? '▲' : '▼'} {strengthLabel(leadPct)}
            <span className="rb-paper-sum-label">потока</span>
          </span>
        )}
      </span>
    </div>
  );
}

// PaperGroup — бумага целиком: полоса с её описанием и роботы под ней.
interface GroupProps extends Omit<RowProps, 'r' | 'armed' | 'nested' | 'inst'> {
  symbol: string;
  rows: RobotSession[];
  armedIds: Set<number>;
  inst?: Instrument;
}

function PaperGroup({ symbol, rows, nowMs, live, threshold, armedIds, onToggleAlarm, day, inst }: GroupProps) {
  const [doneOpen, setDoneOpen] = useState(false);
  // В живом срезе замолчавший робот остаётся в ответе ещё сутки — это и есть
  // «завершённые». В истории замолчали все, и делить там нечего.
  const working = live ? rows.filter((r) => r.active) : rows;
  const finished = live ? rows.filter((r) => !r.active) : [];

  const row = (r: RobotSession) => (
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
  );

  return (
    <section className="rb-paper">
      <PaperHead symbol={symbol} rows={rows} inst={inst} live={live} threshold={threshold} />
      {working.map(row)}

      {/* Замолчавшие роботы — отдельным свёрнутым списком, а не вперемешку с
          работающими: живой список читают, чтобы понять, что происходит прямо
          сейчас, а вчерашний робот выглядит в нём ровно как сегодняшний.
          Выбрасывать их при этом нельзя — по ним видно, кто на бумаге работал,
          с каким тактом и вернулся ли он. */}
      {finished.length > 0 && (
        <details className="rb-finished" onToggle={(e) => setDoneOpen(e.currentTarget.open)}>
          <summary className="rb-finished-summary">
            <span className="jrn-chevron" aria-hidden="true">▸</span>
            Завершённые роботы: {finished.length}
          </summary>
          {doneOpen && finished.map(row)}
        </details>
      )}
    </section>
  );
}

type Tab = 'live' | 'history';

export function RobotsPage() {
  const [tab, setTab] = useState<Tab>('live');
  const [rows, setRows] = useState<RobotSession[]>([]);
  const [watching, setWatching] = useState<string[]>([]);
  const [watchRule, setWatchRule] = useState('');
  const [days, setDays] = useState<DayVolume[]>([]);
  const [instruments, setInstruments] = useState<Instrument[]>([]);
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
        setWatchRule(data.watch_rule ?? '');
        setDays(data.day_volumes ?? []);
        setInstruments(data.instruments ?? []);
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

  const instBySymbol = useMemo(() => {
    const m = new Map<string, Instrument>();
    instruments.forEach((i) => m.set(i.symbol, i));
    return m;
  }, [instruments]);

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

      <div className="rb-status-line">
        {/* Чип описывает ЖИВУЮ ленту. В истории за неделю он не значит ничего:
            там показаны находки прошедших дней, и «задержка 15 минут» рядом с
            ними — просто неверная подпись. */}
        {live && <SourceChip stream={stream} />}
        {/* Робот — это повтор: один и тот же размер через один и тот же промежуток.
            Больше про методику на странице знать незачем; всё остальное — пороги,
            ленты, правила отбора — это внутренности сервиса, а не то, с чем
            работает человек. Ушло в подсказку под вопросительным знаком. */}
        <button
          type="button"
          className="rb-hint"
          title={'Робот — это повтор: сделка одного размера (±1 лот) через ровный промежуток времени. '
            + 'На валюте серия засчитывается с третьего принта, на остальном — с шестого. '
            + 'Пропустил такт — строка желтеет, пропустил второй подряд — уходит в историю.'
            + (watching.length > 0 ? `

В наблюдении ${watching.length} инструментов: ${watchRule}` : '')}
          aria-label="Как работает поиск роботов"
        >
          ?
        </button>
      </div>

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

        <label className="rb-filter" title="Какую долю потока бумаги должен делать робот, чтобы считаться сильным. Поток робота — его принт, разложенный на такт; поток бумаги — её часовой оборот, приведённый к минуте">
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
          % потока бумаги
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
          <TableHead live={live} />
          {groups.map((g) => (
            <PaperGroup
              key={g.symbol}
              symbol={g.symbol}
              rows={g.rows}
              nowMs={nowMs}
              live={live}
              threshold={threshold}
              armedIds={armedIds}
              onToggleAlarm={toggleAlarm}
              day={dayBySymbol.get(g.symbol)}
              inst={instBySymbol.get(g.symbol)}
            />
          ))}
        </div>
      )}
    </div>
  );
}
