import { useState } from 'react';
import { useFundingStore } from './store/fundingStore';
import { useWebSocket } from './hooks/useWebSocket';
import { FundingTable } from './components/FundingTable';
import { SettingsPage } from './components/SettingsPage';
import { PositionsPage } from './components/PositionsPage';
import './App.css';

const WS_URL = import.meta.env.VITE_WS_URL as string
  ?? `${location.protocol === 'https:' ? 'wss' : 'ws'}://${location.host}/ws`;

type Page = 'main' | 'positions' | 'settings';

function StatusDot() {
  const status = useFundingStore((s) => s.wsStatus);
  return <span className={`status-dot status-${status}`} title={status} />;
}

export default function App() {
  useWebSocket(WS_URL);
  const [page, setPage] = useState<Page>('main');

  const current = useFundingStore((s) => s.current);
  const previous = useFundingStore((s) => s.previous);

  return (
    <div className="app">
      <header className="app-header">
        <h1 className="app-title">Funding Rates</h1>
        <div style={{ display: 'flex', alignItems: 'center', gap: 12 }}>
          <button
            className="nav-link"
            style={{ fontWeight: page === 'main' ? 600 : undefined }}
            onClick={() => setPage('main')}
          >
            Таблица
          </button>
          <button
            className="nav-link"
            style={{ fontWeight: page === 'positions' ? 600 : undefined }}
            onClick={() => setPage('positions')}
          >
            Позиции
          </button>
          <button
            className="nav-link"
            style={{ fontWeight: page === 'settings' ? 600 : undefined }}
            onClick={() => setPage('settings')}
          >
            Настройки
          </button>
          <StatusDot />
        </div>
      </header>

      <main className="app-main">
        {page === 'settings' && (
          <SettingsPage onBack={() => setPage('main')} />
        )}
        {page === 'positions' && (
          <PositionsPage onGoToSettings={() => setPage('settings')} />
        )}
        {page === 'main' && (
          <FundingTable current={current} previous={previous} />
        )}
      </main>
    </div>
  );
}
