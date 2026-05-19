import { useFundingStore } from './store/fundingStore';
import { useWebSocket } from './hooks/useWebSocket';
import { FundingTable } from './components/FundingTable';
import { USDTPriceCard } from './components/USDTPriceCard';
import './App.css';

const WS_URL = `${location.protocol === 'https:' ? 'wss' : 'ws'}://${location.host}/ws`;

function StatusDot() {
  const status = useFundingStore((s) => s.wsStatus);
  return <span className={`status-dot status-${status}`} title={status} />;
}

export default function App() {
  useWebSocket(WS_URL);

  const current = useFundingStore((s) => s.current);
  const previous = useFundingStore((s) => s.previous);

  return (
    <div className="app">
      <header className="app-header">
        <h1 className="app-title">Funding Rates</h1>
        <StatusDot />
      </header>

      <main className="app-main">
        <USDTPriceCard
          price={current?.usdtrub_price ?? null}
          prevPrice={previous?.usdtrub_price ?? null}
        />
        <FundingTable current={current} previous={previous} />
      </main>
    </div>
  );
}
