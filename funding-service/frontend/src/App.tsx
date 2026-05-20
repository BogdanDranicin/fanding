import { useState } from 'react';
import { useFundingStore } from './store/fundingStore';
import { useWebSocket } from './hooks/useWebSocket';
import { FundingTable } from './components/FundingTable';
import { USDTPriceCard } from './components/USDTPriceCard';
import { SettingsPage } from './components/SettingsPage';
import './App.css';

const WS_URL = import.meta.env.VITE_WS_URL as string
  ?? `${location.protocol === 'https:' ? 'wss' : 'ws'}://${location.host}/ws`;

function StatusDot() {
  const status = useFundingStore((s) => s.wsStatus);
  return <span className={`status-dot status-${status}`} title={status} />;
}

export default function App() {
  useWebSocket(WS_URL);
  const [page, setPage] = useState<'main' | 'settings'>('main');

  const current = useFundingStore((s) => s.current);
  const previous = useFundingStore((s) => s.previous);

  return (
    <div className="app">
      <header className="app-header">
        <h1 className="app-title">Funding Rates</h1>
        <div style={{ display: 'flex', alignItems: 'center', gap: 12 }}>
          <button className="nav-link" onClick={() => setPage(page === 'settings' ? 'main' : 'settings')}>
            {page === 'settings' ? 'Таблица' : 'Настройки'}
          </button>
          <StatusDot />
        </div>
      </header>

      <main className="app-main">
        {page === 'settings' ? (
          <SettingsPage onBack={() => setPage('main')} />
        ) : (
          <>
            <USDTPriceCard
              price={current?.usdtrub_price ?? null}
              prevPrice={previous?.usdtrub_price ?? null}
            />
            <FundingTable current={current} previous={previous} />
          </>
        )}
      </main>
    </div>
  );
}
