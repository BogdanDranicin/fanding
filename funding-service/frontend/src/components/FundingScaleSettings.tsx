import { useState } from 'react';
import { useScaleStore } from '../store/scaleStore';
import { DEFAULT_SCALE, LEVEL_MARK, type FundingScale } from '../lib/fundingScale';

// Поля порогов держим черновиком-строкой: набирая «0.14», пользователь проходит
// через пустую строку и через «0.», а красить по ним нечего — числа там ещё нет.
// Раньше такое поле просто отказывалось стираться, и значение нельзя было перебить.
function ThresholdField({ label, value, onChange, onBlur }: {
  label: string;
  value: string;
  onChange: (v: string) => void;
  onBlur: () => void;
}) {
  return (
    <label className="settings-row">
      <span className="settings-row-label">{label}</span>
      <input
        className="rb-threshold"
        type="number"
        min={0.001}
        max={100}
        step={0.01}
        inputMode="decimal"
        value={value}
        onChange={(e) => onChange(e.target.value)}
        onBlur={onBlur}
      />
      <span style={{ color: 'var(--text-muted)', fontSize: 13 }}>% от курса</span>
    </label>
  );
}

function ladder(scale: FundingScale): { mark: string; text: string }[] {
  const strong = scale.strongPct;
  const weak = scale.weakPct;
  return [
    { mark: LEVEL_MARK['strong-up'], text: `от +${strong}%` },
    { mark: LEVEL_MARK.up, text: `от +${weak}%` },
    { mark: LEVEL_MARK.flat, text: `в пределах ±${weak}%` },
    { mark: LEVEL_MARK.down, text: `от −${weak}%` },
    { mark: LEVEL_MARK['strong-down'], text: `от −${strong}%` },
  ];
}

/**
 * Пороги подсветки фандинга. Та же лестница, по которой бот выбирает индикатор
 * в телеграме, только числа задаёт пользователь: у каждого свой уровень, с
 * которого ставка перестаёт быть шумом.
 */
export function FundingScaleSettings() {
  const scale = useScaleStore((s) => s.scale);
  const setScale = useScaleStore((s) => s.setScale);
  const resetScale = useScaleStore((s) => s.resetScale);

  const [strongDraft, setStrongDraft] = useState(() => String(scale.strongPct));
  const [weakDraft, setWeakDraft] = useState(() => String(scale.weakPct));

  const commit = (patch: Partial<FundingScale>) => {
    setScale(patch);
  };

  const syncDrafts = () => {
    const now = useScaleStore.getState().scale;
    setStrongDraft(String(now.strongPct));
    setWeakDraft(String(now.weakPct));
  };

  const isDefault = scale.strongPct === DEFAULT_SCALE.strongPct
    && scale.weakPct === DEFAULT_SCALE.weakPct;

  return (
    <div className="settings-section">
      <h3>Подсветка фандинга</h3>
      <p>
        Фандинг красится не по самой ставке, а по её доле от курса: 0.1 по доллару
        и по евро — разные деньги, а процент сравним. Ступени те же, что у
        индикаторов в телеграм-боте; пороги — ваши. Ставка, не дотянувшая до
        меньшего порога, остаётся серой.
      </p>

      <ThresholdField
        label="Зелёный и красный от"
        value={strongDraft}
        onChange={(v) => {
          setStrongDraft(v);
          const n = Number(v);
          if (Number.isFinite(n) && n > 0) commit({ strongPct: n });
        }}
        onBlur={syncDrafts}
      />

      <ThresholdField
        label="Жёлтый и оранжевый от"
        value={weakDraft}
        onChange={(v) => {
          setWeakDraft(v);
          const n = Number(v);
          if (Number.isFinite(n) && n > 0) commit({ weakPct: n });
        }}
        onBlur={syncDrafts}
      />

      <div className="fnd-ladder">
        {ladder(scale).map((step) => (
          <span key={step.text} className="fnd-ladder-step">
            <span aria-hidden="true">{step.mark}</span> {step.text}
          </span>
        ))}
      </div>

      <div>
        <button
          className="btn-plain"
          disabled={isDefault}
          onClick={() => { resetScale(); syncDrafts(); }}
        >
          Вернуть {DEFAULT_SCALE.strongPct} / {DEFAULT_SCALE.weakPct}
        </button>
      </div>
    </div>
  );
}
