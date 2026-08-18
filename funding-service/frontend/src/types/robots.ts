// Робот, найденный в ленте сделок: серия одинаковых принтов с ровным периодом.
export interface RobotSession {
  id: number;
  symbol: string;
  /** Сторона агрессора: B — покупал (лонг), S — продавал (шорт). */
  side: 'B' | 'S';
  /** Лотовка: границы размеров принтов и типичный размер. */
  qty_min: number;
  qty_max: number;
  qty_typical: number;
  /** Тайминг: период повторения в секундах. */
  period_sec: number;
  /** Разброс интервалов вокруг периода; у настоящего робота близок к нулю. */
  jitter: number;
  prints: number;
  beats: number;
  confidence: number;
  /** Серия ещё короче той, на которой периодичность отличима от совпадения. */
  provisional: boolean;
  first_seen: string;
  last_seen: string;
  price_first: number;
  price_last: number;
  detected_at: string;
  updated_at: string;
  /** Робот печатает прямо сейчас. */
  active: boolean;
  /** Сколько тактов подряд робот промолчал: 1 — подсветка, 2 — робота снимают. */
  misses: number;
  /**
   * Когда ждать следующий принт. Считается продолжением фазы вперёд по стенным
   * часам: лента ISS запаздывает на минуты, и «последний принт плюс период»
   * указывал бы в прошлое.
   */
  next_beat_at: string;
  /** Сколько робот уже напечатал, лотов. */
  volume_lots: number;
  /** Дневной оборот инструмента на стороне робота — база для силы. */
  day_side_lots: number;
  /** Доля робота в этом обороте, проценты. */
  strength_pct: number;
}

/** Дневной оборот инструмента, разложенный по стороне агрессора. */
export interface DayVolume {
  symbol: string;
  /** Биржевой день, YYYY-MM-DD MSK. */
  date: string;
  buy: number;
  sell: number;
  trades: number;
  /** Время первой учтённой сделки: после перезапуска среди дня база неполная. */
  since: string;
  latest: string;
}

export interface RobotsResponse {
  /** Тикеры, за лентой которых сервис следит. */
  watching: string[];
  robots: RobotSession[];
  day_volumes: DayVolume[];
  as_of: string;
}
