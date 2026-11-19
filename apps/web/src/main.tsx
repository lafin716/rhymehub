import { StrictMode } from 'react';
import { createRoot } from 'react-dom/client';
import { BrowserRouter } from 'react-router';
import App from './App';
import { registerServiceWorker } from './lib/push';
import './index.css';

// The service worker handles push and notificationclick; registration failures are non-fatal.
if ('serviceWorker' in navigator) {
  window.addEventListener('load', () => {
    void registerServiceWorker().catch(() => undefined);
  });
}

const root = document.getElementById('root');
if (!root) throw new Error('Missing #root element');

createRoot(root).render(
  <StrictMode>
    <BrowserRouter>
      <App />
    </BrowserRouter>
  </StrictMode>,
);
