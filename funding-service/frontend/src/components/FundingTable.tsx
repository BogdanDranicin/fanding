import type { FundingSnapshot, InstrumentFunding } from '../types/funding';
import { useFlashOnChange } from '../hooks/useFlashOnChange';

interface Props {
  current: FundingSnapshot | null;
  previous: FundingSnapshot | null;
}

const fmt4 = new Intl.NumberFormat('ru-RU', { minimumFractionDigits: 4, maximumFractionDigits: 4 });
const fmt6 = new Intl.NumberFormat('ru-RU', { minimumFractionDigits: 6, maximumFractionDigits: 6 });

function Cell({ value, fmt }: { value: number | null | undefined; fmt: 'rate' | 'funding' }) {
  const flash = useFlashOnChange(value ?? null);
  const f = fmt === 'rate' ? fmt4 : fmt6;
  return (
    <td className={['cell', flash].filter(Boolean).join(' ')}>
      {value != null ? f.format(value) : '—'}
    </td>
  );
}

interface Row {
  label: string;
  field: keyof InstrumentFunding;
  fmt: 'rate' | 'funding';
  skipCNY?: boolean;
}

const ROWS: Row[] = [
  { label: 'VWAP',         field: 'vwap',          fmt: 'rate'    },
  { label: 'Last Price',   field: 'last_price',    fmt: 'rate'    },
  { label: 'Forex funding',field: 'forex_funding', fmt: 'funding', skipCNY: true },
  { label: 'MOEX funding', field: 'moex_funding',  fmt: 'funding' },
  { label: 'CB funding',   field: 'cb_funding',    fmt: 'funding', skipCNY: true },
];

export function FundingTable({ current }: Props) {
  const usd = current?.USDRUBF;
  const eur = current?.EURRUBF;
  const cny = current?.CNYRUBF;

  return (
    <table className="funding-table">
      <thead>
        <tr>
          <th />
          <th>USDRUBF</th>
          <th>EURRUBF</th>
          <th>CNYRUBF</th>
        </tr>
      </thead>
      <tbody>
        {ROWS.map(({ label, field, fmt, skipCNY }) => (
          <tr key={label}>
            <th className="row-label">{label}</th>
            <Cell value={usd?.[field] as number | undefined} fmt={fmt} />
            <Cell value={eur?.[field] as number | undefined} fmt={fmt} />
            {skipCNY
              ? <td className="cell muted">—</td>
              : <Cell value={cny?.[field] as number | undefined} fmt={fmt} />
            }
          </tr>
        ))}
      </tbody>
    </table>
  );
}
