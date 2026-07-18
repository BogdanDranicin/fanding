-- Диагностика реконструкции фандинга в журнале: чтобы вживую отслеживать
-- расхождение НАШЕГО расчёта (cb_funding) с фактическим SWAPRATE биржи.
-- Для USD и EUR добавляем: ногу фьючерса на 15:30 (settl_vwap), фактический
-- MOEX SWAPRATE и значение реконструкции БЕЗ мёртвой зоны K1.
ALTER TABLE cb_publications
    ADD COLUMN IF NOT EXISTS settl_vwap_usd              NUMERIC(18,8),
    ADD COLUMN IF NOT EXISTS settl_vwap_eur              NUMERIC(18,8),
    ADD COLUMN IF NOT EXISTS moex_funding_usd            NUMERIC(18,8),
    ADD COLUMN IF NOT EXISTS moex_funding_eur            NUMERIC(18,8),
    ADD COLUMN IF NOT EXISTS cb_funding_no_deadband_usd  NUMERIC(18,8),
    ADD COLUMN IF NOT EXISTS cb_funding_no_deadband_eur  NUMERIC(18,8);
