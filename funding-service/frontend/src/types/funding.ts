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

export type WSStatus = 'connecting' | 'connected' | 'disconnected';

export interface WSMessage {
  type: 'snapshot' | 'publication' | 'ping';
  ts: number;
  payload: unknown;
}
