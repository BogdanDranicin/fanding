import type { FundingSnapshot, InstrumentFunding } from '../types/funding';
import { useFlashOnChange } from '../hooks/useFlashOnChange';
import { useIsMobile } from '../hooks/useIsMobile';

interface Props {
  current: FundingSnapshot | null;
  previous: FundingSnapshot | null;
}

const fmt4 = new Intl.NumberFormat('ru-RU', { minimumFractionDigits: 4, maximumFractionDigits: 4 });
const fmt6 = new Intl.NumberFormat('ru-RU', { minimumFractionDigits: 6, maximumFractionDigits: 6 });

function RateCell({ value }: { value: number | null | undefined }) {
  const flash = useFlashOnChange(value ?? null);
  return (
    <td className={['cell', flash].filter(Boolean).join(' ')}>
      {value != null ? fmt4.format(value) : '—'}
    </td>
  );
}

// DeltaCell renders a signed difference (predicted − actual) with its percentage
// of the reference, used by the "Ошибка прогноза" row to gauge prediction accuracy.
function DeltaCell({ value, reference }: { value: number | null | undefined; reference: number }) {
  const flash = useFlashOnChange(value ?? null);
  if (value == null) {
    return <td className={['cell', flash].filter(Boolean).join(' ')}>—</td>;
  }
  const sign = value >= 0 ? '+' : '';
  let pctStr = '';
  if (reference > 0) {
    const pct = (value / reference) * 100;
    pctStr = ` (${pct >= 0 ? '+' : ''}${pct.toFixed(3)}%)`;
  }
  return (
    <td className={['cell', flash].filter(Boolean).join(' ')}>
      {sign}{fmt4.format(value)}
      {pctStr && <span className="pct">{pctStr}</span>}
    </td>
  );
}

function FundingCell({ value, reference }: { value: number | null | undefined; reference: number }) {
  const flash = useFlashOnChange(value ?? null);

  const highlight =
    value == null ? '' :
    value >= 0.1  ? 'funding-positive' :
    value <= -0.1 ? 'funding-negative' : '';

  let pctStr = '';
  if (value != null && reference > 0) {
    const pct = (value / reference) * 100;
    pctStr = `(${pct >= 0 ? '+' : ''}${pct.toFixed(3)}%)`;
  }

  return (
    <td className={['cell', flash, highlight].filter(Boolean).join(' ')}>
      {value != null ? (
        <>
          {fmt6.format(value)}
          {pctStr && <span className="pct"> {pctStr}</span>}
        </>
      ) : '—'}
    </td>
  );
}

interface Row {
  label: string;
  field?: keyof InstrumentFunding;
  kind: 'rate' | 'funding' | 'delta';
  refField?: keyof InstrumentFunding;
  // compute derives the cell value from the instrument when it is not a plain field
  // (used by the prediction-error row: predicted_cb_rate − official_rate).
  compute?: (inst: InstrumentFunding) => number | undefined;
  skipCNY?: boolean;
}

const predictionError = (inst: InstrumentFunding): number | undefined =>
  inst.predicted_cb_rate != null && inst.official_rate != null
    ? inst.predicted_cb_rate - inst.official_rate
    : undefined;

const ROWS: Row[] = [
  { label: 'VWAP',                  field: 'vwap',              kind: 'rate'                                                  },
  { label: 'Last Price',            field: 'last_price',        kind: 'rate'                                                  },
  { label: 'Предсказанный курс ЦБ', field: 'predicted_cb_rate', kind: 'rate',                              skipCNY: true       },
  { label: 'Курс ЦБ (факт)',        field: 'official_rate',     kind: 'rate',                              skipCNY: true       },
  { label: 'Ошибка прогноза',       kind: 'delta',  compute: predictionError, refField: 'official_rate',  skipCNY: true       },
  { label: 'Forex funding',         field: 'forex_funding',     kind: 'funding', refField: 'vwap',         skipCNY: true       },
  { label: 'Прогнозный фандинг',    field: 'predicted_funding', kind: 'funding', refField: 'predicted_cb_rate', skipCNY: true  },
  { label: 'MOEX funding',          field: 'moex_funding',      kind: 'funding', refField: 'last_price'                       },
  { label: 'CB funding',            field: 'cb_funding',        kind: 'funding', refField: 'official_rate', skipCNY: true       },
];

const SYMS = ['USDRUBF', 'EURRUBF', 'CNYRUBF'] as const;

// rowValue resolves a row's numeric value for an instrument, honouring compute.
function rowValue(row: Row, inst: InstrumentFunding | undefined): number | undefined {
  if (inst == null) return undefined;
  if (row.compute) return row.compute(inst);
  return row.field ? (inst[row.field] as number | undefined) : undefined;
}

function formatFundingRow(value: number | undefined, ref: number | undefined): { text: string; cls: string } {
  if (value == null) return { text: '—', cls: 'accordion-cell' };
  let text = fmt6.format(value);
  if (ref != null && ref > 0) {
    const pct = (value / ref) * 100;
    text += ` (${pct >= 0 ? '+' : ''}${pct.toFixed(3)}%)`;
  }
  const cls = value >= 0.1 ? 'accordion-cell funding-positive'
    : value <= -0.1 ? 'accordion-cell funding-negative'
    : 'accordion-cell';
  return { text, cls };
}

function formatDeltaRow(value: number | undefined, ref: number | undefined): string {
  if (value == null) return '—';
  let text = `${value >= 0 ? '+' : ''}${fmt4.format(value)}`;
  if (ref != null && ref > 0) {
    const pct = (value / ref) * 100;
    text += ` (${pct >= 0 ? '+' : ''}${pct.toFixed(3)}%)`;
  }
  return text;
}

function FundingTableMobile({ current }: Props) {
  return (
    <div className="accordion">
      {SYMS.map((sym) => {
        const inst = current?.[sym];
        const primaryFunding = inst?.cb_funding ?? inst?.moex_funding;
        const primaryLabel = inst?.cb_funding != null ? 'CB' : 'MOEX';
        const badgeCls = primaryFunding == null ? '' :
          primaryFunding >= 0.1  ? 'accordion-badge funding-positive' :
          primaryFunding <= -0.1 ? 'accordion-badge funding-negative' :
          'accordion-badge';

        return (
          <details key={sym} className="accordion-item">
            <summary className="accordion-summary">
              <span className="accordion-title">{sym}</span>
              {primaryFunding != null && (
                <span className={badgeCls}>
                  {primaryLabel}: {fmt6.format(primaryFunding)}
                </span>
              )}
            </summary>
            <div className="accordion-body">
              {ROWS.map((row) => {
                const { label, kind, refField, skipCNY } = row;
                if (skipCNY && sym === 'CNYRUBF') {
                  return (
                    <div key={label} className="accordion-row">
                      <span className="accordion-label">{label}</span>
                      <span className="accordion-cell muted">—</span>
                    </div>
                  );
                }
                const value = rowValue(row, inst);
                const ref = (refField ? inst?.[refField] : undefined) as number | undefined;
                let text: string;
                let cls = 'accordion-cell';
                if (kind === 'rate') {
                  text = value != null ? fmt4.format(value) : '—';
                } else if (kind === 'delta') {
                  text = formatDeltaRow(value, ref);
                } else {
                  ({ text, cls } = formatFundingRow(value, ref));
                }
                return (
                  <div key={label} className="accordion-row">
                    <span className="accordion-label">{label}</span>
                    <span className={cls}>{text}</span>
                  </div>
                );
              })}
            </div>
          </details>
        );
      })}
    </div>
  );
}

export function FundingTable({ current, previous }: Props) {
  const isMobile = useIsMobile();
  if (isMobile) return <FundingTableMobile current={current} previous={previous} />;

  return (
    <table className="funding-table">
      <thead>
        <tr>
          <th />
          {SYMS.map((s) => <th key={s}>{s}</th>)}
        </tr>
      </thead>
      <tbody>
        {ROWS.map((row) => (
          <tr key={row.label}>
            <th className="row-label">{row.label}</th>
            {SYMS.map((sym) => {
              if (row.skipCNY && sym === 'CNYRUBF') {
                return <td key={sym} className="cell muted">—</td>;
              }
              const inst = current?.[sym];
              const value = rowValue(row, inst);
              if (row.kind === 'rate') return <RateCell key={sym} value={value} />;
              const ref = (row.refField ? inst?.[row.refField] : undefined) as number | undefined;
              if (row.kind === 'delta') return <DeltaCell key={sym} value={value} reference={ref ?? 0} />;
              return <FundingCell key={sym} value={value} reference={ref ?? 0} />;
            })}
          </tr>
        ))}
      </tbody>
    </table>
  );
}
