// PisoWiFi captive portal — shared helpers
const Portal = (() => {
  const peso = new Intl.NumberFormat('en-PH', { style: 'currency', currency: 'PHP' });

  function formatMB(size) {
    size = parseFloat(size) || 0;
    if (size < 1) return '0MB';
    const base = Math.floor(Math.log(size) / Math.log(1024));
    const units = ['MB', 'GB', 'TB', 'PB'];
    const val = size / Math.pow(1024, base);
    return (Number.isInteger(val) ? val : val.toFixed(2)) + units[Math.min(base, units.length - 1)];
  }

  function toast(msg, ok, opts) {
    const d = document.createElement('div');
    d.className = 'toast ' + (ok ? 'ok' : 'err') + (opts && opts.noNavOffset ? ' no-nav-offset' : '');
    d.textContent = msg;
    document.body.appendChild(d);
    setTimeout(() => d.remove(), 3000);
  }

  function initNav(activeHref) {
    document.querySelectorAll('.nav-item').forEach(a => {
      a.classList.toggle('active', a.getAttribute('href') === activeHref);
    });
  }

  // Device identity survives MAC address changes (iOS/Android randomize MAC
  // per network by default) — the server reunites the balance server-side
  // whenever this token shows up attached to a different-looking device.
  // Stored in cookie for better captive portal support.
  const DEVICE_TOKEN_KEY = 'pisowifi_device_token';
  const COOKIE_PATH = '/';
  const COOKIE_MAX_AGE = 30 * 24 * 60 * 60; // 30 days

  function getDeviceToken() {
    const cookies = document.cookie.split('; ');
    for (let cookie of cookies) {
      const [key, value] = cookie.split('=');
      if (decodeURIComponent(key) === DEVICE_TOKEN_KEY) {
        return decodeURIComponent(value);
      }
    }
    return '';
  }

  function setDeviceToken(token) {
    if (!token) return;
    const secure = location.protocol === 'https:' ? '; Secure' : '';
    document.cookie = `${DEVICE_TOKEN_KEY}=${encodeURIComponent(token)}; Path=${COOKIE_PATH}; Max-Age=${COOKIE_MAX_AGE}; SameSite=Lax${secure}`;
  }

  return { peso, formatMB, toast, initNav, getDeviceToken, setDeviceToken };
})();
