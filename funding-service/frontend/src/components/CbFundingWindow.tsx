import { useCallback, useEffect, useState } from 'react';
import { createPortal } from 'react-dom';
import { useFundingStore } from '../store/fundingStore';
import { useIsMobile } from '../hooks/useIsMobile';
import type { InstrumentFunding } from '../types/funding';

// Document Picture-in-Picture — единственный способ получить в браузере окно
// «поверх всех»: обычный window.open такого не умеет, поверх остальных программ
// его может закрепить только сама операционная система. Апишка новая, в типах
// браузера её пока нет, поэтому описываем то немногое, чем пользуемся.
interface PiPWindowOptions {
  width?: number;
  height?: number;
}

interface DocumentPictureInPicture {
  requestWindow(options?: PiPWindowOptions): Promise<Window>;
  readonly window: Window | null;
}

declare global {
  interface Window {
    documentPictureInPicture?: DocumentPictureInPicture;
  }
}

const WIDTH = 300;
const HEIGHT = 168;

// Собственные стили: у отдельного окна нет ни таблицы стилей приложения, ни
// смысла её тащить — виджет из трёх строк описывается короче, чем копирование
// чужих правил. Цвета взяты те же, что в index.css.
const CSS = `
:root { color-scheme: dark; }
* { box-sizing: border-box; margin: 0; padding: 0; }
body {
  font-family: system-ui, 'Segoe UI', Roboto, sans-serif;
  background: #0a0b10;
  color: #f5f7fc;
  -webkit-font-smoothing: antialiased;
  user-select: none;
  overflow: hidden;
}
.cbw {
  display: flex;
  flex-direction: column;
  height: 100vh;
  padding: 10px 14px 12px;
  gap: 2px;
}
.cbw-head {
  display: flex;
  align-items: baseline;
  justify-content: space-between;
  gap: 8px;
  font-size: 10px;
  letter-spacing: 0.1em;
  text-transform: uppercase;
  color: #6b7280;
  border-bottom: 1px solid #2b303d;
  padding-bottom: 6px;
  margin-bottom: 4px;
}
.cbw-time { font-variant-numeric: tabular-nums; letter-spacing: 0.04em; }
.cbw-row {
  display: flex;
  align-items: baseline;
  gap: 10px;
  flex: 1;
  min-height: 0;
}
.cbw-sym {
  width: 3.2em;
  font-size: clamp(11px, 3.6vw, 15px);
  color: #9aa4bb;
  letter-spacing: 0.04em;
}
.cbw-val {
  font-size: clamp(17px, 7vw, 34px);
  font-weight: 700;
  font-variant-numeric: tabular-nums;
  line-height: 1.1;
}
.cbw-up { color: #2ee66e; }
.cbw-down { color: #ff5464; }
.cbw-pct {
  margin-left: auto;
  font-size: clamp(10px, 3.2vw, 13px);
  font-variant-numeric: tabular-nums;
  color: #9aa4bb;
}
.cbw-none { color: #6b7280; }
.cbw-wait {
  font-size: 11px;
  color: #6b7280;
  text-align: center;
  padding-top: 4px;
}
`;

const fmt6 = new Intl.NumberFormat('ru-RU', { minimumFractionDigits: 6, maximumFractionDigits: 6 });

// Строка CB funding существует только для доллара и евро: у юаня своего курса
// ЦБ в этой строке нет — на странице он тоже стоит прочерком.
const SYMS = [
  { key: 'USDRUBF', label: 'USD' },
  { key: 'EURRUBF', label: 'EUR' },
] as const;

function Row({ label, inst }: { label: string; inst: InstrumentFunding | undefined }) {
  const value = inst?.cb_funding;
  const ref = inst?.official_rate;
  // Пороги те же, что в таблице на странице: ±0.1 — уже заметный фандинг.
  const cls = value == null ? '' : value >= 0.1 ? ' cbw-up' : value <= -0.1 ? ' cbw-down' : '';
  const pct = value != null && ref != null && ref > 0 ? (value / ref) * 100 : null;

  return (
    <div className="cbw-row">
      <span className="cbw-sym">{label}</span>
      <span className={`cbw-val${cls}`}>
        {value != null ? fmt6.format(value) : <span className="cbw-none">—</span>}
      </span>
      {pct != null && (
        <span className="cbw-pct">{pct >= 0 ? '+' : ''}{pct.toFixed(3)}%</span>
      )}
    </div>
  );
}

/** Содержимое отдельного окна. Живёт на общем состоянии — обновляется само. */
function Widget() {
  const current = useFundingStore((s) => s.current);
  const status = useFundingStore((s) => s.wsStatus);

  const time = current
    ? new Date(current.ts).toLocaleTimeString('ru-RU', { timeZone: 'Europe/Moscow' })
    : '—';

  return (
    <div className="cbw">
      <div className="cbw-head">
        <span>CB funding</span>
        <span className="cbw-time">{time} МСК</span>
      </div>
      {SYMS.map(({ key, label }) => (
        <Row key={key} label={label} inst={current?.[key]} />
      ))}
      {!current && (
        <div className="cbw-wait">
          {status === 'connected' ? 'ждём снимок' : 'нет связи с сервером'}
        </div>
      )}
    </div>
  );
}

/**
 * Кнопка «CB funding отдельным окном» и само это окно.
 *
 * Окно открывается через Document Picture-in-Picture — оно висит поверх всех
 * программ, пока пользователь не закроет его сам. Там, где эта апишка не
 * поддержана, остаётся обычное всплывающее окно: содержимое то же, но поверх
 * остальных окон его придётся закреплять средствами системы.
 */
export function CbFundingWindow() {
  const isMobile = useIsMobile();
  const [win, setWin] = useState<Window | null>(null);
  const [error, setError] = useState('');

  // Своё окно живёт, пока живёт страница: осиротевшее всплывающее окно после
  // ухода со страницы не обновлялось бы и показывало давние числа.
  useEffect(() => {
    if (!win) return;
    const close = () => win.close();
    window.addEventListener('pagehide', close);
    return () => window.removeEventListener('pagehide', close);
  }, [win]);

  const toggle = useCallback(async () => {
    if (win) {
      win.close();
      return;
    }
    setError('');

    let w: Window | null = null;
    const pip = window.documentPictureInPicture;
    if (pip) {
      try {
        w = await pip.requestWindow({ width: WIDTH, height: HEIGHT });
      } catch {
        // Запрос отклонён (например, окно уже открыто в другой вкладке) —
        // пробуем обычное окно, оно лучше, чем ничего.
        w = null;
      }
    }
    if (!w) {
      w = window.open('', 'cb-funding', `popup=yes,width=${WIDTH},height=${HEIGHT}`);
    }
    if (!w) {
      setError('окно заблокировано браузером');
      return;
    }

    w.document.title = 'CB funding';
    const style = w.document.createElement('style');
    style.textContent = CSS;
    w.document.head.appendChild(style);
    // Закрыть окно может и сам пользователь — тогда кнопка должна вернуться
    // в исходное состояние, а портал перестать рисовать в мёртвый документ.
    w.addEventListener('pagehide', () => setWin(null), { once: true });
    setWin(w);
  }, [win]);

  // На телефоне отдельного окна не бывает: Document PiP там не поддержан, а
  // всплывающее окно мобильный браузер открывает новой вкладкой — и накрывает
  // ею ту самую страницу, поверх которой виджет должен был висеть.
  if (isMobile) return null;

  return (
    <>
      <button
        type="button"
        className={`cbw-btn${win ? ' cbw-btn-on' : ''}`}
        onClick={() => void toggle()}
        title={
          win
            ? 'Закрыть окно CB funding'
            : 'Открыть CB funding отдельным окном поверх остальных'
        }
      >
        <span className="cbw-btn-icon" aria-hidden="true">⧉</span>
        CB funding
      </button>
      {error && <span className="cbw-err">{error}</span>}
      {win && createPortal(<Widget />, win.document.body)}
    </>
  );
}
