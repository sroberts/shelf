// Registers the service worker in a production build. Dev has none, so a
// stale worker can never serve an old shell during development.
export function registerWorker() {
  if (import.meta.env.DEV || !('serviceWorker' in navigator)) return;
  addEventListener('load', () => {
    navigator.serviceWorker.register('/sw.js', { scope: '/' }).catch(() => {
      // An install that fails (the gateway's sign-in page, say) keeps the old
      // worker or none; the app works the same either way.
    });
  });
}
