// Service Worker for foswvs-go PWA
// Handles offline support, caching, and background tasks

const CACHE_NAME = 'foswvs-v1';
const STATIC_ASSETS = [
  '/',
  '/index.html',
  '/a/index.html',
  '/css/portal.css',
  '/css/admin.css',
  '/js/portal.js',
  '/js/vendor/qrcode-generator.min.js',
  '/js/vendor/jsQR.min.js',
  '/favicon.ico',
  '/manifest.json'
];

// Install: cache static assets
self.addEventListener('install', (event) => {
  event.waitUntil(
    caches.open(CACHE_NAME).then((cache) => {
      // Only cache core assets that exist; don't fail if some don't
      return Promise.allSettled(
        STATIC_ASSETS.map(url =>
          cache.add(url).catch(err => console.log(`Could not cache ${url}:`, err))
        )
      );
    }).then(() => self.skipWaiting())
  );
});

// Activate: clean up old caches
self.addEventListener('activate', (event) => {
  event.waitUntil(
    caches.keys().then((cacheNames) => {
      return Promise.all(
        cacheNames.map((cacheName) => {
          if (cacheName !== CACHE_NAME) {
            return caches.delete(cacheName);
          }
        })
      );
    }).then(() => self.clients.claim())
  );
});

// Fetch: cache-first for static assets, network-first for API/dynamic content
self.addEventListener('fetch', (event) => {
  const { request } = event;
  const url = new URL(request.url);

  // Skip cross-origin requests
  if (url.origin !== location.origin) {
    return;
  }

  // Network-first for API endpoints: try network, fallback to cache
  if (url.pathname.startsWith('/api/') || url.pathname.startsWith('/ws')) {
    event.respondWith(
      fetch(request)
        .then((response) => {
          // Cache successful GET responses for offline fallback (avoid caching POST/PUT/DELETE)
          if (response.status === 200 && request.method === 'GET') {
            const cache_copy = response.clone();
            caches.open(CACHE_NAME).then((cache) => {
              cache.put(request, cache_copy);
            });
          }
          return response;
        })
        .catch(() => {
          // Return cached version if network fails
          return caches.match(request).then((cached) => {
            if (cached) {
              return cached;
            }
            // If not in cache and network failed, return offline page
            if (request.mode === 'navigate') {
              return caches.match('/index.html');
            }
            // For non-navigation requests, return a generic offline response
            return new Response(
              JSON.stringify({ error: 'offline', message: 'Network unavailable' }),
              {
                status: 503,
                statusText: 'Service Unavailable',
                headers: new Headers({ 'Content-Type': 'application/json' })
              }
            );
          });
        })
    );
    return;
  }

  // Cache-first for static assets: check cache first, fallback to network
  if (
    request.method === 'GET' &&
    (
      url.pathname.match(/\.(js|css|png|jpg|jpeg|svg|gif|webp|ico|woff|woff2|ttf|eot)$/i) ||
      url.pathname === '/' ||
      url.pathname.endsWith('.html')
    )
  ) {
    event.respondWith(
      caches.match(request)
        .then((cached) => {
          if (cached) {
            return cached;
          }
          return fetch(request)
            .then((response) => {
              // Cache successful responses
              if (response && response.status === 200) {
                const cache_copy = response.clone();
                caches.open(CACHE_NAME).then((cache) => {
                  cache.put(request, cache_copy);
                });
              }
              return response;
            })
            .catch(() => {
              // If both cache and network fail for HTML, return offline page
              if (request.mode === 'navigate') {
                return caches.match('/index.html');
              }
              return new Response('Offline', { status: 503 });
            });
        })
    );
    return;
  }

  // Default: network-first for everything else
  event.respondWith(
    fetch(request)
      .catch(() => caches.match(request))
  );
});

// Background sync for share transactions (future enhancement)
// When the device is offline, failed share requests can be queued and
// retried when connectivity is restored
self.addEventListener('sync', (event) => {
  if (event.tag === 'sync-share-tx') {
    event.waitUntil(
      // Retry pending share transactions
      // Implementation would sync with the app's pending queue
      Promise.resolve()
    );
  }
});

// Handle messages from the app
self.addEventListener('message', (event) => {
  if (event.data && event.data.type === 'SKIP_WAITING') {
    self.skipWaiting();
  }
  if (event.data && event.data.type === 'GET_CACHE_STATUS') {
    caches.keys().then((names) => {
      event.ports[0].postMessage({
        caches: names,
        activeCacheName: CACHE_NAME
      });
    });
  }
});
