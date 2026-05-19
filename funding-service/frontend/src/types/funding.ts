export interface InstrumentFunding {
  vwap: number;
  last_price: number;
  moex_funding?: number;
  forex_funding?: number;
  cb_funding?: number;
  official_rate?: number;
}

export interface FundingSnapshot {
  ts: number; // unix milliseconds
  USDRUBF: InstrumentFunding;
  EURRUBF: InstrumentFunding;
  CNYRUBF: InstrumentFunding;
  usdtrub_price: number;
}

export type WSStatus = 'connecting' | 'connected' | 'disconnected';

export interface WSMessage {
  type: 'snapshot' | 'publication' | 'ping';
  ts: number;
  payload: unknown;
}
