package handlers

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"log"
	"math"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/foswvs/foswvs-go/internal/auth"
	"github.com/foswvs/foswvs-go/internal/bandwidth"
	"github.com/foswvs/foswvs-go/internal/db"
	"github.com/foswvs/foswvs-go/internal/gpio"
	"github.com/foswvs/foswvs-go/internal/network"
	"github.com/foswvs/foswvs-go/internal/ws"
)

// App holds all dependencies for the HTTP handlers.
type App struct {
	Store       *db.Store
	IPT         Firewall
	Net         *network.Net
	Coinslot    CoinAcceptor
	Auth        *auth.SessionStore
	Hub         *ws.Hub
	DataDir     string
	WebDir      string
	DevMode     bool
	JWTSecret   []byte // encrypts device tokens (see reconcileDeviceToken)
	Maintenance *MaintenanceState
	Shaper      *bandwidth.Shaper

	// DHCP lease tracking to avoid unnecessary upserts
	PrevLeasesMu sync.RWMutex
	PrevLeases   map[string]network.Lease // keyed by MAC
}

// Routes builds the HTTP router.
func (a *App) Routes() http.Handler {
	mux := http.NewServeMux()

	// --- WebSocket endpoints ---
	mux.HandleFunc("/ws", a.handleClientWS)
	mux.HandleFunc("/ws/admin", a.handleAdminWS)

	// --- Client API ---
	mux.HandleFunc("/api/connect", a.handleConnect)
	mux.HandleFunc("/api/device_info", a.handleDeviceInfo)
	mux.HandleFunc("/api/data_usage", a.handleDataUsage)
	mux.HandleFunc("/api/device_token", a.handleDeviceToken)
	mux.HandleFunc("/api/topup", a.handleTopup)
	mux.HandleFunc("/api/topup_cancel", a.handleTopupCancel)
	mux.HandleFunc("/api/topup_check", a.handleTopupCheck)
	mux.HandleFunc("/api/topup_checker", a.handleTopupChecker)
	mux.HandleFunc("/api/network_status", a.handleNetworkStatus)
	mux.HandleFunc("/api/txn", a.handleDeviceTxn)
	mux.HandleFunc("/api/share", a.handleShare)
	mux.HandleFunc("/api/rates", a.handleRatesPublic)
	mux.HandleFunc("/api/maintenance", a.handleMaintenancePublic)

	// --- DHCP hook (called by dhcpd on commit) ---
	mux.HandleFunc("/api/dhcp_hook", a.handleDHCPHook)

	// --- Admin API ---
	mux.HandleFunc("/api/admin/login", a.handleAdminLogin)
	mux.HandleFunc("/api/admin/logout", a.handleAdminLogout)
	mux.HandleFunc("/api/admin/check", a.handleAdminCheck)
	mux.HandleFunc("/api/admin/password", a.handleAdminPassword)
	mux.HandleFunc("/api/admin/devices", a.requireAdmin(a.handleAdminDevices))
	mux.HandleFunc("/api/admin/device", a.requireAdmin(a.handleAdminDevice))
	mux.HandleFunc("/api/admin/txn", a.requireAdmin(a.handleAdminTxn))
	mux.HandleFunc("/api/admin/earnings", a.requireAdmin(a.handleAdminEarnings))
	mux.HandleFunc("/api/admin/rates", a.requireAdmin(a.handleAdminRates))
	mux.HandleFunc("/api/admin/gpio", a.requireAdmin(a.handleAdminGPIO))
	mux.HandleFunc("/api/admin/maintenance", a.requireAdmin(a.handleAdminMaintenance))
	mux.HandleFunc("/api/admin/traffic_control", a.requireAdmin(a.handleAdminTrafficControl))
	mux.HandleFunc("/api/admin/traffic_control/interfaces", a.requireAdmin(a.handleAdminTrafficControlInterfaces))
	mux.HandleFunc("/api/admin/system", a.requireAdmin(a.handleAdminSystem))
	mux.HandleFunc("/api/admin/network_config", a.requireAdmin(a.handleAdminNetworkConfig))
	mux.HandleFunc("/api/admin/network_interfaces", a.requireAdmin(a.handleAdminNetworkInterfaces))
	mux.HandleFunc("/api/admin/network_roles", a.requireAdmin(a.handleAdminNetworkRoles))
	mux.HandleFunc("/api/admin/hostapd_config", a.requireAdmin(a.handleAdminHostapdConfig))
	mux.HandleFunc("/api/admin/add_session", a.requireAdmin(a.handleAdminAddSession))
	mux.HandleFunc("/api/admin/del_session", a.requireAdmin(a.handleAdminDelSession))
	mux.HandleFunc("/api/admin/clear_mb", a.requireAdmin(a.handleAdminClearMB))
	mux.HandleFunc("/api/admin/block", a.requireAdmin(a.handleAdminBlock))
	mux.HandleFunc("/api/admin/dhcp_mode", a.requireAdmin(a.handleAdminDHCPMode))

	// --- Static files ---
	webRoot := a.WebDir
	if webRoot == "" {
		webRoot = filepath.Join(a.DataDir, "web")
	}
	fs := http.FileServer(http.Dir(webRoot))

	// Captive portal: unknown routes → index.html
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		path := filepath.Join(webRoot, r.URL.Path)
		if _, err := os.Stat(path); err != nil {
			http.ServeFile(w, r, filepath.Join(webRoot, "index.html"))
			return
		}
		// Block access to sensitive files
		if strings.HasSuffix(r.URL.Path, ".db") || strings.HasSuffix(r.URL.Path, ".sha256") {
			http.NotFound(w, r)
			return
		}
		fs.ServeHTTP(w, r)
	})

	return mux
}

// --- Helpers ---

func (a *App) clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func (a *App) deviceForIP(ip string) (int64, error) {
	devID, err := a.Store.GetDeviceIDByIP(ip)
	if err == nil && devID != 0 {
		return devID, nil
	}

	if !a.DevMode && err != nil {
		return 0, err
	}

	return a.Store.UpsertDevice(devMACForIP(ip), ip, devHostnameForIP(ip))
}

func devMACForIP(ip string) string {
	h := fnv.New32a()
	_, _ = h.Write([]byte(ip))
	sum := h.Sum32()
	return fmt.Sprintf("02:00:%02X:%02X:%02X:%02X", byte(sum>>24), byte(sum>>16), byte(sum>>8), byte(sum))
}

func devHostnameForIP(ip string) string {
	name := strings.NewReplacer(".", "-", ":", "-").Replace(ip)
	return "dev-" + name
}

func (a *App) deviceUsagePayload(devID int64, ip string) map[string]interface{} {
	mac, du, _ := a.Store.GetDeviceFullInfo(devID)
	return map[string]interface{}{
		"ip":       ip,
		"mac":      mac,
		"mb_limit": du.MBLimit,
		"mb_used":  du.MBUsed,
	}
}

func (a *App) networkStatus(ip string) string {
	if a.IPT.IsConnected(ip) {
		return "connected"
	}
	return "disconnected"
}

func writeJSON(w http.ResponseWriter, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

// --- Rates config ---

type RatesConfig map[int]float64

func (a *App) loadRates() RatesConfig {
	path := filepath.Join(a.DataDir, "rates.json")
	data, err := os.ReadFile(path)
	if err != nil {
		return RatesConfig{1: 24, 5: 128, 10: 1024}
	}
	var raw map[string]float64
	if err := json.Unmarshal(data, &raw); err != nil {
		return RatesConfig{1: 24, 5: 128, 10: 1024}
	}
	rates := make(RatesConfig)
	for k, v := range raw {
		n, _ := strconv.Atoi(k)
		if n > 0 {
			rates[n] = v
		}
	}
	return rates
}

func (a *App) saveRates(rates RatesConfig) error {
	path := filepath.Join(a.DataDir, "rates.json")
	raw := make(map[string]float64)
	for k, v := range rates {
		raw[strconv.Itoa(k)] = v
	}
	data, _ := json.Marshal(raw)
	return os.WriteFile(path, data, 0644)
}

// AmountToMB converts a coin count (piso) to MB using the rates config.
func (a *App) AmountToMB(peso int) float64 {
	rates := a.loadRates()
	size := 0.0

	// Sort denominations descending
	keys := make([]int, 0, len(rates))
	for k := range rates {
		keys = append(keys, k)
	}
	sort.Sort(sort.Reverse(sort.IntSlice(keys)))

	remaining := peso
	for _, amt := range keys {
		if remaining >= amt {
			base := remaining / amt
			size += float64(base) * rates[amt]
			remaining -= base * amt
		}
	}
	return size
}

// FormatMB formats MB to human-readable string.
func FormatMB(size float64) string {
	if size < 1 {
		return "0MB"
	}
	units := []string{"MB", "GB", "TB", "PB"}
	base := int(math.Floor(math.Log(size) / math.Log(1024)))
	if base >= len(units) {
		base = len(units) - 1
	}
	val := size / math.Pow(1024, float64(base))
	if val == math.Floor(val) {
		return fmt.Sprintf("%.0f%s", val, units[base])
	}
	return fmt.Sprintf("%.2f%s", val, units[base])
}

// --- Client WebSocket ---

// mintDeviceToken issues a fresh, encrypted device token for devID, logging
// (but not failing the caller) on error.
func (a *App) mintDeviceToken(devID int64) string {
	token, err := auth.NewDeviceToken(a.JWTSecret, devID, auth.DeviceTokenTTL)
	if err != nil {
		log.Printf("device token sign: %v", err)
		return ""
	}
	return token
}

// reconcileDeviceToken reunites a returning browser with its prior data
// balance after its MAC address rotates (iOS/Android randomize MAC per
// network by default, so the DHCP watcher sees what looks like a brand new
// device). clientToken is whatever the browser presented in the WebSocket
// handshake, if anything. It returns the token the browser should persist,
// or "" if the browser's existing token is still valid and unchanged (no
// need to reissue — tokens are only refreshed on top-up or a MAC change).
func (a *App) reconcileDeviceToken(devID int64, clientToken string) string {
	if clientToken != "" {
		if oldDevID, err := auth.ParseDeviceToken(a.JWTSecret, clientToken); err == nil {
			if oldDevID == devID {
				return "" // unchanged and still valid, nothing to do
			}
			// Different device than the token remembers — the MAC rotated.
			// Reunite the balance under the current device and issue a new
			// token bound to it.
			if err := a.Store.MergeDeviceSessions(oldDevID, devID); err != nil {
				log.Printf("device token merge %d->%d: %v", oldDevID, devID, err)
			}
		}
		// Invalid/expired/malformed tokens fall through to mint a fresh one.
	}

	return a.mintDeviceToken(devID)
}

func (a *App) handleClientWS(w http.ResponseWriter, r *http.Request) {
	ip := a.clientIP(r)
	client, clientToken := a.Hub.NewClientWithIP(w, r, ip)
	if client == nil {
		return
	}

	devID, _ := a.deviceForIP(ip)
	if devID == 0 {
		a.Hub.SendToClient(client, ws.MsgUnregistered, map[string]string{"message": "unknown device"})
		return
	}

	a.Hub.SetClientDeviceID(client, devID)

	if token := a.reconcileDeviceToken(devID, clientToken); token != "" {
		a.Hub.SendToClient(client, ws.MsgDeviceToken, map[string]string{"token": token})
	}

	a.Hub.SendToClient(client, ws.MsgDataUsage, a.deviceUsagePayload(devID, ip))
	a.Hub.SendToClient(client, ws.MsgNetworkStatus, map[string]string{"status": a.networkStatus(ip)})
	a.Hub.SendToClient(client, ws.MsgMaintenance, a.Maintenance.Get())
}

func (a *App) handleAdminWS(w http.ResponseWriter, r *http.Request) {
	token := auth.GetSessionToken(r)
	if !a.Auth.ValidateSession(token) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	client := a.Hub.NewAdminClient(w, r)
	if client == nil {
		return
	}

	// Push initial dashboard state so the admin panel renders from the WS
	// alone, without separate REST pulls.
	if es, err := a.Store.GetEarningsSummary(); err == nil {
		a.Hub.SendToClient(client, ws.MsgEarnings, es)
	}
	if bs, err := a.Store.GetBandwidthSummary(); err == nil {
		a.Hub.SendToClient(client, ws.MsgBandwidth, bs)
	}
	a.Hub.SendToClient(client, ws.MsgMaintenance, a.Maintenance.Get())
	if devs, err := a.Store.GetActiveDevices(); err == nil {
		a.Hub.SendToClient(client, ws.MsgActiveDevices, devs)
	}
	a.Hub.SendToClient(client, ws.MsgSystemInfo, map[string]interface{}{
		"cpu_temp": network.CPUTemp(),
		"uptime":   network.Uptime(),
	})
}

// --- Client API Handlers ---

func (a *App) handleConnect(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	ip := a.clientIP(r)
	devID, err := a.deviceForIP(ip)
	if err != nil || devID == 0 {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	mc := a.Maintenance.Get()
	if mc.Mode == MaintenanceLockdown {
		writeError(w, http.StatusServiceUnavailable, "portal is under maintenance")
		return
	}

	du, _ := a.Store.GetDataUsage(devID)
	if du.MBLimit <= du.MBUsed {
		if mc.Mode != MaintenanceFree || !a.maybeGrantMaintenanceFreeMB(devID) {
			writeError(w, http.StatusForbidden, "no data available")
			return
		}
	}

	if !a.IPT.IsConnected(ip) {
		if err := a.IPT.AddClient(ip); err != nil {
			writeError(w, http.StatusInternalServerError, "iptables error")
			return
		}
	}

	// Push updated status via WS
	a.Hub.SendToDevice(devID, ws.MsgNetworkStatus, map[string]string{"status": "connected"})
	a.Hub.SendToDevice(devID, ws.MsgDataUsage, a.deviceUsagePayload(devID, ip))
	w.WriteHeader(http.StatusOK)
}

func (a *App) handleDataUsage(w http.ResponseWriter, r *http.Request) {
	ip := a.clientIP(r)
	devID, _ := a.deviceForIP(ip)
	if devID == 0 {
		writeError(w, http.StatusUnauthorized, "unknown device")
		return
	}

	writeJSON(w, a.deviceUsagePayload(devID, ip))
}

func (a *App) handleDeviceToken(w http.ResponseWriter, r *http.Request) {
	ip := a.clientIP(r)
	devID, _ := a.deviceForIP(ip)
	if devID == 0 {
		writeError(w, http.StatusUnauthorized, "unknown device")
		return
	}

	token := a.mintDeviceToken(devID)
	if token == "" {
		writeError(w, http.StatusInternalServerError, "failed to generate token")
		return
	}

	writeJSON(w, map[string]string{"token": token})
}

func (a *App) handleTopup(w http.ResponseWriter, r *http.Request) {
	ip := a.clientIP(r)
	devID, _ := a.deviceForIP(ip)
	if devID == 0 {
		writeError(w, http.StatusUnauthorized, "unknown device")
		return
	}

	// Coin acceptor is presumed unreliable/offline for the duration of
	// either maintenance mode — don't let anyone try to feed it money.
	if a.Maintenance.Get().Mode != MaintenanceOff {
		writeError(w, http.StatusServiceUnavailable, "portal is under maintenance")
		return
	}

	mac, count, topupAt, _ := a.Store.GetDeviceTopupInfo(devID)

	// Incremental rate limiting: backoff = 5 * count (seconds), capped at 60s
	const baseBackoff = 5 * time.Second
	const maxBackoff = 60 * time.Second
	backoffDuration := time.Duration(count) * baseBackoff
	if backoffDuration > maxBackoff {
		backoffDuration = maxBackoff
	}
	nextAllowedTime := topupAt.Add(backoffDuration)

	// Check if still in backoff period
	now := time.Now()
	if now.Before(nextAllowedTime) {
		remaining := int64(nextAllowedTime.Sub(now).Seconds()) + 1
		writeError(w, http.StatusTooManyRequests, fmt.Sprintf("Please wait %d seconds before trying again.", remaining))
		return
	}

	// Reset counter if backoff period has expired
	if now.After(nextAllowedTime) && count > 0 {
		count = 0
	}

	if a.Coinslot.IsBusy() {
		writeError(w, http.StatusConflict, "coinslot is busy")
		return
	}

	if a.Coinslot.SensorRead() != 0 {
		writeError(w, http.StatusConflict, "coinslot is busy")
		return
	}

	// Start topup session
	cancelCh := make(chan struct{})

	// Store cancel channel keyed by device ID
	topupCancelsMu.Lock()
	topupCancels[devID] = cancelCh
	topupCancelsMu.Unlock()

	// Store initial topup status for polling (captive portal fallback)
	initialStatus := map[string]interface{}{
		"amt": 0,
		"mb":  0,
		"cd":  30,
	}
	topupStatusMu.Lock()
	topupStatus[devID] = initialStatus
	topupStatusMu.Unlock()

	// Notify client immediately that topup is starting
	a.Hub.SendToDevice(devID, ws.MsgTopupProgress, initialStatus)

	resultCh := a.Coinslot.RunTopup(mac, func(n int) float64 {
		return a.AmountToMB(n)
	}, cancelCh)

	// Stream results via WebSocket and update polling cache
	go func() {
		var lastResult gpio.TopupResult
		for res := range resultCh {
			lastResult = res
			// Update polling cache for captive portal
			topupStatusMu.Lock()
			topupStatus[devID] = map[string]interface{}{
				"amt": res.Amount,
				"mb":  res.MB,
				"cd":  res.Countdown,
			}
			topupStatusMu.Unlock()

			a.Hub.SendToDevice(devID, ws.MsgTopupProgress, res)
		}

		// Cleanup
		topupCancelsMu.Lock()
		delete(topupCancels, devID)
		topupCancelsMu.Unlock()

		topupStatusMu.Lock()
		delete(topupStatus, devID)
		topupStatusMu.Unlock()

		if lastResult.Cancelled && lastResult.Amount == 0 {
			// Topup was cancelled with no coins inserted; notify client to reset UI
			du, _ := a.Store.GetDataUsage(devID)
			a.Hub.SendToDevice(devID, ws.MsgTopupDone, map[string]interface{}{
				"amt":      0,
				"mb":       0,
				"mb_limit": du.MBLimit,
				"mb_used":  du.MBUsed,
			})
			return
		}

		if lastResult.Amount == 0 {
			a.Store.IncrTopupCount(devID)
			// Notify client to reset UI (timeout with no coins inserted)
			du, _ := a.Store.GetDataUsage(devID)
			a.Hub.SendToDevice(devID, ws.MsgTopupDone, map[string]interface{}{
				"amt":      0,
				"mb":       0,
				"mb_limit": du.MBLimit,
				"mb_used":  du.MBUsed,
			})
			return
		}

		// Record session - re-verify device at credit time to handle reconnects
		// Use current device at IP to ensure the device making the request gets credit
		creditDevID, _ := a.deviceForIP(ip)
		if creditDevID == 0 {
			log.Printf("topup: device at %s no longer available for credit", ip)
			return
		}
		if creditDevID != devID {
			log.Printf("topup: device changed from %d to %d at IP %s", devID, creditDevID, ip)
		}

		mb := a.AmountToMB(lastResult.Amount)
		a.Store.AddSession(creditDevID, float64(lastResult.Amount), mb)
		a.Store.ResetTopupCount(creditDevID)

		// Open firewall (only if not already connected to avoid redundant iptables entries)
		if !a.IPT.IsConnected(ip) {
			a.IPT.AddClient(ip)
		}

		// Notify client
		du, _ := a.Store.GetDataUsage(creditDevID)
		a.Hub.SendToDevice(devID, ws.MsgTopupDone, map[string]interface{}{
			"amt":      lastResult.Amount,
			"mb":       mb,
			"mb_limit": du.MBLimit,
			"mb_used":  du.MBUsed,
		})

		// Refresh the device token on every top-up, per the 30-day expiry
		// policy — keeps a regularly-paying device's token alive long term.
		if token := a.mintDeviceToken(creditDevID); token != "" {
			a.Hub.SendToDevice(devID, ws.MsgDeviceToken, map[string]string{"token": token})
		}

		// Notify admin
		if a.Hub.HasAdminClients() {
			a.pushAdminEarnings()
		}
	}()

	w.WriteHeader(http.StatusAccepted)
	writeJSON(w, map[string]string{"status": "topup_started"})
}

func (a *App) handleTopupCancel(w http.ResponseWriter, r *http.Request) {
	ip := a.clientIP(r)
	devID, _ := a.deviceForIP(ip)
	if devID == 0 {
		return
	}

	topupCancelsMu.Lock()
	ch, ok := topupCancels[devID]
	if ok {
		close(ch)
		delete(topupCancels, devID)
	}
	topupCancelsMu.Unlock()

	// Cancel without coin inserted = empty tap attempt, apply rate-limiting
	if ok {
		a.Store.IncrTopupCount(devID)
	}

	// If user has sufficient data, connect them
	du, _ := a.Store.GetDataUsage(devID)
	if du.MBLimit > du.MBUsed {
		if !a.IPT.IsConnected(ip) {
			if err := a.IPT.AddClient(ip); err == nil {
				a.Hub.SendToDevice(devID, ws.MsgNetworkStatus, map[string]string{"status": "connected"})
			}
		}
	}

	w.WriteHeader(http.StatusOK)
}

func (a *App) handleTopupCheck(w http.ResponseWriter, r *http.Request) {
	// Legacy endpoint for non-WS clients — returns 204 if no active topup
	ip := a.clientIP(r)
	devID, _ := a.deviceForIP(ip)
	if devID == 0 {
		http.Error(w, "", http.StatusUnauthorized)
		return
	}

	topupCancelsMu.Lock()
	_, active := topupCancels[devID]
	topupCancelsMu.Unlock()

	if !active {
		w.WriteHeader(http.StatusNoContent)
		return
	}

	// Active topup in progress — WS handles streaming, but return 200 for legacy
	w.WriteHeader(http.StatusOK)
}

func (a *App) handleTopupChecker(w http.ResponseWriter, r *http.Request) {
	// Poll endpoint for checking topup progress (like PHP version)
	// Used by captive portal popup which may not support WebSocket
	ip := a.clientIP(r)
	devID, _ := a.deviceForIP(ip)
	if devID == 0 {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}

	topupStatusMu.RLock()
	status, exists := topupStatus[devID]
	topupStatusMu.RUnlock()

	if !exists {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}

	writeJSON(w, status)
}

func (a *App) handleNetworkStatus(w http.ResponseWriter, r *http.Request) {
	ip := a.clientIP(r)
	writeJSON(w, map[string]string{"status": a.networkStatus(ip)})
}

func (a *App) handleDeviceInfo(w http.ResponseWriter, r *http.Request) {
	ip := a.clientIP(r)
	devID, _ := a.deviceForIP(ip)
	if devID == 0 {
		writeError(w, http.StatusUnauthorized, "unknown device")
		return
	}

	// Read device token cookie sent by client (for MAC address rotation handling)
	clientTokenCookie, err := r.Cookie("pisowifi_device_token")
	clientToken := ""
	if err == nil {
		clientToken = clientTokenCookie.Value
	}

	mac, du, _ := a.Store.GetDeviceFullInfo(devID)

	// Reconcile device token: handles MAC address changes (iOS/Android randomization)
	// Returns "" if token is still valid, or a new token if device changed
	token := a.reconcileDeviceToken(devID, clientToken)
	if token == "" {
		// Token was valid and unchanged, no new token needed
		// But we should still send it back for UI consistency
		token = clientToken
	}

	// Set device token as cookie (fresh generation if missing or invalid)
	if token != "" {
		http.SetCookie(w, &http.Cookie{
			Name:     "pisowifi_device_token",
			Value:    token,
			Path:     "/",
			MaxAge:   30 * 24 * 60 * 60, // 30 days
			HttpOnly: false,
			SameSite: http.SameSiteLaxMode,
			Secure:   r.TLS != nil,
		})
	}

	writeJSON(w, map[string]interface{}{
		"ip":       ip,
		"mac":      mac,
		"mb_limit": du.MBLimit,
		"mb_used":  du.MBUsed,
		"status":   a.networkStatus(ip),
		"token":    token,
	})
}

func (a *App) handleDeviceTxn(w http.ResponseWriter, r *http.Request) {
	ip := a.clientIP(r)
	devID, _ := a.deviceForIP(ip)
	if devID == 0 {
		return
	}
	sessions, _ := a.Store.GetDeviceSessions(devID)
	if sessions == nil {
		sessions = []db.Session{}
	}
	writeJSON(w, sessions)
}

func (a *App) handleShare(w http.ResponseWriter, r *http.Request) {
	ip := a.clientIP(r)
	devID, _ := a.deviceForIP(ip)
	if devID == 0 {
		writeError(w, http.StatusUnauthorized, "unknown device")
		return
	}

	switch r.Method {
	case http.MethodPost:
		// Generate share code
		code := strings.ToUpper(fmt.Sprintf("%05X", db.NowMS()%0xFFFFF))
		a.Store.AddShareTx(devID, code)
		w.Write([]byte(code))

	case http.MethodPut:
		// Redeem share code
		body := make([]byte, 256)
		n, _ := r.Body.Read(body)
		parts := strings.SplitN(string(body[:n]), "|", 2)
		if len(parts) != 2 {
			writeError(w, http.StatusBadRequest, "invalid format")
			return
		}

		code := strings.ToUpper(strings.TrimSpace(parts[0]))
		size, err := strconv.Atoi(strings.TrimSpace(parts[1]))
		if err != nil || size < 1 || size > 99999 {
			writeError(w, http.StatusBadRequest, "enter 1 to 99999")
			return
		}

		targetDevID, _ := a.Store.GetShareTxDeviceID(code)
		if targetDevID == 0 {
			writeError(w, http.StatusBadRequest, "code expired")
			return
		}

		if devID == targetDevID {
			writeError(w, http.StatusBadRequest, "not allowed using own code")
			return
		}

		du, _ := a.Store.GetDataUsage(devID)
		free := du.MBLimit - du.MBUsed
		if free < float64(size) {
			writeError(w, http.StatusBadRequest, "insufficient data")
			return
		}

		// Deduct from sender
		a.Store.UpdateMBUsed(devID, float64(size))
		// Credit to receiver
		a.Store.AddSession(targetDevID, 0, float64(size))

		// Notify receiver via WS
		receiverIP, _ := a.Store.GetDeviceIP(targetDevID)
		a.Hub.SendToDevice(targetDevID, ws.MsgShareReceived, map[string]string{
			"message": "You Received " + FormatMB(float64(size)),
		})
		a.Hub.SendToDevice(targetDevID, ws.MsgDataUsage, a.deviceUsagePayload(targetDevID, receiverIP))

		// Connect receiver (only if not already connected)
		if !a.IPT.IsConnected(receiverIP) {
			a.IPT.AddClient(receiverIP)
		}

		w.Write([]byte("Successfully Shared " + FormatMB(float64(size))))

	case http.MethodGet:
		// Check for incoming shares
		mb, _ := a.Store.GetRecentSessionMB(devID, 3)
		if mb == 0 {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		w.Write([]byte("You Received " + FormatMB(mb)))

	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (a *App) handleRatesPublic(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, a.loadRates())
}

func (a *App) handleMaintenancePublic(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, a.Maintenance.Get())
}

// --- Admin Handlers ---

func (a *App) requireAdmin(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token := auth.GetSessionToken(r)
		if !a.Auth.ValidateSession(token) {
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		next(w, r)
	}
}

func (a *App) handleAdminLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	body := make([]byte, 512)
	n, _ := r.Body.Read(body)

	var req struct {
		Password string `json:"password"`
	}
	if err := json.Unmarshal(body[:n], &req); err != nil {
		// Try base64 fallback (legacy compat)
		decoded, err := base64.StdEncoding.DecodeString(string(body[:n]))
		if err != nil {
			writeError(w, http.StatusBadRequest, "bad request")
			return
		}
		req.Password = string(decoded)
	}

	if len(req.Password) < 3 {
		writeError(w, http.StatusBadRequest, "password too short")
		return
	}

	if !auth.PasswordFileExists(a.DataDir) {
		if err := auth.InitPassword(a.DataDir, req.Password); err != nil {
			writeError(w, http.StatusInternalServerError, "init error")
			return
		}
		token, _ := a.Auth.CreateSession()
		auth.SetSessionCookie(w, token)
		writeJSON(w, map[string]string{"status": "init"})
		return
	}

	storedHash, err := auth.ReadPasswordHash(a.DataDir)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "read error")
		return
	}

	if auth.HashPassword(req.Password) != storedHash {
		writeError(w, http.StatusUnauthorized, "invalid password")
		return
	}

	token, _ := a.Auth.CreateSession()
	auth.SetSessionCookie(w, token)
	writeJSON(w, map[string]string{"status": "ok"})
}

func (a *App) handleAdminLogout(w http.ResponseWriter, r *http.Request) {
	token := auth.GetSessionToken(r)
	a.Auth.DestroySession(token)
	auth.ClearSessionCookie(w)
	w.WriteHeader(http.StatusOK)
}

func (a *App) handleAdminCheck(w http.ResponseWriter, r *http.Request) {
	if !auth.PasswordFileExists(a.DataDir) {
		writeJSON(w, map[string]string{"status": "init_required"})
		return
	}
	token := auth.GetSessionToken(r)
	if a.Auth.ValidateSession(token) {
		writeJSON(w, map[string]string{"status": "ok"})
	} else {
		writeError(w, http.StatusUnauthorized, "session expired")
	}
}

func (a *App) handleAdminPassword(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPut {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	token := auth.GetSessionToken(r)
	if !a.Auth.ValidateSession(token) {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	body := make([]byte, 512)
	n, _ := r.Body.Read(body)
	var req struct {
		Password string `json:"password"`
	}
	json.Unmarshal(body[:n], &req)

	if len(req.Password) < 3 {
		writeError(w, http.StatusBadRequest, "password too short")
		return
	}

	hash := auth.HashPassword(req.Password)
	if err := auth.WritePasswordHash(a.DataDir, hash); err != nil {
		writeError(w, http.StatusInternalServerError, "write error")
		return
	}
	w.WriteHeader(http.StatusOK)
}

func (a *App) handleAdminDevices(w http.ResponseWriter, r *http.Request) {
	filter := r.URL.Query().Get("filter")
	var devs []db.Device
	var err error

	switch filter {
	case "active":
		devs, err = a.Store.GetActiveDevices()
	case "recent":
		devs, err = a.Store.GetRecentDevices()
	case "restricted":
		devs, err = a.Store.GetRestrictedDevices()
	default:
		devs, err = a.Store.GetAllDevices()
	}

	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if devs == nil {
		devs = []db.Device{}
	}
	writeJSON(w, devs)
}

func (a *App) handleAdminDevice(w http.ResponseWriter, r *http.Request) {
	mac := r.URL.Query().Get("mac")
	if mac == "" {
		writeError(w, http.StatusBadRequest, "mac required")
		return
	}

	devID, err := a.Store.GetDeviceIDByMAC(mac)
	if err != nil {
		log.Printf("get device id by mac: %v", err)
		writeError(w, http.StatusInternalServerError, "failed to lookup device")
		return
	}
	if devID == 0 {
		writeError(w, http.StatusNotFound, "device not found")
		return
	}

	dev, err := a.Store.GetDeviceInfo(devID)
	if err != nil {
		log.Printf("get device info: %v", err)
		writeError(w, http.StatusInternalServerError, "failed to retrieve device info")
		return
	}
	du, err := a.Store.GetDataUsage(devID)
	if err != nil {
		log.Printf("get data usage: %v", err)
		writeError(w, http.StatusInternalServerError, "failed to retrieve data usage")
		return
	}
	activeAt, err := a.Store.GetActiveAt(devID)
	if err != nil {
		log.Printf("get active at: %v", err)
		writeError(w, http.StatusInternalServerError, "failed to retrieve activity timestamp")
		return
	}
	ip, err := a.Store.GetDeviceIP(devID)
	if err != nil {
		log.Printf("get device ip: %v", err)
		writeError(w, http.StatusInternalServerError, "failed to retrieve device IP")
		return
	}
	connected := a.IPT.IsConnected(ip)

	writeJSON(w, map[string]interface{}{
		"mac":       dev.MAC,
		"ip":        dev.IP,
		"host":      dev.Hostname,
		"mb_limit":  du.MBLimit,
		"mb_used":   du.MBUsed,
		"active_at": activeAt,
		"connected": connected,
	})
}

func (a *App) handleAdminTxn(w http.ResponseWriter, r *http.Request) {
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	mac := r.URL.Query().Get("mac")

	if limit <= 0 {
		limit = 25
	}

	if mac != "" {
		devID, _ := a.Store.GetDeviceIDByMAC(mac)
		if devID == 0 {
			writeError(w, http.StatusNotFound, "device not found")
			return
		}
		sessions, err := a.Store.GetDeviceSessions(devID)
		if err != nil {
			log.Printf("get device sessions: %v", err)
			writeError(w, http.StatusInternalServerError, "failed to retrieve sessions")
			return
		}
		if sessions == nil {
			sessions = []db.Session{}
		}
		writeJSON(w, sessions)
		return
	}

	txns, err := a.Store.GetAllTransactions(offset, limit)
	if err != nil {
		log.Printf("get all transactions: %v", err)
		writeError(w, http.StatusInternalServerError, "failed to retrieve transactions")
		return
	}
	if txns == nil {
		txns = []db.Transaction{}
	}
	writeJSON(w, txns)
}

func (a *App) handleAdminEarnings(w http.ResponseWriter, r *http.Request) {
	es, err := a.Store.GetEarningsSummary()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, es)
}

func (a *App) handleAdminRates(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, a.loadRates())

	case http.MethodPut:
		body := make([]byte, 4096)
		n, _ := r.Body.Read(body)

		var entries []string
		if err := json.Unmarshal(body[:n], &entries); err != nil {
			writeError(w, http.StatusBadRequest, "invalid json")
			return
		}

		rates := make(RatesConfig)
		for _, e := range entries {
			parts := strings.SplitN(e, ":", 2)
			if len(parts) != 2 {
				continue
			}
			amt, _ := strconv.Atoi(parts[0])
			mb, _ := strconv.Atoi(parts[1])
			if amt > 0 && mb > 0 {
				rates[amt] = float64(mb)
			}
		}

		if len(rates) == 0 {
			writeError(w, http.StatusBadRequest, "at least one rate is required")
			return
		}

		a.saveRates(rates)
		writeJSON(w, map[string]string{"status": "saved"})

	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (a *App) handleAdminGPIO(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, a.Coinslot.PinConfig())

	case http.MethodPut:
		body := make([]byte, 512)
		n, _ := r.Body.Read(body)

		var req struct {
			SlotPin    int `json:"slot_pin"`
			SensorPin  int `json:"sensor_pin"`
			DebounceMS int `json:"debounce_ms"`
		}
		if err := json.Unmarshal(body[:n], &req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid json")
			return
		}
		if req.SlotPin <= 0 || req.SensorPin <= 0 {
			writeError(w, http.StatusBadRequest, "pins must be positive")
			return
		}
		if req.SlotPin == req.SensorPin {
			writeError(w, http.StatusBadRequest, "slot and sensor pins must differ")
			return
		}
		if req.DebounceMS <= 0 || req.DebounceMS > 1000 {
			writeError(w, http.StatusBadRequest, "debounce delay must be 1-1000ms")
			return
		}

		if err := a.Coinslot.Reconfigure(req.SlotPin, req.SensorPin); err != nil {
			writeError(w, http.StatusConflict, err.Error())
			return
		}

		a.Coinslot.SetDebounceDelay(time.Duration(req.DebounceMS) * time.Millisecond)

		cfg := gpio.Config{SlotPin: req.SlotPin, SensorPin: req.SensorPin, DebounceMS: req.DebounceMS}
		if err := gpio.SaveConfig(a.DataDir, cfg); err != nil {
			log.Printf("gpio config save: %v", err)
		}

		// Return the saved config so frontend can verify
		writeJSON(w, a.Coinslot.PinConfig())

	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (a *App) handleAdminMaintenance(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, a.Maintenance.Get())

	case http.MethodPut:
		body := make([]byte, 512)
		n, _ := r.Body.Read(body)

		var req struct {
			Mode   string  `json:"mode"`
			FreeMB float64 `json:"free_mb"`
		}
		if err := json.Unmarshal(body[:n], &req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid json")
			return
		}
		if req.Mode != MaintenanceOff && req.Mode != MaintenanceLockdown && req.Mode != MaintenanceFree {
			writeError(w, http.StatusBadRequest, "invalid mode")
			return
		}
		if req.Mode == MaintenanceFree && req.FreeMB <= 0 {
			writeError(w, http.StatusBadRequest, "free_mb must be positive")
			return
		}

		cfg, err := a.Maintenance.Set(req.Mode, req.FreeMB)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "save failed")
			return
		}

		// Lockdown takes effect immediately — kick everyone currently
		// connected rather than waiting for them to exhaust their balance
		// or for the next DHCP watcher tick.
		if cfg.Mode == MaintenanceLockdown {
			devs, _ := a.Store.GetAllDevices()
			for _, d := range devs {
				if a.IPT.IsConnected(d.IP) {
					a.IPT.RemoveClient(d.IP)
					a.Hub.SendToDevice(d.ID, ws.MsgNetworkStatus, map[string]string{"status": "disconnected"})
					a.Hub.SendToDevice(d.ID, ws.MsgAlert, map[string]string{"message": "portal is under maintenance"})
				}
			}
		}

		a.Hub.BroadcastAll(ws.MsgMaintenance, cfg)
		writeJSON(w, cfg)

	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (a *App) handleAdminTrafficControl(w http.ResponseWriter, r *http.Request) {
	if a.Shaper == nil {
		writeError(w, http.StatusInternalServerError, "traffic control not initialized")
		return
	}

	switch r.Method {
	case http.MethodGet:
		tcCfg := a.Shaper.GetTrafficControlConfig()
		writeJSON(w, map[string]interface{}{
			"maximum_dynamic_mbps": tcCfg.MaximumDynamicMbps,
			"total_bandwidth_mbps": tcCfg.TotalBandwidthMbps,
			"qdisc_type":           tcCfg.QdiscType,
			"overhead_bytes":       tcCfg.OverheadBytes,
			"enable_ingress":       tcCfg.EnableIngress,
			"interface_name":       tcCfg.InterfaceName,
			"active_users":         a.Shaper.GetActiveIPCount(),
			"effective_per_ip":     a.Shaper.GetEffectivePerIPLimit(tcCfg.TotalBandwidthMbps),
		})

	case http.MethodPut:
		body := make([]byte, 1024)
		n, _ := r.Body.Read(body)

		var req struct {
			MaximumDynamicMbps int    `json:"maximum_dynamic_mbps"`
			TotalBandwidthMbps int    `json:"total_bandwidth_mbps"`
			QdiscType          string `json:"qdisc_type"`
			OverheadBytes      int    `json:"overhead_bytes"`
			EnableIngress      *bool  `json:"enable_ingress"`
			InterfaceName      string `json:"interface_name"`
		}
		if err := json.Unmarshal(body[:n], &req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid json")
			return
		}

		if req.MaximumDynamicMbps <= 0 || req.MaximumDynamicMbps > 1000 {
			writeError(w, http.StatusBadRequest, "maximum_dynamic_mbps must be 1-1000")
			return
		}
		if req.TotalBandwidthMbps <= 0 || req.TotalBandwidthMbps > 10000 {
			writeError(w, http.StatusBadRequest, "total_bandwidth_mbps must be 1-10000")
			return
		}

		// Validate interface if provided
		if req.InterfaceName != "" {
			if err := bandwidth.ValidateInterface(req.InterfaceName); err != nil {
				writeError(w, http.StatusBadRequest, fmt.Sprintf("invalid interface: %v", err))
				return
			}
		} else {
			// Auto-select best available interface
			req.InterfaceName = bandwidth.SelectInterfaceForTrafficControl("")
		}

		// Determine enable_ingress: default to true for CAKE if not provided
		enableIngress := false
		if req.EnableIngress != nil {
			enableIngress = *req.EnableIngress
		} else if req.QdiscType == "cake" || req.QdiscType == "" {
			enableIngress = true // Default to true for CAKE
		}

		cfg := bandwidth.TrafficControlConfig{
			MaximumDynamicMbps: req.MaximumDynamicMbps,
			TotalBandwidthMbps: req.TotalBandwidthMbps,
			QdiscType:          req.QdiscType,
			OverheadBytes:      req.OverheadBytes,
			EnableIngress:      enableIngress,
			InterfaceName:      req.InterfaceName,
		}

		if cfg.QdiscType == "" {
			cfg.QdiscType = "cake"
		}
		if cfg.OverheadBytes <= 0 || cfg.OverheadBytes > 256 {
			cfg.OverheadBytes = 38
		}

		if err := a.Shaper.SetTrafficControlConfig(cfg, a.DataDir); err != nil {
			log.Printf("traffic control config error: %v", err)
			writeError(w, http.StatusInternalServerError, fmt.Sprintf("traffic control error: %v", err))
			return
		}

		writeJSON(w, map[string]string{"status": "saved", "interface": cfg.InterfaceName})

	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// handleAdminTrafficControlInterfaces lists available network interfaces for traffic control
func (a *App) handleAdminTrafficControlInterfaces(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if a.Shaper == nil {
		writeError(w, http.StatusInternalServerError, "traffic control not initialized")
		return
	}

	// Detect available interfaces
	ifaces := bandwidth.DetectNetworkInterfaces()

	// Get current selected interface from config
	cfg := a.Shaper.GetTrafficControlConfig()
	currentIface := cfg.InterfaceName

	// Enhance response with selection info
	type InterfaceInfo struct {
		Name     string `json:"name"`
		Status   string `json:"status"`
		IP       string `json:"ip"`
		MAC      string `json:"mac"`
		Selected bool   `json:"selected"`
	}

	response := make([]InterfaceInfo, len(ifaces))
	for i, iface := range ifaces {
		response[i] = InterfaceInfo{
			Name:     iface.Name,
			Status:   iface.Status,
			IP:       iface.IP,
			MAC:      iface.MAC,
			Selected: iface.Name == currentIface,
		}
	}

	writeJSON(w, response)
}

func (a *App) handleAdminSystem(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, map[string]interface{}{
		"cpu_temp":   network.CPUTemp(),
		"uptime":     network.Uptime(),
		"interfaces": a.Net.Interfaces(),
	})
}

func (a *App) handleAdminAddSession(w http.ResponseWriter, r *http.Request) {
	mac := r.URL.Query().Get("mac")
	limitStr := r.URL.Query().Get("limit")

	if mac == "" || limitStr == "" {
		writeError(w, http.StatusBadRequest, "mac and limit required")
		return
	}

	limit, _ := strconv.Atoi(limitStr)
	if limit <= 0 {
		writeError(w, http.StatusBadRequest, "invalid limit")
		return
	}

	devID, err := a.Store.GetDeviceIDByMAC(mac)
	if err != nil {
		log.Printf("get device id by mac: %v", err)
		writeError(w, http.StatusInternalServerError, "failed to lookup device")
		return
	}
	if devID == 0 {
		writeError(w, http.StatusNotFound, "device not found")
		return
	}

	sid, err := a.Store.AddSession(devID, 0, float64(limit))
	if err != nil {
		log.Printf("add session: %v", err)
		writeError(w, http.StatusInternalServerError, "failed to add session")
		return
	}

	du, err := a.Store.GetDataUsage(devID)
	if err != nil {
		log.Printf("get data usage: %v", err)
		writeError(w, http.StatusInternalServerError, "failed to retrieve data usage")
		return
	}

	if du.MBLimit > du.MBUsed {
		ip, err := a.Store.GetDeviceIP(devID)
		if err != nil {
			log.Printf("get device ip: %v", err)
			writeError(w, http.StatusInternalServerError, "failed to retrieve device IP")
			return
		}
		if ip != "" {
			if !a.IPT.IsConnected(ip) {
				if err := a.IPT.AddClient(ip); err != nil {
					log.Printf("add client: %v", err)
					// Log but don't fail - the session was added successfully
				}
			}
		}
	}

	writeJSON(w, map[string]interface{}{
		"sid":      sid,
		"mb_limit": du.MBLimit,
		"mb_used":  du.MBUsed,
	})
}

func (a *App) handleAdminDelSession(w http.ResponseWriter, r *http.Request) {
	sidStr := r.URL.Query().Get("sid")
	sid, err := strconv.ParseInt(sidStr, 10, 64)
	if err != nil || sid <= 0 {
		writeError(w, http.StatusBadRequest, "invalid session id")
		return
	}
	if err := a.Store.RemoveSession(sid); err != nil {
		log.Printf("remove session: %v", err)
		writeError(w, http.StatusInternalServerError, "failed to remove session")
		return
	}
	w.WriteHeader(http.StatusOK)
}

func (a *App) handleAdminClearMB(w http.ResponseWriter, r *http.Request) {
	mac := r.URL.Query().Get("mac")
	if mac == "" {
		writeError(w, http.StatusBadRequest, "mac required")
		return
	}
	devID, err := a.Store.GetDeviceIDByMAC(mac)
	if err != nil {
		log.Printf("get device id by mac: %v", err)
		writeError(w, http.StatusInternalServerError, "failed to lookup device")
		return
	}
	if devID == 0 {
		writeError(w, http.StatusNotFound, "device not found")
		return
	}
	if err := a.Store.ClearSessions(devID); err != nil {
		log.Printf("clear sessions: %v", err)
		writeError(w, http.StatusInternalServerError, "failed to clear sessions")
		return
	}
	w.WriteHeader(http.StatusOK)
}

func (a *App) handleAdminBlock(w http.ResponseWriter, r *http.Request) {
	mac := r.URL.Query().Get("mac")
	if mac == "" {
		writeError(w, http.StatusBadRequest, "mac required")
		return
	}
	devID, err := a.Store.GetDeviceIDByMAC(mac)
	if err != nil {
		log.Printf("get device id by mac: %v", err)
		writeError(w, http.StatusInternalServerError, "failed to lookup device")
		return
	}
	if devID == 0 {
		writeError(w, http.StatusNotFound, "device not found")
		return
	}

	if err := a.Store.ForceExhaustData(devID); err != nil {
		log.Printf("force exhaust data: %v", err)
		writeError(w, http.StatusInternalServerError, "failed to block device")
		return
	}

	ip, err := a.Store.GetDeviceIP(devID)
	if err != nil {
		log.Printf("get device ip: %v", err)
		writeError(w, http.StatusInternalServerError, "failed to retrieve device IP")
		return
	}

	if ip != "" {
		if err := a.IPT.RemoveClient(ip); err != nil {
			log.Printf("remove client: %v", err)
			// Log but don't fail - the device data was already exhausted
		}
		a.Hub.SendToDevice(devID, ws.MsgNetworkStatus, map[string]string{"status": "disconnected"})
	}

	w.WriteHeader(http.StatusOK)
}

// pushAdminEarnings sends current earnings to all admin clients.
func (a *App) pushAdminEarnings() {
	es, err := a.Store.GetEarningsSummary()
	if err != nil {
		return
	}
	a.Hub.SendToAdmins(ws.MsgEarnings, es)
}

// pushAdminBandwidth sends current bandwidth usage to all admin clients.
// Called from the usage poller (which is what actually moves mb_used) —
// unlike earnings, bandwidth changes continuously, not just on top-up, so
// it needs its own periodic refresh point rather than a topup-triggered one.
func (a *App) pushAdminBandwidth() {
	bs, err := a.Store.GetBandwidthSummary()
	if err != nil {
		return
	}
	a.Hub.SendToAdmins(ws.MsgBandwidth, bs)
}

func (a *App) handleAdminNetworkConfig(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		cfg, err := a.Net.GetNetworkConfig()
		if err != nil {
			log.Printf("get network config: %v", err)
			writeError(w, http.StatusInternalServerError, "unable to read network config")
			return
		}
		writeJSON(w, cfg)

	case http.MethodPut:
		body := make([]byte, 4096)
		n, _ := r.Body.Read(body)

		var req network.NetworkConfig
		if err := json.Unmarshal(body[:n], &req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid json")
			return
		}

		// Apply role-based configuration if roles are specified
		if len(req.Roles) > 0 {
			if err := a.Net.ApplyRoleConfiguration(&req); err != nil {
				writeError(w, http.StatusBadRequest, err.Error())
				return
			}
		}

		if err := a.Net.SetNetworkConfig(&req, a.DataDir); err != nil {
			log.Printf("set network config: %v", err)
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}

		writeJSON(w, map[string]string{"status": "saved"})

	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (a *App) handleAdminNetworkInterfaces(w http.ResponseWriter, r *http.Request) {
	defer func() {
		if rec := recover(); rec != nil {
			log.Printf("panic in handleAdminNetworkInterfaces: %v", rec)
			writeError(w, http.StatusInternalServerError, "internal server error")
		}
	}()

	switch r.Method {
	case http.MethodGet:
		// Discover all available interfaces
		log.Printf("discovering network interfaces")
		interfaces, err := a.Net.DiscoverInterfaces()
		if err != nil {
			log.Printf("discover interfaces error: %v", err)
			writeError(w, http.StatusInternalServerError, fmt.Sprintf("unable to discover interfaces: %v", err))
			return
		}
		log.Printf("discovered %d interfaces", len(interfaces))

		// Detect WAN interface (has default route)
		wanIface := a.Net.DetectWANInterface()
		log.Printf("detected WAN interface: %s", wanIface)

		// Get current roles from config
		cfg, err := a.Net.GetNetworkConfig()
		roleMap := make(map[string]string)
		if err != nil {
			log.Printf("get network config: %v (continuing anyway)", err)
		}
		if cfg != nil && cfg.Roles != nil {
			for _, role := range cfg.Roles {
				roleMap[role.InterfaceName] = role.Role
			}
		}

		// Enhance interfaces with detection
		for i := range interfaces {
			iface := &interfaces[i]

			// Detect WAN
			if iface.Name == wanIface {
				iface.IsWAN = true
				iface.Role = "wan"
			}

			// Detect private IP range
			iface.IsPrivateIP = network.IsPrivateIP(iface.IP)

			// Detect hostapd candidate
			iface.IsHostapdCandidate = network.IsHostapdCandidate(iface.Name)

			// Override role from config if set
			if configRole, ok := roleMap[iface.Name]; ok {
				iface.Role = configRole
			}
		}

		writeJSON(w, interfaces)

	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (a *App) handleAdminNetworkRoles(w http.ResponseWriter, r *http.Request) {
	defer func() {
		if rec := recover(); rec != nil {
			log.Printf("panic in handleAdminNetworkRoles: %v", rec)
			writeError(w, http.StatusInternalServerError, "internal server error")
		}
	}()

	switch r.Method {
	case http.MethodGet:
		// Get current role assignments
		log.Printf("fetching network roles")
		cfg, err := a.Net.GetNetworkConfig()
		if err != nil {
			log.Printf("get network config error: %v", err)
			writeError(w, http.StatusInternalServerError, fmt.Sprintf("unable to read config: %v", err))
			return
		}

		type roleResponse struct {
			Roles []network.InterfaceRole `json:"roles"`
		}

		roles := cfg.Roles
		if roles == nil {
			roles = []network.InterfaceRole{}
		}
		log.Printf("returning %d roles", len(roles))
		resp := roleResponse{Roles: roles}
		writeJSON(w, resp)

	case http.MethodPut:
		body := make([]byte, 4096)
		n, _ := r.Body.Read(body)

		var req struct {
			Roles []network.InterfaceRole `json:"roles"`
		}
		if err := json.Unmarshal(body[:n], &req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid json")
			return
		}

		// Get current config
		cfg, err := a.Net.GetNetworkConfig()
		if err != nil {
			writeError(w, http.StatusInternalServerError, "unable to read config")
			return
		}

		// Update roles and apply configuration
		cfg.Roles = req.Roles

		// Validate and apply role configuration
		if len(cfg.Roles) > 0 {
			if err := a.Net.ApplyRoleConfiguration(cfg); err != nil {
				writeError(w, http.StatusBadRequest, err.Error())
				return
			}
		}

		// Save updated configuration
		if err := a.Net.SetNetworkConfig(cfg, a.DataDir); err != nil {
			log.Printf("set network config with roles: %v", err)
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}

		writeJSON(w, map[string]string{"status": "roles updated"})

	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (a *App) handleAdminHostapdConfig(w http.ResponseWriter, r *http.Request) {
	defer func() {
		if rec := recover(); rec != nil {
			log.Printf("panic in handleAdminHostapdConfig: %v", rec)
			writeError(w, http.StatusInternalServerError, "internal server error")
		}
	}()

	switch r.Method {
	case http.MethodGet:
		// Get hostapd configuration
		log.Printf("fetching hostapd config")
		confPath := filepath.Join(a.DataDir, "conf", "hostapd.conf")
		cfg, err := network.ReadHostapdConfig(confPath)
		if err != nil {
			log.Printf("read hostapd config: %v", err)
			writeError(w, http.StatusInternalServerError, fmt.Sprintf("unable to read hostapd config: %v", err))
			return
		}
		writeJSON(w, cfg)

	case http.MethodPut:
		body := make([]byte, 2048)
		n, _ := r.Body.Read(body)

		var req network.HostapdConfig
		if err := json.Unmarshal(body[:n], &req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid json")
			return
		}

		// Validate
		if req.SSID == "" {
			writeError(w, http.StatusBadRequest, "SSID cannot be empty")
			return
		}
		if req.Channel < 1 || req.Channel > 165 {
			writeError(w, http.StatusBadRequest, "channel must be 1-165")
			return
		}
		if req.Mode == "" {
			req.Mode = "g" // Default
		}

		confPath := filepath.Join(a.DataDir, "conf", "hostapd.conf")
		if err := network.WriteHostapdConfig(confPath, &req); err != nil {
			log.Printf("write hostapd config: %v", err)
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}

		// Return updated config
		confPath = filepath.Join(a.DataDir, "conf", "hostapd.conf")
		cfg, _ := network.ReadHostapdConfig(confPath)
		writeJSON(w, cfg)

	case http.MethodPatch:
		// Toggle hostapd enabled/disabled state
		body := make([]byte, 256)
		n, _ := r.Body.Read(body)

		var req struct {
			Enabled bool `json:"enabled"`
		}
		if err := json.Unmarshal(body[:n], &req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid json")
			return
		}

		// Control hostapd service
		if req.Enabled {
			if err := exec.Command("systemctl", "enable", "--now", "hostapd").Run(); err != nil {
				log.Printf("enable hostapd: %v", err)
				writeError(w, http.StatusInternalServerError, "failed to enable hostapd")
				return
			}
		} else {
			if err := exec.Command("systemctl", "disable", "--now", "hostapd").Run(); err != nil {
				log.Printf("disable hostapd: %v", err)
				writeError(w, http.StatusInternalServerError, "failed to disable hostapd")
				return
			}
		}

		// Return current config
		confPath := filepath.Join(a.DataDir, "conf", "hostapd.conf")
		cfg, _ := network.ReadHostapdConfig(confPath)
		writeJSON(w, cfg)

	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// normalizeMACAddress parses and canonicalizes a MAC address, handling non-zero-padded hex.
// Converts formats like "8:7C:39:CF:B6:3F" to "08:7c:39:cf:b6:3f".
func normalizeMACAddress(macStr string) (string, error) {
	// Replace dashes with colons for uniform parsing
	normalized := strings.ReplaceAll(macStr, "-", ":")

	// Split by colon to get octets
	parts := strings.Split(normalized, ":")
	if len(parts) != 6 {
		return "", fmt.Errorf("invalid MAC format")
	}

	// Pad each octet to 2 characters with leading zeros
	for i, part := range parts {
		if len(part) > 2 {
			return "", fmt.Errorf("invalid octet: %s", part)
		}
		parts[i] = strings.ToUpper(fmt.Sprintf("%02s", part))
	}

	paddedMAC := strings.Join(parts, ":")

	// Validate with net.ParseMAC
	_, err := net.ParseMAC(paddedMAC)
	if err != nil {
		return "", err
	}

	return paddedMAC, nil
}

// isLocalhostRequest checks if the request originates from localhost.
// Only accepts 127.0.0.1, ::1, and localhost.
func isLocalhostRequest(r *http.Request) bool {
	host := r.RemoteAddr
	// RemoteAddr contains IP:port, extract just the IP
	if idx := strings.LastIndex(host, ":"); idx != -1 {
		host = host[:idx]
	}
	// Remove IPv6 brackets if present
	host = strings.TrimPrefix(host, "[")
	host = strings.TrimSuffix(host, "]")

	return host == "127.0.0.1" || host == "::1" || host == "localhost"
}

// handleDHCPHook processes lease commits from the dhcpd hook.
// SECURITY: Requires localhost origin AND valid authentication token.
// Receives: IP, MAC, hostname, token as query parameters.
// Response: 200 OK on success, 403 Forbidden if not from localhost or invalid token.
func (a *App) handleDHCPHook(w http.ResponseWriter, r *http.Request) {
	// Security Layer 1: Verify request is from localhost
	if !isLocalhostRequest(r) {
		log.Printf("SECURITY: DHCP hook rejected from unauthorized host %s", r.RemoteAddr)
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Security Layer 2: Verify authentication token
	token := r.URL.Query().Get("token")
	expectedToken := a.Net.GetDHCPHookToken()
	if token != expectedToken {
		log.Printf("SECURITY: DHCP hook rejected - invalid token from %s", r.RemoteAddr)
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	// Parse hook parameters from query string
	// dhcpd hook calls with: /api/dhcp_hook?ip=10.0.0.5&mac=AA:BB:CC:DD:EE:FF&hostname=mydevice&token=...
	ip := r.URL.Query().Get("ip")
	macStr := r.URL.Query().Get("mac")
	hostname := r.URL.Query().Get("hostname")

	// Validate required fields
	if ip == "" || macStr == "" {
		http.Error(w, "missing ip or mac", http.StatusBadRequest)
		return
	}

	// Parse and canonicalize MAC address (handles non-zero-padded hex formats)
	// net.ParseMAC requires zero-padded format, so preprocess non-padded inputs
	mac, err := normalizeMACAddress(macStr)
	if err != nil {
		http.Error(w, "invalid mac address", http.StatusBadRequest)
		return
	}

	// Submit the lease to the network service
	lease := network.Lease{
		IP:       ip,
		MAC:      mac,
		Hostname: hostname,
	}

	if err := a.Net.SubmitHookLease(lease); err != nil {
		log.Printf("dhcp hook: failed to submit lease %s: %v", mac, err)
		http.Error(w, "channel full", http.StatusServiceUnavailable)
		return
	}

	// Update device in database to refresh updated_at timestamp
	if _, err := a.Store.UpsertDevice(mac, ip, hostname); err != nil {
		log.Printf("dhcp hook: failed to upsert device %s: %v", mac, err)
		// Don't fail the response — the lease was submitted successfully
	}

	w.WriteHeader(http.StatusOK)
	w.Write([]byte("OK"))
}

// handleAdminDHCPMode returns or changes the DHCP lease monitoring mode.
// GET: returns { "mode": "hook" | "file_poll" }
// POST: sets mode; request body: { "mode": "hook" | "file_poll" }
func (a *App) handleAdminDHCPMode(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		mode := a.Net.GetLeaseMonitorMode()
		writeJSON(w, map[string]string{"mode": string(mode)})

	case http.MethodPost:
		var req struct {
			Mode string `json:"mode"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid request", http.StatusBadRequest)
			return
		}

		// Validate mode
		if req.Mode != string(network.HookMode) && req.Mode != string(network.FilePollMode) {
			http.Error(w, "invalid mode (must be 'hook' or 'file_poll')", http.StatusBadRequest)
			return
		}

		a.Net.SetLeaseMonitorMode(network.LeaseMonitorMode(req.Mode))
		log.Printf("admin: DHCP monitoring mode changed to %s", req.Mode)

		writeJSON(w, map[string]string{"mode": req.Mode, "status": "changed"})

	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// --- Topup cancel registry + status cache ---
var (
	topupCancels   = make(map[int64]chan struct{})
	topupCancelsMu = &sync.Mutex{}

	// topupStatus tracks current topup progress for polling endpoint
	topupStatus   = make(map[int64]map[string]interface{})
	topupStatusMu = &sync.RWMutex{}
)
