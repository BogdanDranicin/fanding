// Пороги подсветки фандинга — те же, по которым бот выбирает индикатор в
// телеграме (см. indicatorEmoji в internal/telegram/dispatcher.go). Считаются
// от ПРОЦЕНТА фандинга к курсу, а не от самой ставки: ставка в 0.1 по доллару
// и по евро — разные деньги, а процент сравним между инструментами.
//
// Значения держит пользователь: у каждого свой порог, с которого фандинг для
// него «интересный», и зашивать сюда чужой незачем.

const SCALE_KEY = 'funding_scale.v1';

export interface FundingScale {
  /** Процент, с которого фандинг считается сильным: зелёный вверх, красный вниз. */
  strongPct: number;
  /** Процент, с которого фандинг заметен: жёлтый вверх, оранжевый вниз. */
  weakPct: number;
}

export const DEFAULT_SCALE: FundingScale = { strongPct: 0.14, weakPct: 0.05 };

export type FundingLevel = 'strong-up' | 'up' | 'flat' | 'down' | 'strong-down' | 'none';

/** Индикатор уровня — ровно тот же набор, что уходит в телеграм. */
export const LEVEL_MARK: Record<FundingLevel, string> = {
  'strong-up': '🟢',
  up: '🟡',
  flat: '⚪️',
  down: '🟠',
  'strong-down': '🔴',
  none: '',
};

export const LEVEL_TITLE: Record<FundingLevel, string> = {
  'strong-up': 'сильный положительный',
  up: 'заметный положительный',
  flat: 'в пределах порога',
  down: 'заметный отрицательный',
  'strong-down': 'сильный отрицательный',
  none: 'не с чем сравнить: нет курса',
};

function clampPct(v: unknown, def: number): number {
  const n = Number(v);
  if (!Number.isFinite(n) || n <= 0) return def;
  return Math.min(100, n);
}

/** Приводит пороги к рабочему виду: оба положительны и слабый строго ниже сильного. */
export function normalizeScale(raw: Partial<FundingScale> | null | undefined): FundingScale {
  const strong = clampPct(raw?.strongPct, DEFAULT_SCALE.strongPct);
  const weak = clampPct(raw?.weakPct, DEFAULT_SCALE.weakPct);
  return weak < strong ? { strongPct: strong, weakPct: weak } : { strongPct: strong, weakPct: strong };
}

export function loadFundingScale(): FundingScale {
  try {
    const raw = localStorage.getItem(SCALE_KEY);
    if (!raw) return DEFAULT_SCALE;
    return normalizeScale(JSON.parse(raw) as Partial<FundingScale>);
  } catch {
    return DEFAULT_SCALE;
  }
}

export function saveFundingScale(scale: FundingScale): void {
  try {
    localStorage.setItem(SCALE_KEY, JSON.stringify(scale));
  } catch {
    // Хранилище переполнено — пороги доживут до конца сессии.
  }
}

/** Процент фандинга к курсу. null — считать не от чего. */
export function fundingPct(
  value: number | null | undefined,
  reference: number | null | undefined,
): number | null {
  if (value == null || reference == null || reference <= 0) return null;
  return (value / reference) * 100;
}

/**
 * Уровень фандинга по порогам. Без курса уровня нет вовсе — не «серый, потому
 * что мало», а «сравнить не с чем», и выглядеть это должно по-разному.
 */
export function fundingLevel(
  value: number | null | undefined,
  reference: number | null | undefined,
  scale: FundingScale,
): FundingLevel {
  const pct = fundingPct(value, reference);
  if (pct == null) return 'none';
  if (pct >= scale.strongPct) return 'strong-up';
  if (pct >= scale.weakPct) return 'up';
  if (pct > -scale.weakPct) return 'flat';
  if (pct > -scale.strongPct) return 'down';
  return 'strong-down';
}

/**
 * Класс подсветки для ячейки. У «не с чем сравнить» класса нет вовсе: это не
 * уровень фандинга, а отсутствие курса, и красить такое не во что.
 */
export function levelClass(level: FundingLevel): string {
  return level === 'none' ? '' : `fnd-${level}`;
}
