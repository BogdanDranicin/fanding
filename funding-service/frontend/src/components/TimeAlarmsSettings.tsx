import { useEffect, useState } from 'react';
import { useAlarmStore } from '../store/alarmStore';
import {
  ALARM_PRESETS,
  TONES,
  alarmTitle,
  clockOf,
  describeAlarm,
  makeAlarm,
  minutesOf,
  nextFireAt,
  playAlarmTone,
  type AlarmTone,
  type TimeAlarm,
} from '../lib/timeAlarms';

// countdown — «через 4 мин», «через 12 с»: сколько осталось до следующего сигнала.
function countdown(ms: number): string {
  const sec = Math.round(ms / 1000);
  if (sec < 60) return `через ${sec} с`;
  const min = Math.round(sec / 60);
  if (min < 60) return `через ${min} мин`;
  const h = Math.floor(min / 60);
  return `через ${h} ч ${String(min - h * 60).padStart(2, '0')} мин`;
}

/** Строка списка: расписание, когда сработает в следующий раз, и управление. */
function AlarmItem({ a, now }: { a: TimeAlarm; now: number }) {
  const updateAlarm = useAlarmStore((s) => s.updateAlarm);
  const removeAlarm = useAlarmStore((s) => s.removeAlarm);
  const volume = useAlarmStore((s) => s.volume);
  const [editing, setEditing] = useState(false);

  const at = a.enabled ? nextFireAt(a, now) : 0;

  if (editing) {
    return (
      <AlarmEditor
        draft={a}
        onCancel={() => setEditing(false)}
        onSave={(next) => {
          updateAlarm(a.id, next);
          setEditing(false);
        }}
      />
    );
  }

  return (
    <div className={`alm-item${a.enabled ? '' : ' alm-item-off'}`}>
      <input
        type="checkbox"
        checked={a.enabled}
        aria-label="Включить сигнал"
        onChange={(e) => updateAlarm(a.id, { enabled: e.target.checked })}
      />

      <div className="alm-item-main">
        <span className="alm-item-title">{alarmTitle(a)}</span>
        <span className="alm-item-sub">
          {a.label.trim() ? `${describeAlarm(a)} · ` : ''}
          {a.enabled && at ? countdown(at - now) : 'выключен'}
        </span>
      </div>

      <div className="alm-item-actions">
        <button
          type="button"
          className="alm-icon"
          title="Проверить звук"
          onClick={() => playAlarmTone(a.tone, volume)}
        >
          ▶
        </button>
        <button
          type="button"
          className="alm-icon"
          title="Изменить"
          onClick={() => setEditing(true)}
        >
          ✎
        </button>
        <button
          type="button"
          className="alm-icon alm-icon-danger"
          title="Удалить"
          onClick={() => removeAlarm(a.id)}
        >
          ✕
        </button>
      </div>
    </div>
  );
}

/** Форма сигнала — одна и та же для нового и для правки существующего. */
function AlarmEditor({ draft, onSave, onCancel }: {
  draft: TimeAlarm;
  onSave: (a: TimeAlarm) => void;
  onCancel: () => void;
}) {
  const volume = useAlarmStore((s) => s.volume);
  const [a, setA] = useState<TimeAlarm>(draft);
  // Шаг держим строкой: очищенное поле — это ещё не «каждые 0 минут», а момент,
  // когда пользователь стирает старое число, чтобы набрать новое.
  const [every, setEvery] = useState(String(draft.everyMin));
  const [lead, setLead] = useState(String(draft.leadSec));

  const patch = (p: Partial<TimeAlarm>) => setA((prev) => ({ ...prev, ...p }));

  const save = () => {
    const everyMin = Math.min(1440, Math.max(1, Math.round(Number(every)) || 60));
    const leadSec = Math.min(600, Math.max(0, Math.round(Number(lead)) || 0));
    onSave({ ...a, everyMin, leadSec });
  };

  return (
    <div className="alm-editor">
      <div className="alm-kinds">
        <button
          type="button"
          className={`alm-kind${a.kind === 'interval' ? ' alm-kind-on' : ''}`}
          onClick={() => patch({ kind: 'interval' })}
        >
          Через равные промежутки
        </button>
        <button
          type="button"
          className={`alm-kind${a.kind === 'at' ? ' alm-kind-on' : ''}`}
          onClick={() => patch({ kind: 'at' })}
        >
          В заданное время
        </button>
      </div>

      {a.kind === 'interval' ? (
        <>
          <label className="alm-field">
            <span className="alm-field-label">Каждые</span>
            <input
              type="number"
              className="alm-num"
              min={1}
              max={1440}
              inputMode="numeric"
              value={every}
              onChange={(e) => setEvery(e.target.value)}
            />
            <span className="alm-field-unit">минут</span>
          </label>

          <label className="alm-field">
            <span className="alm-field-label">Начиная с</span>
            <input
              type="time"
              className="alm-time"
              value={clockOf(a.startMin)}
              onChange={(e) => {
                const m = minutesOf(e.target.value);
                if (m !== null) patch({ startMin: m });
              }}
            />
            <span className="alm-field-unit">МСК — первая отметка суток</span>
          </label>
        </>
      ) : (
        <label className="alm-field">
          <span className="alm-field-label">Время</span>
          <input
            type="time"
            className="alm-time"
            value={clockOf(a.atMin)}
            onChange={(e) => {
              const m = minutesOf(e.target.value);
              if (m !== null) patch({ atMin: m });
            }}
          />
          <span className="alm-field-unit">МСК, каждый день</span>
        </label>
      )}

      <label className="alm-field">
        <span className="alm-field-label">Сигнал за</span>
        <input
          type="number"
          className="alm-num"
          min={0}
          max={600}
          inputMode="numeric"
          value={lead}
          onChange={(e) => setLead(e.target.value)}
        />
        <span className="alm-field-unit">секунд до отметки (0 — точно в момент)</span>
      </label>

      <label className="alm-field">
        <span className="alm-field-label">Звук</span>
        <select
          className="alm-select"
          value={a.tone}
          onChange={(e) => patch({ tone: e.target.value as AlarmTone })}
        >
          {TONES.map((t) => <option key={t.value} value={t.value}>{t.label}</option>)}
        </select>
        <button
          type="button"
          className="alm-icon"
          title="Проверить звук"
          onClick={() => playAlarmTone(a.tone, volume)}
        >
          ▶
        </button>
      </label>

      <label className="alm-field">
        <span className="alm-field-label">Название</span>
        <input
          type="text"
          className="alm-text"
          maxLength={60}
          placeholder="необязательно"
          value={a.label}
          onChange={(e) => patch({ label: e.target.value })}
        />
      </label>

      <label className="alm-check">
        <input
          type="checkbox"
          checked={a.weekdaysOnly}
          onChange={(e) => patch({ weekdaysOnly: e.target.checked })}
        />
        <span>Только по будням</span>
      </label>

      <div className="alm-editor-actions">
        <button type="button" className="btn-plain alm-primary" onClick={save}>Сохранить</button>
        <button type="button" className="btn-plain" onClick={onCancel}>Отмена</button>
      </div>
    </div>
  );
}

/** Раздел настроек «Сигналы по времени». */
export function TimeAlarmsSettings() {
  const alarms = useAlarmStore((s) => s.alarms);
  const enabled = useAlarmStore((s) => s.enabled);
  const volume = useAlarmStore((s) => s.volume);
  const setEnabled = useAlarmStore((s) => s.setEnabled);
  const setVolume = useAlarmStore((s) => s.setVolume);
  const addAlarm = useAlarmStore((s) => s.addAlarm);
  const [adding, setAdding] = useState<TimeAlarm | null>(null);

  // Часы для обратного отсчёта в списке. Секунда — ровно то, что показано.
  const [now, setNow] = useState(() => Date.now());
  useEffect(() => {
    const id = setInterval(() => setNow(Date.now()), 1000);
    return () => clearInterval(id);
  }, []);

  return (
    <div className="settings-section">
      <h3>Сигналы по времени</h3>
      <p>
        Звук на переход часа, получаса, любой другой промежуток или на точное
        время по Москве. Считаются, пока открыта вкладка с сайтом; как и у
        сигнала фандинга, странице нужен хотя бы один клик — без него браузер
        не даёт играть звук.
      </p>

      <label className="settings-row">
        <input
          type="checkbox"
          checked={enabled}
          onChange={(e) => setEnabled(e.target.checked)}
        />
        <span>Включить сигналы по времени</span>
      </label>

      <label className="settings-row">
        <span className="settings-row-label">Громкость</span>
        <input
          type="range"
          min={0.05}
          max={1}
          step={0.05}
          value={volume}
          disabled={!enabled}
          onChange={(e) => setVolume(Number(e.target.value))}
        />
      </label>

      {alarms.length > 0 && (
        <div className="alm-list">
          {alarms.map((a) => <AlarmItem key={a.id} a={a} now={now} />)}
        </div>
      )}

      {adding ? (
        <AlarmEditor
          draft={adding}
          onCancel={() => setAdding(null)}
          onSave={(a) => {
            addAlarm(a);
            setAdding(null);
          }}
        />
      ) : (
        <div className="alm-presets">
          {ALARM_PRESETS.map((p) => (
            <button
              key={p.label}
              type="button"
              className="btn-plain"
              onClick={() => addAlarm(makeAlarm(p.patch))}
            >
              + {p.label}
            </button>
          ))}
          <button
            type="button"
            className="btn-plain"
            onClick={() => setAdding(makeAlarm())}
          >
            + Свой сигнал…
          </button>
        </div>
      )}
    </div>
  );
}
