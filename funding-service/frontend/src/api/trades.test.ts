import { describe, expect, it } from 'vitest';
import { loadPercent, pnlPercent, quoteFor, signedPercent, type TradePosition } from './trades';

const prices = { SBER: 280, GOLD: 2.9765, GDZ6: 4400, MXZ6: 233275, MXH7: 234000, MXZ7: 240000, IMOEXF: 2298, SiZ6: 84000, BRZL: 1 };

describe('quoteFor', () => {
  it('акция и конкретный фьючерс — по коду', () => {
    expect(quoteFor('SBER', prices)).toBe(280);
    expect(quoteFor('MXH7', prices)).toBe(234000);
  });
  it('общее название фьючерса — ближайший контракт', () => {
    expect(quoteFor('MIX', prices)).toBe(233275);
    expect(quoteFor('Si', prices)).toBe(84000);
  });
  it('золото — фьючерс, а не одноимённый фонд на TQBR', () => {
    expect(quoteFor('GOLD', prices)).toBe(4400);
  });
  it('индекс — по вечному фьючерсу, неизвестное — пусто', () => {
    expect(quoteFor('IMOEX', prices)).toBe(2298);
    expect(quoteFor('BR', prices)).toBeNull();
    expect(quoteFor('ОФЗ 26248', prices)).toBeNull();
  });
});

describe('pnlPercent', () => {
  it('лонг растёт с ценой, шорт — наоборот', () => {
    expect(pnlPercent({ direction: 'long', entry_price: 98.23 }, 100.66)).toBeCloseTo(2.47, 2);
    expect(pnlPercent({ direction: 'short', entry_price: 100 }, 95)).toBeCloseTo(5, 6);
  });
  it('цена входа, записанная с запятой вместо тысяч, приводится к масштабу', () => {
    expect(pnlPercent({ direction: 'short', entry_price: 233.05 }, 232250)).toBeCloseTo(0.343, 2);
    expect(pnlPercent({ direction: 'long', entry_price: 102 }, 100.29)).toBeCloseTo(-1.676, 2);
  });
  it('без входа или цены — пусто', () => {
    expect(pnlPercent({ direction: 'long', entry_price: null }, 100)).toBeNull();
    expect(pnlPercent({ direction: 'long', entry_price: 100 }, null)).toBeNull();
  });
});

describe('loadPercent', () => {
  const pos = (size: string) => ({ size }) as TradePosition;
  it('складывает доли в процентах', () => {
    expect(loadPercent([pos('110%'), pos('110%'), pos('100%')])).toBe(320);
  });
  it('разные единицы — загрузку не показываем', () => {
    expect(loadPercent([pos('25%'), pos('6500 шт')])).toBeNull();
    expect(loadPercent([])).toBeNull();
  });
});

it('signedPercent', () => {
  expect(signedPercent(2.4712)).toBe('+2,47%');
  expect(signedPercent(-1.1)).toBe('-1,10%');
  expect(signedPercent(null)).toBe('—');
});
