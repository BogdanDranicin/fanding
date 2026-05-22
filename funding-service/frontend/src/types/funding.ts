export interface InstrumentFunding {
  vwap: number;
  last_price: number;
  moex_funding?: number;
  forex_funding?: number;
  cb_funding?: number;
  official_rate?: number;
  predicted_funding?: number;
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

export interface Position {
  symbol: string;
  exchange: string;
  side: 'buy' | 'sell';
  pos: number;
  entry_price: number;
  current_price?: number;
  unrealized_profit?: number;
  unrealized_profit_pct?: number;
  date: string;
  time: string;
  asset: string;
}

export interface BrokerConnectionStatus {
  configured: boolean;
  expires_at?: string;
}
