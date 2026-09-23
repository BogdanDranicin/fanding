DROP TABLE IF EXISTS trade_price_fills;
ALTER TABLE trade_positions DROP COLUMN IF EXISTS close_auto;
ALTER TABLE trade_positions DROP COLUMN IF EXISTS entry_auto;
