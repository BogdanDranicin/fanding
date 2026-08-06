import { useState, useEffect } from 'react';
import { useFundingStore } from './store/fundingStore';
import { useAuthStore } from './store/authStore';
import { useWebSocket } from './hooks/useWebSocket';
import { useAuthSync } from './hooks/useAuth';
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

// Страницы только для админов: «Журнал» и «Скорость» — служебная диагностика,
// остальным их не показываем вовсе (и бэкенд отдаёт по ним 403).
const ADMIN_PAGES: Page[] = ['race', 'journal'];

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
  useAuthSync();
  useFundingAlert();
  const [page, setPage] = useState<Page>(pageFromPath);
  const [menuOpen, setMenuOpen] = useState(false);

  const current = useFundingStore((s) => s.current);
  const previous = useFundingStore((s) => s.previous);
  const isAdmin = useAuthStore((s) => s.me.is_admin);
  const authLoading = useAuthStore((s) => s.loading);

  useEffect(() => {
    initAlertUnlock();
  }, []);

  useEffect(() => {
    const handler = () => setPage(pageFromPath());
    window.addEventListener('popstate', handler);
    return () => window.removeEventListener('popstate', handler);
  }, []);

  // Прямой заход по /journal или /race без прав — молча показываем главную.
  // Пока права не загрузились, админскую страницу тоже не рисуем: иначе она
  // успела бы сходить в API и получить 403 у собственного же админа.
  const deniedAdminPage = ADMIN_PAGES.includes(page) && !isAdmin;
  const shownPage: Page = deniedAdminPage ? 'main' : page;

  // Адрес в строке браузера приводим к тому, что реально показано — но только
  // когда права уже известны, иначе перезагрузка сбрасывала бы админа с его страницы.
  useEffect(() => {
    if (!authLoading && deniedAdminPage) window.history.replaceState({}, '', '/');
  }, [authLoading, deniedAdminPage]);

  const navigate = (p: Page) => {
    const path = p === 'main' ? '/' : `/${p}`;
    window.history.pushState({}, '', path);
    setPage(p);
    setMenuOpen(false); // close the mobile burger menu after picking a page
  };

  const links = ([
    { page: 'main', label: 'Фандинг' },
    { page: 'calculator', label: 'Калькулятор' },
    { page: 'race', label: 'Скорость' },
    { page: 'journal', label: 'Журнал' },
    { page: 'settings', label: 'Настройки' },
  ] satisfies { page: Page; label: string }[]).filter(
    (l) => isAdmin || !ADMIN_PAGES.includes(l.page),
  );

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
              className={`nav-link${shownPage === p ? ' nav-link-active' : ''}`}
              aria-current={shownPage === p ? 'page' : undefined}
              onClick={() => navigate(p)}
            >
              {label}
            </button>
          ))}
        </nav>
      </header>

      <main className="app-main">
        {shownPage === 'settings' && (
          <SettingsPage onBack={() => navigate('main')} />
        )}
        {shownPage === 'calculator' && <CalculatorPage />}
        {shownPage === 'race' && <RacePage />}
        {shownPage === 'journal' && <JournalPage />}
        {shownPage === 'main' && (
          <FundingTable current={current} previous={previous} />
        )}
      </main>
    </div>
  );
}
