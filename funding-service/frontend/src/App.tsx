import { useState, useEffect } from 'react';
import { useFundingStore } from './store/fundingStore';
import { useWebSocket } from './hooks/useWebSocket';
import { FundingTable } from './components/FundingTable';
import { SettingsPage } from './components/SettingsPage';
import { PositionsPage } from './components/PositionsPage';
import './App.css';

const WS_URL = import.meta.env.VITE_WS_URL as string
  ?? `${location.protocol === 'https:' ? 'wss' : 'ws'}://${location.host}/ws`;

type Page = 'main' | 'positions' | 'settings';

const VALID_PAGES: Page[] = ['main', 'positions', 'settings'];

function pageFromHash(): Page {
  const h = window.location.hash.slice(1) as Page;
  return VALID_PAGES.includes(h) ? h : 'main';
}

function StatusDot() {
  const status = useFundingStore((s) => s.wsStatus);
  return <span className={`status-dot status-${status}`} title={status} />;
}

export default function App() {
  useWebSocket(WS_URL);
  const [page, setPage] = useState<Page>(pageFromHash);

  const current = useFundingStore((s) => s.current);
  const previous = useFundingStore((s) => s.previous);

  useEffect(() => {
    const handler = () => setPage(pageFromHash());
    window.addEventListener('hashchange', handler);
    return () => window.removeEventListener('hashchange', handler);
  }, []);

  const navigate = (p: Page) => {
    window.location.hash = p === 'main' ? '' : p;
    setPage(p);
  };

  return (
    <div className="app">
      <header className="app-header">
        <h1 className="app-title">Funding Rates</h1>
        <div style={{ display: 'flex', alignItems: 'center', gap: 12 }}>
          <button
            className="nav-link"
            style={{ fontWeight: page === 'main' ? 600 : undefined }}
            onClick={() => navigate('main')}
          >
            Таблица
          </button>
          <button
            className="nav-link"
            style={{ fontWeight: page === 'positions' ? 600 : undefined }}
            onClick={() => navigate('positions')}
          >
            Позиции
          </button>
          <button
            className="nav-link"
            style={{ fontWeight: page === 'settings' ? 600 : undefined }}
            onClick={() => navigate('settings')}
          >
            Настройки
          </button>
          <StatusDot />
        </div>
      </header>

      <main className="app-main">
        {page === 'settings' && (
          <SettingsPage onBack={() => navigate('main')} />
        )}
        {page === 'positions' && (
          <PositionsPage onGoToSettings={() => navigate('settings')} />
        )}
        {page === 'main' && (
          <FundingTable current={current} previous={previous} />
        )}
      </main>
    </div>
  );
}
