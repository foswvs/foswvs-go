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
  const DEVICE_TOKEN_KEY = 'pisowifi_device_token';

  function getDeviceToken() {
    try { return localStorage.getItem(DEVICE_TOKEN_KEY) || ''; }
    catch (e) { return ''; }
  }

  function setDeviceToken(token) {
    try { localStorage.setItem(DEVICE_TOKEN_KEY, token); }
    catch (e) { /* localStorage unavailable (private mode, etc.) — non-fatal */ }
  }

  return { peso, formatMB, toast, initNav, getDeviceToken, setDeviceToken };
})();
