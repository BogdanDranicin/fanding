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
  first_seen: string;
  last_seen: string;
  price_first: number;
  price_last: number;
  detected_at: string;
  updated_at: string;
  /** Робот печатает прямо сейчас. */
  active: boolean;
}

export interface RobotsResponse {
  /** Тикеры, за лентой которых сервис следит. */
  watching: string[];
  robots: RobotSession[];
  as_of: string;
}
