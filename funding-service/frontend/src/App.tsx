import { useState, useEffect } from 'react';
import { useFundingStore } from './store/fundingStore';
import { useWebSocket } from './hooks/useWebSocket';
import { useFundingAlert } from './hooks/useFundingAlert';
import { initAlertUnlock } from './lib/alertSound';
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
  useFundingAlert();
  const [page, setPage] = useState<Page>(pageFromPath);
  const [menuOpen, setMenuOpen] = useState(false);

  const current = useFundingStore((s) => s.current);
  const previous = useFundingStore((s) => s.previous);

  useEffect(() => {
    initAlertUnlock();
  }, []);

  useEffect(() => {
    const handler = () => setPage(pageFromPath());
    window.addEventListener('popstate', handler);
    return () => window.removeEventListener('popstate', handler);
  }, []);

  const navigate = (p: Page) => {
    const path = p === 'main' ? '/' : `/${p}`;
    window.history.pushState({}, '', path);
    setPage(p);
    setMenuOpen(false); // close the mobile burger menu after picking a page
  };

  const links: { page: Page; label: string }[] = [
    { page: 'main', label: 'Таблица' },
    { page: 'calculator', label: 'Калькулятор' },
    { page: 'race', label: 'Скорость' },
    { page: 'journal', label: 'Журнал' },
    { page: 'settings', label: 'Настройки' },
  ];

  return (
    <div className="app">
      <header className="app-header">
        <h1 className="app-title">Funding Rates</h1>

        <div className="app-header-controls">
          <StatusDot />
          <button
            className="nav-burger"
            aria-label="Меню"
            aria-expanded={menuOpen}
            onClick={() => setMenuOpen((o) => !o)}
          >
            {menuOpen ? '✕' : '☰'}
          </button>
        </div>

        <nav className={`app-nav${menuOpen ? ' app-nav-open' : ''}`}>
          {links.map(({ page: p, label }) => (
            <button
              key={p}
              className={`nav-link${page === p ? ' nav-link-active' : ''}`}
              aria-current={page === p ? 'page' : undefined}
              onClick={() => navigate(p)}
            >
              {label}
            </button>
          ))}
        </nav>
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
