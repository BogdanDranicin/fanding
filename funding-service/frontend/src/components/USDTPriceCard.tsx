import { useFlashOnChange } from '../hooks/useFlashOnChange';

interface Props {
  price: number | null;
  prevPrice: number | null;
}

const fmt4 = new Intl.NumberFormat('ru-RU', { minimumFractionDigits: 4, maximumFractionDigits: 4 });

export function USDTPriceCard({ price, prevPrice }: Props) {
  const flash = useFlashOnChange(price);
  const up = price != null && prevPrice != null && price > prevPrice;
  const down = price != null && prevPrice != null && price < prevPrice;

  return (
    <div className={['usdt-card', flash].filter(Boolean).join(' ')}>
      <span className="usdt-label">USDT/RUB</span>
      <span className={['usdt-price', up ? 'up' : down ? 'down' : ''].filter(Boolean).join(' ')}>
        {price != null ? fmt4.format(price) : '—'}
      </span>
    </div>
  );
}
