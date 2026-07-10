import { useState, useEffect } from 'react';
import { useFundingStore } from './store/fundingStore';
import { useWebSocket } from './hooks/useWebSocket';
import { FundingTable } from './components/FundingTable';
import { SettingsPage } from './components/SettingsPage';
import { CalculatorPage } from './components/CalculatorPage';
import { RacePage } from './components/RacePage';
import { JournalPage } from './components/JournalPage';
import './App.css';

const WS_URL = import.meta.env.VITE_WS_URL as string
  ?? `${location.protocol === 'https:' ? 'wss' : 'ws'}://${location.host}/ws`;

type Page = 'main' | 'settings' | 'calculator' | 'race' | 'journal';

const VALID_PAGES: Page[] = ['main', 'settings', 'calculator', 'race', 'journal'];

function pageFromPath(): Page {
  const p = window.location.pathname.slice(1) as Page;
  return VALID_PAGES.includes(p) ? p : 'main';
}

function StatusDot() {
  const status = useFundingStore((s) => s.wsStatus);
  return <span className={`status-dot status-${status}`} title={status} />;
}

export default function App() {
  useWebSocket(WS_URL);
  const [page, setPage] = useState<Page>(pageFromPath);

  const current = useFundingStore((s) => s.current);
  const previous = useFundingStore((s) => s.previous);

  useEffect(() => {
    const handler = () => setPage(pageFromPath());
    window.addEventListener('popstate', handler);
    return () => window.removeEventListener('popstate', handler);
  }, []);

  const navigate = (p: Page) => {
    const path = p === 'main' ? '/' : `/${p}`;
    window.history.pushState({}, '', path);
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
            style={{ fontWeight: page === 'calculator' ? 600 : undefined }}
            onClick={() => navigate('calculator')}
          >
            Калькулятор
          </button>
          <button
            className="nav-link"
            style={{ fontWeight: page === 'race' ? 600 : undefined }}
            onClick={() => navigate('race')}
          >
            Скорость
          </button>
          <button
            className="nav-link"
            style={{ fontWeight: page === 'journal' ? 600 : undefined }}
            onClick={() => navigate('journal')}
          >
            Журнал
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
        {page === 'calculator' && <CalculatorPage />}
        {page === 'race' && <RacePage />}
        {page === 'journal' && <JournalPage />}
        {page === 'main' && (
          <FundingTable current={current} previous={previous} />
        )}
      </main>
    </div>
  );
}
