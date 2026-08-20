import { describe, expect, it } from 'vitest';
import {
  clockOf,
  describeAlarm,
  makeAlarm,
  minutesOf,
  mskDayStart,
  mskWeekday,
  nextFireAt,
  nextOccurrence,
  normalizeAlarm,
} from './timeAlarms';

// Все проверки ведутся от московского времени, записанного явным смещением:
// часовой пояс машины, на которой идут тесты, влиять на расписание не должен.
function msk(iso: string): number {
  return Date.parse(`${iso}+03:00`);
}

describe('московские сутки', () => {
  it('начинаются в полночь МСК, а не UTC', () => {
    expect(mskDayStart(msk('2026-08-20T00:30:00'))).toBe(msk('2026-08-20T00:00:00'));
    expect(mskDayStart(msk('2026-08-20T23:59:59'))).toBe(msk('2026-08-20T00:00:00'));
    // 02:00 МСК — это ещё вчерашние сутки по UTC: как раз тот случай, на котором
    // ломается наивный расчёт через UTC-полночь.
    expect(mskDayStart(msk('2026-08-20T02:00:00'))).toBe(msk('2026-08-20T00:00:00'));
  });

  it('знают день недели', () => {
    expect(mskWeekday(msk('2026-08-20T12:00:00'))).toBe(4); // четверг
    expect(mskWeekday(msk('2026-08-22T12:00:00'))).toBe(6); // суббота
    expect(mskWeekday(msk('2026-08-23T12:00:00'))).toBe(0); // воскресенье
  });
});

describe('часы', () => {
  it('переводятся в обе стороны', () => {
    expect(clockOf(0)).toBe('00:00');
    expect(clockOf(9 * 60 + 5)).toBe('09:05');
    expect(clockOf(23 * 60 + 59)).toBe('23:59');
    expect(minutesOf('09:05')).toBe(545);
    expect(minutesOf('7:30')).toBe(450);
  });

  it('не принимают мусор', () => {
    expect(minutesOf('')).toBeNull();
    expect(minutesOf('25:00')).toBeNull();
    expect(minutesOf('12:60')).toBeNull();
    expect(minutesOf('полдень')).toBeNull();
  });
});

describe('переход часа и получаса', () => {
  it('раз в час бьёт ровно в :00', () => {
    const a = makeAlarm({ everyMin: 60 });
    expect(nextOccurrence(a, msk('2026-08-20T14:12:33'))).toBe(msk('2026-08-20T15:00:00'));
  });

  it('раз в полчаса бьёт в :00 и :30', () => {
    const a = makeAlarm({ everyMin: 30 });
    expect(nextOccurrence(a, msk('2026-08-20T14:12:00'))).toBe(msk('2026-08-20T14:30:00'));
    expect(nextOccurrence(a, msk('2026-08-20T14:30:00'))).toBe(msk('2026-08-20T15:00:00'));
  });

  it('раз в два часа отсчитывается от полуночи', () => {
    const a = makeAlarm({ everyMin: 120 });
    expect(nextOccurrence(a, msk('2026-08-20T09:01:00'))).toBe(msk('2026-08-20T10:00:00'));
  });

  it('со сдвигом начинает день с заданной отметки', () => {
    const a = makeAlarm({ everyMin: 120, startMin: 9 * 60 + 45 });
    expect(nextOccurrence(a, msk('2026-08-20T08:00:00'))).toBe(msk('2026-08-20T09:45:00'));
    expect(nextOccurrence(a, msk('2026-08-20T10:00:00'))).toBe(msk('2026-08-20T11:45:00'));
  });

  // Шаг, не делящий сутки нацело, не должен уползать день ото дня: расписание
  // пересобирается каждые сутки от своей первой отметки.
  it('на исходе суток переходит к первой отметке следующего дня', () => {
    const a = makeAlarm({ everyMin: 50, startMin: 9 * 60 });
    expect(nextOccurrence(a, msk('2026-08-20T23:40:00'))).toBe(msk('2026-08-21T09:00:00'));
  });
});

describe('точное время', () => {
  it('срабатывает сегодня, если момент ещё не прошёл', () => {
    const a = makeAlarm({ kind: 'at', atMin: 18 * 60 + 45 });
    expect(nextOccurrence(a, msk('2026-08-20T10:00:00'))).toBe(msk('2026-08-20T18:45:00'));
  });

  it('переносится на завтра, если момент прошёл', () => {
    const a = makeAlarm({ kind: 'at', atMin: 18 * 60 + 45 });
    expect(nextOccurrence(a, msk('2026-08-20T19:00:00'))).toBe(msk('2026-08-21T18:45:00'));
  });

  it('по будням перепрыгивает выходные', () => {
    const a = makeAlarm({ kind: 'at', atMin: 10 * 60, weekdaysOnly: true });
    // Пятница после отметки → следующая будет в понедельник.
    expect(nextOccurrence(a, msk('2026-08-21T11:00:00'))).toBe(msk('2026-08-24T10:00:00'));
  });

  it('по будням не звонит и по интервалу в субботу', () => {
    const a = makeAlarm({ everyMin: 60, weekdaysOnly: true });
    expect(nextOccurrence(a, msk('2026-08-22T12:00:00'))).toBe(msk('2026-08-24T00:00:00'));
  });
});

describe('предупреждение до отметки', () => {
  it('сдвигает сигнал назад на заданные секунды', () => {
    const a = makeAlarm({ everyMin: 60, leadSec: 30 });
    expect(nextFireAt(a, msk('2026-08-20T14:12:00'))).toBe(msk('2026-08-20T14:59:30'));
  });

  // Момент между сигналом и самой отметкой: сигнал уже прозвучал, и следующим
  // должен быть следующий час, а не тот же самый.
  it('не возвращает уже прозвучавший сигнал', () => {
    const a = makeAlarm({ everyMin: 60, leadSec: 30 });
    expect(nextFireAt(a, msk('2026-08-20T14:59:40'))).toBe(msk('2026-08-20T15:59:30'));
  });
});

describe('подпись расписания', () => {
  it('читается словами', () => {
    expect(describeAlarm(makeAlarm({ everyMin: 60 }))).toBe('каждый час в :00');
    expect(describeAlarm(makeAlarm({ everyMin: 30 }))).toBe('каждые 30 минут — :00, :30');
    expect(describeAlarm(makeAlarm({ everyMin: 120 }))).toBe('каждые 2 часа');
    expect(describeAlarm(makeAlarm({ everyMin: 1 }))).toBe('каждую минуту');
    // Шаг, дающий десятки отметок в часе, перечислять нельзя — подпись займёт
    // полстраницы; такие описываются одним шагом.
    expect(describeAlarm(makeAlarm({ everyMin: 5 }))).toBe('каждые 5 минут');
    expect(describeAlarm(makeAlarm({ everyMin: 15 }))).toBe('каждые 15 минут — :00, :15, :30, :45');
    expect(describeAlarm(makeAlarm({ kind: 'at', atMin: 555 }))).toBe('в 09:15');
    expect(describeAlarm(makeAlarm({ everyMin: 60, leadSec: 15, weekdaysOnly: true })))
      .toBe('каждый час в :00, сигнал за 15 с, по будням');
  });
});

describe('чтение сохранённого', () => {
  it('чинит испорченную запись вместо того, чтобы ронять расписание', () => {
    const a = normalizeAlarm({ everyMin: -5, startMin: 9999, leadSec: 1e6, tone: 'сирена' as never });
    expect(a.everyMin).toBe(1);
    expect(a.startMin).toBe(1439);
    expect(a.leadSec).toBe(600);
    expect(a.tone).toBe('chime');
    expect(a.id).not.toBe('');
  });

  it('оставляет разумные значения как есть', () => {
    const a = normalizeAlarm({ id: 'x', kind: 'at', atMin: 600, tone: 'gong', label: 'открытие' });
    expect(a).toMatchObject({ id: 'x', kind: 'at', atMin: 600, tone: 'gong', label: 'открытие' });
  });
});
