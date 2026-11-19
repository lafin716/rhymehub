/* rhymehub service worker: Web Push display and notification click routing.
 * Deliberately has no fetch caching: the dashboard is always served live from the core. */

const DEFAULT_ICON = '/icon.svg';

self.addEventListener('install', () => {
  self.skipWaiting();
});

self.addEventListener('activate', (event) => {
  event.waitUntil(self.clients.claim());
});

self.addEventListener('push', (event) => {
  let payload = {};
  if (event.data) {
    try {
      payload = event.data.json();
    } catch (_err) {
      payload = { title: 'rhymehub', body: event.data.text() };
    }
  }
  const title = typeof payload.title === 'string' && payload.title ? payload.title : 'rhymehub';
  const options = {
    body: typeof payload.body === 'string' ? payload.body : '',
    icon: DEFAULT_ICON,
    badge: DEFAULT_ICON,
    tag: typeof payload.kind === 'string' ? payload.kind : undefined,
    data: { url: typeof payload.url === 'string' ? payload.url : '/notifications' },
  };
  event.waitUntil(self.registration.showNotification(title, options));
});

function safeTargetUrl(raw) {
  try {
    const url = new URL(raw || '/', self.location.origin);
    // Only ever open same-origin URLs from push payloads.
    return url.origin === self.location.origin ? url.href : new URL('/', self.location.origin).href;
  } catch (_err) {
    return new URL('/', self.location.origin).href;
  }
}

self.addEventListener('notificationclick', (event) => {
  event.notification.close();
  const target = safeTargetUrl(event.notification.data && event.notification.data.url);

  event.waitUntil(
    (async () => {
      const windows = await self.clients.matchAll({ type: 'window', includeUncontrolled: true });
      for (const client of windows) {
        if (client.url === target && 'focus' in client) {
          return client.focus();
        }
      }
      for (const client of windows) {
        if ('navigate' in client && 'focus' in client) {
          await client.navigate(target);
          return client.focus();
        }
      }
      return self.clients.openWindow(target);
    })(),
  );
});
