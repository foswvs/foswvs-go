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
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/foswvs/foswvs-go/internal/auth"
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
}

// Routes builds the HTTP router.
func (a *App) Routes() http.Handler {
	mux := http.NewServeMux()

	// --- WebSocket endpoints ---
	mux.HandleFunc("/ws", a.handleClientWS)
	mux.HandleFunc("/ws/admin", a.handleAdminWS)

	// --- Client API ---
	mux.HandleFunc("/api/connect", a.handleConnect)
	mux.HandleFunc("/api/data_usage", a.handleDataUsage)
	mux.HandleFunc("/api/topup", a.handleTopup)
	mux.HandleFunc("/api/topup_cancel", a.handleTopupCancel)
	mux.HandleFunc("/api/topup_check", a.handleTopupCheck)
	mux.HandleFunc("/api/network_status", a.handleNetworkStatus)
	mux.HandleFunc("/api/txn", a.handleDeviceTxn)
	mux.HandleFunc("/api/share", a.handleShare)
	mux.HandleFunc("/api/rates", a.handleRatesPublic)

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
	mux.HandleFunc("/api/admin/system", a.requireAdmin(a.handleAdminSystem))
	mux.HandleFunc("/api/admin/add_session", a.requireAdmin(a.handleAdminAddSession))
	mux.HandleFunc("/api/admin/del_session", a.requireAdmin(a.handleAdminDelSession))
	mux.HandleFunc("/api/admin/clear_mb", a.requireAdmin(a.handleAdminClearMB))
	mux.HandleFunc("/api/admin/block", a.requireAdmin(a.handleAdminBlock))

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
	if err != nil || devID != 0 || !a.DevMode {
		return devID, err
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
	mac, _ := a.Store.GetDeviceMAC(devID)
	du, _ := a.Store.GetDataUsage(devID)
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

	if err := a.IPT.AddClient(ip); err != nil {
		writeError(w, http.StatusInternalServerError, "iptables error")
		return
	}

	// Push updated status via WS
	a.Hub.SendToIP(ip, ws.MsgNetworkStatus, map[string]string{"status": "connected"})
	a.Hub.SendToIP(ip, ws.MsgDataUsage, a.deviceUsagePayload(devID, ip))
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

	mac, _ := a.Store.GetDeviceMAC(devID)

	// Rate limit
	count, _ := a.Store.GetTopupCount(devID)
	if count > 2 {
		writeError(w, http.StatusTooManyRequests, "too many attempts")
		return
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

	resultCh := a.Coinslot.RunTopup(mac, func(n int) float64 {
		return a.AmountToMB(n)
	}, cancelCh)

	// Stream results via WebSocket
	go func() {
		var lastResult gpio.TopupResult
		for res := range resultCh {
			lastResult = res
			a.Hub.SendToIP(ip, ws.MsgTopupProgress, res)
		}

		// Cleanup cancel channel
		topupCancelsMu.Lock()
		delete(topupCancels, devID)
		topupCancelsMu.Unlock()

		if lastResult.Cancelled && lastResult.Amount == 0 {
			return
		}

		if lastResult.Amount == 0 {
			a.Store.IncrTopupCount(devID)
			return
		}

		// Record session
		mb := a.AmountToMB(lastResult.Amount)
		a.Store.AddSession(devID, float64(lastResult.Amount), mb)
		a.Store.ResetTopupCount(devID)

		// Open firewall
		a.IPT.AddClient(ip)

		// Notify client
		du, _ := a.Store.GetDataUsage(devID)
		a.Hub.SendToIP(ip, ws.MsgTopupDone, map[string]interface{}{
			"amt":      lastResult.Amount,
			"mb":       mb,
			"mb_limit": du.MBLimit,
			"mb_used":  du.MBUsed,
		})

		// Refresh the device token on every top-up, per the 30-day expiry
		// policy — keeps a regularly-paying device's token alive long term.
		if token := a.mintDeviceToken(devID); token != "" {
			a.Hub.SendToIP(ip, ws.MsgDeviceToken, map[string]string{"token": token})
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

func (a *App) handleNetworkStatus(w http.ResponseWriter, r *http.Request) {
	ip := a.clientIP(r)
	writeJSON(w, map[string]string{"status": a.networkStatus(ip)})
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
		a.Hub.SendToIP(receiverIP, ws.MsgShareReceived, map[string]string{
			"message": "You Received " + FormatMB(float64(size)),
		})
		a.Hub.SendToIP(receiverIP, ws.MsgDataUsage, a.deviceUsagePayload(targetDevID, receiverIP))

		// Connect receiver
		a.IPT.AddClient(receiverIP)

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

	devID, _ := a.Store.GetDeviceIDByMAC(mac)
	if devID == 0 {
		writeError(w, http.StatusNotFound, "device not found")
		return
	}

	dev, _ := a.Store.GetDeviceInfo(devID)
	du, _ := a.Store.GetDataUsage(devID)
	activeAt, _ := a.Store.GetActiveAt(devID)
	ip, _ := a.Store.GetDeviceIP(devID)
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
		sessions, _ := a.Store.GetDeviceSessions(devID)
		if sessions == nil {
			sessions = []db.Session{}
		}
		writeJSON(w, sessions)
		return
	}

	txns, _ := a.Store.GetAllTransactions(offset, limit)
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
			writeError(w, http.StatusBadRequest, "no valid rates")
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
			SlotPin   int `json:"slot_pin"`
			SensorPin int `json:"sensor_pin"`
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

		if err := a.Coinslot.Reconfigure(req.SlotPin, req.SensorPin); err != nil {
			writeError(w, http.StatusConflict, err.Error())
			return
		}

		cfg := gpio.Config{SlotPin: req.SlotPin, SensorPin: req.SensorPin}
		if err := gpio.SaveConfig(a.DataDir, cfg); err != nil {
			log.Printf("gpio config save: %v", err)
		}

		writeJSON(w, map[string]string{"status": "saved"})

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
					a.Hub.SendToIP(d.IP, ws.MsgNetworkStatus, map[string]string{"status": "disconnected"})
					a.Hub.SendToIP(d.IP, ws.MsgAlert, map[string]string{"message": "portal is under maintenance"})
				}
			}
		}

		a.Hub.BroadcastAll(ws.MsgMaintenance, cfg)
		writeJSON(w, cfg)

	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
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

	devID, _ := a.Store.GetDeviceIDByMAC(mac)
	if devID == 0 {
		writeError(w, http.StatusNotFound, "device not found")
		return
	}

	sid, _ := a.Store.AddSession(devID, 0, float64(limit))

	du, _ := a.Store.GetDataUsage(devID)
	if du.MBLimit > du.MBUsed {
		ip, _ := a.Store.GetDeviceIP(devID)
		a.IPT.AddClient(ip)
	}

	writeJSON(w, map[string]interface{}{
		"sid":      sid,
		"mb_limit": du.MBLimit,
		"mb_used":  du.MBUsed,
	})
}

func (a *App) handleAdminDelSession(w http.ResponseWriter, r *http.Request) {
	sidStr := r.URL.Query().Get("sid")
	sid, _ := strconv.ParseInt(sidStr, 10, 64)
	if sid <= 0 {
		writeError(w, http.StatusBadRequest, "invalid session id")
		return
	}
	a.Store.RemoveSession(sid)
	w.WriteHeader(http.StatusOK)
}

func (a *App) handleAdminClearMB(w http.ResponseWriter, r *http.Request) {
	mac := r.URL.Query().Get("mac")
	if mac == "" {
		writeError(w, http.StatusBadRequest, "mac required")
		return
	}
	devID, _ := a.Store.GetDeviceIDByMAC(mac)
	if devID == 0 {
		writeError(w, http.StatusNotFound, "device not found")
		return
	}
	a.Store.ClearSessions(devID)
	w.WriteHeader(http.StatusOK)
}

func (a *App) handleAdminBlock(w http.ResponseWriter, r *http.Request) {
	mac := r.URL.Query().Get("mac")
	if mac == "" {
		writeError(w, http.StatusBadRequest, "mac required")
		return
	}
	devID, _ := a.Store.GetDeviceIDByMAC(mac)
	if devID == 0 {
		writeError(w, http.StatusNotFound, "device not found")
		return
	}

	a.Store.ForceExhaustData(devID)
	ip, _ := a.Store.GetDeviceIP(devID)
	a.IPT.RemoveClient(ip)

	a.Hub.SendToIP(ip, ws.MsgNetworkStatus, map[string]string{"status": "disconnected"})
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

// --- Topup cancel registry ---
var (
	topupCancels   = make(map[int64]chan struct{})
	topupCancelsMu = &sync.Mutex{}
)
