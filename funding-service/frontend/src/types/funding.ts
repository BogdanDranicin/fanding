export interface InstrumentFunding {
  vwap: number;
  last_price: number;
  moex_funding?: number;
  forex_funding?: number;
  cb_funding?: number;
  official_rate?: number;
  predicted_funding?: number;
  predicted_cb_rate?: number;
}

export interface FundingSnapshot {
  ts: number; // unix milliseconds
  USDRUBF: InstrumentFunding;
  EURRUBF: InstrumentFunding;
  CNYRUBF: InstrumentFunding;
  usdtrub_price: number;
}

export interface InstrumentSpec {
  symbol: string;
  initial_margin: number;
  lot_size: number;
  step_price: number;
  min_step: number;
}

export type SpecsMap = Record<string, InstrumentSpec>;

export interface InstrumentInfo {
  symbol: string;
  short_name: string;
  market_type: 'future' | 'stock';
  initial_margin: number;
  lot_size: number;
  step_price: number;
  min_step: number;
}

// CBPublication is one audit row from the /api/v1/cb-publications journal.
export interface CBPublication {
  date: string;                          // ISO date (UTC midnight of the MSK publication day)
  detected_at: string | null;           // ISO timestamp — exact moment we first saw the new rate
  updated_at: string | null;
  usd_rate: number | null;
  eur_rate: number | null;
  cny_rate: number | null;
  cb_funding_usd: number | null;
  cb_funding_eur: number | null;
  cny_funding: number | null;
  predicted_funding_usd: number | null;
  predicted_funding_eur: number | null;
  predicted_cb_rate_usd: number | null;
  predicted_cb_rate_eur: number | null;
  winner_channel: string | null;
  winner_latency_ms: number | null;
  // Диагностика реконструкции vs биржа (USD/EUR).
  settl_vwap_usd: number | null;
  settl_vwap_eur: number | null;
  moex_funding_usd: number | null;
  moex_funding_eur: number | null;
  cb_funding_no_deadband_usd: number | null;
  cb_funding_no_deadband_eur: number | null;
}

export type WSStatus = 'connecting' | 'connected' | 'disconnected';

export interface WSMessage {
  type: 'snapshot' | 'publication' | 'ping';
  ts: number;
  payload: unknown;
}
