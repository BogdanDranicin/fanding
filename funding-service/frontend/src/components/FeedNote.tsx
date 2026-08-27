import type { FundingSnapshot } from '../types/funding';

// lagLabel — отставание по-человечески: «12 мс», «1.7 с», «15 мин».
function lagLabel(ms: number): string {
  if (ms < 1000) return `${Math.round(ms)} мс`;
  if (ms < 60_000) return `${(ms / 1000).toFixed(1)} с`;
  return `${Math.round(ms / 60_000)} мин`;
}

// Как называется источник ноги фьючерса на человеческом языке.
const SETTL_SOURCE: Record<string, string> = {
  live: 'по живому потоку сделок',
  'iss-trades': 'по ленте сделок MOEX ISS',
  voltoday: 'по приросту дневного объёма (лента сделок молчала)',
};

/**
 * Строка под таблицей: откуда взяты цифры и насколько они свежие.
 *
 * Появилась потому, что вопрос «почему задержка» нельзя было проверить глазами.
 * Цена, отставшая на пятнадцать минут, выглядит ровно как цена, пришедшая
 * секунду назад, и единственным способом различить их было лезть в логи.
 * Теперь страница говорит это сама — и цифрой замера, а не обещанием.
 */
export function FeedNote({ current }: { current: FundingSnapshot | null }) {
  if (!current) return null;
  const feed = current.feed;
  const settl = current.USDRUBF.settl_source ?? current.EURRUBF.settl_source;

  return (
    <p className="feed-note">
      {feed?.live ? (
        <>
          Сделки идут живым потоком брокера
          {feed.symbols > 0 && <> по {feed.symbols} фьючерсам</>}
          {feed.lag_ms > 0 && <>, отставание от биржи {lagLabel(feed.lag_ms)}</>}
          {'. '}
        </>
      ) : (
        <>
          Живой поток сейчас недоступен — цены и объёмы идут публичной лентой
          MOEX ISS, а она приходит с задержкой ровно в 15 минут.{' '}
        </>
      )}
      {settl && (
        <>
          Нога фьючерса на 15:30 посчитана {SETTL_SOURCE[settl] ?? settl}
          {(current.USDRUBF.settl_provisional || current.EURRUBF.settl_provisional)
            && ' — предварительно, окно могло быть обрезано, значение ещё уточнится'}
          .
        </>
      )}
    </p>
  );
}
