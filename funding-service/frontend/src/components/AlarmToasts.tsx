import { useEffect } from 'react';
import { useAlarmStore, type FiredAlarm } from '../store/alarmStore';

// SHOW_MS — сколько уведомление висит на экране. Сигнал прежде всего звуковой,
// плашка лишь отвечает на вопрос «что это сейчас пикнуло».
const SHOW_MS = 8000;

function Toast({ f }: { f: FiredAlarm }) {
  const dismiss = useAlarmStore((s) => s.dismissFired);

  useEffect(() => {
    const id = setTimeout(() => dismiss(f.key), SHOW_MS);
    return () => clearTimeout(id);
  }, [dismiss, f.key]);

  return (
    <div className="alm-toast" role="status">
      <span className="alm-toast-mark">{f.mark}</span>
      <span className="alm-toast-body">
        <span className="alm-toast-title">{f.title}</span>
        {f.ahead && <span className="alm-toast-sub">предупреждение до отметки</span>}
      </span>
      <button
        type="button"
        className="alm-toast-close"
        aria-label="Скрыть"
        onClick={() => dismiss(f.key)}
      >
        ✕
      </button>
    </div>
  );
}

/** Плашки сработавших сигналов времени. Рисуются поверх любой страницы. */
export function AlarmToasts() {
  const fired = useAlarmStore((s) => s.fired);
  if (fired.length === 0) return null;
  return (
    <div className="alm-toasts">
      {fired.map((f) => <Toast key={f.key} f={f} />)}
    </div>
  );
}
