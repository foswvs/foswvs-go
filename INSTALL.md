# Raspberry Pi install guide

Full walkthrough for getting foswvs-go running on real hardware, from a
freshly flashed SD card to your first captive portal login. Written for
Raspberry Pi OS on the "legacy" network stack (`dhcpcd`, not
NetworkManager) — matches Raspberry Pi OS Bullseye/Buster, i.e. anything
from before the Bookworm release. If `nmcli` is your normal way of
managing interfaces, you're on Bookworm and step 3 will need adapting.

Tested target: `armv7l` (32-bit, e.g. Pi 2/3/4 running 32-bit Raspberry Pi
OS). If you're on 64-bit Raspberry Pi OS, use `make build-arm64` wherever
this doc says `build-arm`.

## What you need

- A Raspberry Pi with onboard WiFi (or a USB WiFi adapter that supports AP
  mode) — this becomes the "PisoWiFi" access point
- A second network connection for internet uplink (Ethernet is easiest)
- A coin acceptor wired to GPIO — defaults are pin 2 (coin pulse) and pin
  17 (sensor enable); if your wiring differs, you can remap both from the
  admin dashboard's Settings → Coinslot GPIO Pins once it's running, no
  rebuild needed
- Your own machine, for cross-compiling and `scp`/`ssh` access to the Pi

## 1. Flash the OS and enable SSH

Use Raspberry Pi Imager. Before writing, open the advanced options (gear
icon) and set: hostname, enable SSH with a password or your public key,
and configure WiFi *only if* you're using the Pi's own WiFi for your
internet uplink instead of Ethernet (don't confuse this with the AP the
Pi will host — that's separate and configured in step 4).

Boot the Pi, then from your machine:

```bash
ssh pi@<hostname-or-ip>
```

## 2. Install system packages

```bash
sudo apt update
sudo apt install -y hostapd isc-dhcp-server iptables openssl
```

`iptables` is normally preinstalled, but the explicit install is harmless
if it's already there.

Raspberry Pi OS images ship `hostapd` masked by default. Unmask it now so
`systemctl enable` in a later step actually works:

```bash
sudo systemctl unmask hostapd
```

## 3. Give wlan0 a static IP

The whole app assumes the Pi is reachable at `10.0.0.1` on the AP
interface — it's baked into `internal/iptables`'s subnet validation, the
DNAT target, and the `conf/dhcpd.conf` template. Don't change it unless
you're prepared to change all three.

Edit `/etc/dhcpcd.conf` and append:

```
interface wlan0
static ip_address=10.0.0.1/24
nohook wpa_supplicant
```

`nohook wpa_supplicant` stops dhcpcd from trying to *join* a network on
wlan0 — it's about to become an access point instead, and wpa_supplicant
fighting hostapd for the same interface is the single most common cause
of "the AP won't stay up."

If you're using wlan0 for both internet uplink *and* the AP (i.e. no
spare interface/dongle), stop here and use a second interface instead —
one radio can't be a client and an AP at the same time.

Restart networking to apply:

```bash
sudo systemctl restart dhcpcd
```

## 4. Configure the access point (hostapd)

```bash
git clone https://github.com/foswvs/foswvs-go.git
cd foswvs-go
sudo cp conf/hostapd.conf /etc/hostapd/hostapd.conf
```

Open `/etc/hostapd/hostapd.conf` if you want to change the SSID (default
`PisoWiFi`) or channel. It's an **open network by design** — anyone can
join the WiFi for free, and the captive portal gates actual internet
access behind payment. That's the product, not a misconfiguration; only
add `wpa_passphrase`/`wpa=2` lines if you specifically want a WiFi
password on top of that.

Point the hostapd service at this config file:

```bash
echo 'DAEMON_CONF="/etc/hostapd/hostapd.conf"' | sudo tee -a /etc/default/hostapd
sudo systemctl enable hostapd
```

Don't start it yet — do that in step 8, after the DHCP server is also
configured, so both come up together.

## 5. Configure the DHCP server

```bash
sudo cp conf/dhcpd.conf /etc/dhcp/dhcpd.conf
```

Tell isc-dhcp-server which interface to serve on:

```bash
sudo sed -i 's/^INTERFACESv4=.*/INTERFACESv4="wlan0"/' /etc/default/isc-dhcp-server
```

If that line doesn't exist in the file, add it manually:
`INTERFACESv4="wlan0"`.

```bash
sudo systemctl enable isc-dhcp-server
```

## 6. Enable IP forwarding

```bash
echo 'net.ipv4.ip_forward=1' | sudo tee /etc/sysctl.d/99-foswvs.conf
sudo sysctl --system
```

(`scripts/iptables-base.sh`, deployed in the next step, also sets this
live on every boot as a safety net — but persisting it here means it's
correct even before that script has run.)

## 7. Cross-compile and deploy the app

From your own machine (not the Pi), inside the repo:

```bash
make deploy PI_HOST=pi@<hostname-or-ip>
```

This cross-compiles for `armv7l` (`GOOS=linux GOARCH=arm GOARM=7`), then
copies over:
- the binary → `/home/pi/foswvs-go/foswvs-go`
- `web/static/` → `/home/pi/foswvs-go/web/`
- `scripts/iptables-base.sh` → `/usr/local/bin/foswvs-iptables-base.sh`
  (the base NAT/redirect/forward rules — the Go binary only ever adds
  per-client rules as people pay for data, it doesn't set up the
  underlying policy, so this script is what makes the captive redirect
  and internet NAT work at all)
- `foswvs-go.service` → `/lib/systemd/system/`

...then enables and starts the `foswvs-go` service.

Prefer to build directly on the Pi instead? Skip cross-compiling:

```bash
# on the Pi, needs Go 1.22+
git clone https://github.com/foswvs/foswvs-go.git && cd foswvs-go
sudo make install
```

## 8. Bring it all up

```bash
sudo systemctl start isc-dhcp-server
sudo systemctl start hostapd
sudo systemctl status hostapd isc-dhcp-server foswvs-go --no-pager
```

All three should show `active (running)`. If `foswvs-go` isn't already
running from the deploy step:

```bash
sudo systemctl restart foswvs-go
sudo journalctl -u foswvs-go -f
```

You should see it log the HTTP listener coming up, the DHCP watcher, and
the usage poller starting.

## 9. First connect

1. Join the `PisoWiFi` network from a phone or laptop
2. It should auto-open the captive portal; if not, browse to
   `http://10.0.0.1/`
3. Go to `http://10.0.0.1/a/` for the admin dashboard — the **first**
   password you enter there becomes the admin password (no default to
   guess or change later)

If nothing loads at all, see Troubleshooting below before assuming the Go
app is at fault — most first-boot issues are the AP/DHCP layer, not
foswvs-go itself.

## 10. (Optional) Enable HTTPS

Plain HTTP works for everything except one thing: the QR-code camera
scanner in the Share tab. Browsers only grant camera access on a secure
context (HTTPS, or localhost), so without TLS, "Scan QR Code" will always
fall back to manual code entry. Everything else — payments, data usage,
the admin dashboard — works fine over HTTP.

Generate a self-signed cert (swap in the Pi's real LAN IP if it's not
10.0.0.1 from another interface's perspective — but for wlan0 clients,
10.0.0.1 is correct):

```bash
sudo mkdir -p /home/pi/foswvs-go/ssl
sudo openssl req -x509 -newkey rsa:2048 -sha256 -days 825 -nodes \
  -keyout /home/pi/foswvs-go/ssl/foswvs.key \
  -out /home/pi/foswvs-go/ssl/foswvs.crt \
  -subj "/CN=10.0.0.1" \
  -addext "subjectAltName=DNS:localhost,IP:127.0.0.1,IP:10.0.0.1"
```

Edit `/lib/systemd/system/foswvs-go.service` and uncomment/add to
`ExecStart` (the file has the exact flags in a comment already):

```
-tls-addr :443 -tls-cert /home/pi/foswvs-go/ssl/foswvs.crt -tls-key /home/pi/foswvs-go/ssl/foswvs.key
```

Then:

```bash
sudo systemctl daemon-reload
sudo systemctl restart foswvs-go
```

Since it's self-signed, connecting clients will see a browser warning —
tapping through it ("visit this site anyway") is enough to unlock camera
access; the cert doesn't need to be from a trusted CA for that.

## Troubleshooting

**`systemctl start hostapd` fails / exits immediately**
Almost always wlan0 is still held by `wpa_supplicant`. Confirm step 3's
`nohook wpa_supplicant` is in `/etc/dhcpcd.conf` and that you restarted
dhcpcd. Check `sudo journalctl -u hostapd -n 50` for the actual reason.

**AP shows up but clients can't get an IP**
`sudo journalctl -u isc-dhcp-server -n 50`. Usually `INTERFACESv4` in
`/etc/default/isc-dhcp-server` doesn't say `wlan0`, or wlan0 doesn't have
its static IP yet (check `ip addr show wlan0` — should show
`10.0.0.1/24`).

**Clients get an IP but never see the captive portal**
The redirect rules didn't apply. Check:
```bash
sudo iptables -t nat -L PREROUTING -n
```
You should see a DNAT rule to `10.0.0.1` for ports 80/443. If it's
missing, run `sudo /usr/local/bin/foswvs-iptables-base.sh` manually and
read its output — it'll error loudly if something's wrong rather than
failing silently (`set -euo pipefail`).

**Paid clients can't reach the actual internet**
Check `sudo iptables -t nat -L POSTROUTING -n` for a MASQUERADE rule, and
confirm `cat /proc/sys/net/ipv4/ip_forward` returns `1`. Also confirm
your uplink interface (Ethernet, presumably) actually has internet — the
Pi itself needs a route out.

**Coin acceptor does nothing**
Check the wiring against whatever pins are configured — default is slot
pin 2, sensor pin 17. If your acceptor is wired to different GPIOs,
remap them from the admin dashboard's Settings tab rather than editing
code; it's saved to `gpio.json` in the data directory and takes effect
immediately (as long as no topup is in progress when you save).

**Changed something and want to start iptables clean**
Reboot, or manually reset before re-running the script:
```bash
sudo iptables -F && sudo iptables -t nat -F && sudo iptables -P FORWARD ACCEPT
sudo /usr/local/bin/foswvs-iptables-base.sh
```

## Updating later

```bash
make deploy PI_HOST=pi@<hostname-or-ip>
```

Re-running `deploy` rebuilds, re-copies everything, and restarts the
service. Your data (`devices`/`session`/`sharetx` tables, rates, GPIO
config, device tokens) lives in `/home/pi/foswvs-go/foswvs.db` and
sibling files — untouched by redeploys.
