package network

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// LeaseMonitorMode determines how DHCP leases are discovered.
type LeaseMonitorMode string

const (
	HookMode      LeaseMonitorMode = "hook"       // Receive updates via dhcpd hook
	FilePollMode  LeaseMonitorMode = "file_poll"  // Poll dhcpd.leases file
	DefaultMode   LeaseMonitorMode = HookMode
)

// Net provides network utility functions.
type Net struct {
	Iface string

	// Lease monitoring
	monitorMode        LeaseMonitorMode
	leaseMu            sync.RWMutex
	leasesCacheMap     map[string]Lease // keyed by MAC

	// DHCP lease file polling (used in file_poll mode)
	leasesCachePath    string
	leasesCacheModTime time.Time
	leasesCacheSize    int64

	// Hook mode configuration
	hookCallbackChan   chan Lease // Receives leases from dhcpd hook
	hookToken          string      // Random token for hook authentication
	hookTokenMu        sync.RWMutex
}

func New(iface string) *Net {
	return &Net{
		Iface:            iface,
		monitorMode:      DefaultMode,
		leasesCacheMap:   make(map[string]Lease),
		hookCallbackChan: make(chan Lease, 100), // Buffered for hook deliveries
	}
}

// SetLeaseMonitorMode sets the mechanism to use for lease monitoring.
// Default is HookMode; set to FilePollMode to poll dhcpd.leases instead.
func (n *Net) SetLeaseMonitorMode(mode LeaseMonitorMode) {
	if mode == FilePollMode || mode == HookMode {
		n.monitorMode = mode
	}
}

// GetLeaseMonitorMode returns the current lease monitoring mode.
func (n *Net) GetLeaseMonitorMode() LeaseMonitorMode {
	return n.monitorMode
}

// GenerateDHCPHookToken creates a new random authentication token for the DHCP hook.
// Called on startup; regenerated on every service restart for defense-in-depth.
// Returns the generated token (32 hex characters).
func (n *Net) GenerateDHCPHookToken() (string, error) {
	// Generate 16 random bytes (32 hex characters when encoded)
	token := make([]byte, 16)
	if _, err := rand.Read(token); err != nil {
		return "", fmt.Errorf("generate token: %w", err)
	}

	tokenStr := hex.EncodeToString(token)

	n.hookTokenMu.Lock()
	n.hookToken = tokenStr
	n.hookTokenMu.Unlock()

	log.Printf("dhcp: generated new hook authentication token")
	return tokenStr, nil
}

// GetDHCPHookToken returns the current hook authentication token.
func (n *Net) GetDHCPHookToken() string {
	n.hookTokenMu.RLock()
	defer n.hookTokenMu.RUnlock()
	return n.hookToken
}

// WriteDHCPHookToken writes the token to a file for the hook script to read.
// File: {dataDir}/.dhcp_hook_token (mode 0600 for security)
func (n *Net) WriteDHCPHookToken(dataDir string) error {
	tokenFile := filepath.Join(dataDir, ".dhcp_hook_token")
	token := n.GetDHCPHookToken()

	if err := os.WriteFile(tokenFile, []byte(token), 0600); err != nil {
		return fmt.Errorf("write token file: %w", err)
	}

	log.Printf("dhcp: wrote hook token to %s", tokenFile)
	return nil
}

// Interfaces returns non-loopback network interface names.
func (n *Net) Interfaces() []string {
	entries, err := os.ReadDir("/sys/class/net")
	if err != nil {
		return nil
	}
	var ifaces []string
	for _, e := range entries {
		if e.Name() != "lo" {
			ifaces = append(ifaces, e.Name())
		}
	}
	return ifaces
}

// ARPList returns IPs in the ARP table matching 10.0.x.x.
func (n *Net) ARPList() []string {
	data, err := os.ReadFile("/proc/net/arp")
	if err != nil {
		return nil
	}

	var ips []string
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 4 {
			continue
		}
		ip := fields[0]
		if strings.HasPrefix(ip, "10.0.") {
			ips = append(ips, ip)
		}
	}
	return ips
}

// MACForIP returns the MAC address for a given IP from the ARP table.
func (n *Net) MACForIP(ip string) string {
	data, err := os.ReadFile("/proc/net/arp")
	if err != nil {
		return ""
	}

	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 4 && fields[0] == ip {
			return strings.ToUpper(fields[3])
		}
	}
	return ""
}

// DHCPLeases parses DHCP lease info. Returns slice of (mac, ip, hostname).
type Lease struct {
	MAC      string
	IP       string
	Hostname string
}

// DHCPLeases returns cached DHCP leases discovered via the configured monitoring mode.
// In hook mode: returns leases received from dhcpd hook callbacks (async updates).
// In file_poll mode: parses the dhcpd.leases file incrementally.
// Falls back to ARP-based discovery if neither method yields leases.
func (n *Net) DHCPLeases() []Lease {
	n.leaseMu.RLock()
	defer n.leaseMu.RUnlock()

	if len(n.leasesCacheMap) > 0 {
		return n.mapToSlice()
	}

	// Fallback to ARP-based discovery if cache is empty
	return n.arpBasedLeases()
}

// PollDHCPLeasesFile polls the dhcpd.leases file for updates (when not in hook mode).
// This is typically called from a background goroutine on a regular interval.
// Returns any new or updated leases discovered since the last poll.
func (n *Net) PollDHCPLeasesFile() []Lease {
	// Try to read from common lease file locations
	paths := []string{
		"/var/lib/dhcp/dhcpd.leases",
		"/var/lib/dhcpd/dhcpd.leases",
	}

	for _, p := range paths {
		if leases := n.parseDHCPLeasesIncremental(p); len(leases) > 0 {
			return leases
		}
	}

	return nil
}

// parseDHCPLeasesIncremental only parses newly appended entries since last read.
// Thread-safe; used by PollDHCPLeasesFile in file_poll mode.
func (n *Net) parseDHCPLeasesIncremental(path string) []Lease {
	info, err := os.Stat(path)
	if err != nil {
		return nil
	}

	modTime := info.ModTime()
	currentSize := info.Size()

	// File hasn't changed since last check; return cached leases
	if path == n.leasesCachePath && modTime == n.leasesCacheModTime && currentSize == n.leasesCacheSize {
		n.leaseMu.RLock()
		defer n.leaseMu.RUnlock()
		return n.mapToSlice()
	}

	// File changed; read from last known position (or from start if new file)
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}

	var startPos int64
	if path == n.leasesCachePath && currentSize > n.leasesCacheSize {
		// Only parse newly appended data
		startPos = n.leasesCacheSize
	}

	// Parse full file on first read or if file shrank (rotated)
	newLeases := parseDHCPLeasesFromBytes(data[startPos:])

	// Update cache: merge new leases into the map
	n.leaseMu.Lock()
	for _, lease := range newLeases {
		if lease.MAC != "" && lease.IP != "" {
			n.leasesCacheMap[lease.MAC] = lease
		}
	}

	// Update cache metadata
	n.leasesCachePath = path
	n.leasesCacheModTime = modTime
	n.leasesCacheSize = currentSize
	n.leaseMu.Unlock()

	return n.mapToSlice()
}

// mapToSlice converts the in-memory lease map to a slice.
// Assumes caller holds leaseMu for reading.
func (n *Net) mapToSlice() []Lease {
	leases := make([]Lease, 0, len(n.leasesCacheMap))
	for _, lease := range n.leasesCacheMap {
		leases = append(leases, lease)
	}
	return leases
}

func (n *Net) arpBasedLeases() []Lease {
	data, err := os.ReadFile("/proc/net/arp")
	if err != nil {
		return nil
	}

	var leases []Lease
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 4 || fields[0] == "IP" {
			continue
		}
		ip := fields[0]
		mac := strings.ToUpper(fields[3])
		if strings.HasPrefix(ip, "10.0.") && mac != "00:00:00:00:00:00" {
			hostname := lookupHostname(ip)
			leases = append(leases, Lease{MAC: mac, IP: ip, Hostname: hostname})
		}
	}
	return leases
}

func lookupHostname(ip string) string {
	out, err := exec.Command("nmblookup", "-A", ip).Output()
	if err != nil {
		return "-NA-"
	}
	for _, line := range strings.Split(string(out), "\n") {
		if strings.Contains(line, "<00>") && !strings.Contains(line, "<GROUP>") {
			fields := strings.Fields(line)
			if len(fields) > 0 {
				return fields[0]
			}
		}
	}
	return "-NA-"
}

// HandleDHCPHook is called by the dhcpd hook when a lease is committed.
// It updates the local lease cache with the new or updated lease.
// Logs a warning if called when not in hook mode.
func (n *Net) HandleDHCPHook(lease Lease) {
	if n.monitorMode != HookMode {
		log.Printf("warning: DHCP hook received but monitor mode is %s (not hook)", n.monitorMode)
	}

	if lease.MAC == "" || lease.IP == "" {
		log.Printf("warning: DHCP hook received incomplete lease: MAC=%s IP=%s", lease.MAC, lease.IP)
		return
	}

	if lease.Hostname == "" {
		lease.Hostname = "-NA-"
	}

	n.leaseMu.Lock()
	n.leasesCacheMap[lease.MAC] = lease
	n.leaseMu.Unlock()

	log.Printf("dhcp hook: registered %s (%s, %s)", lease.MAC, lease.IP, lease.Hostname)
}

// StartHookListener starts a goroutine that processes incoming lease updates
// from the dhcpd hook callback channel. Run this in hook mode to enable
// asynchronous lease updates. Blocks until ctx is cancelled.
func (n *Net) StartHookListener(ctx context.Context) {
	if n.monitorMode != HookMode {
		return
	}

	go func() {
		for {
			select {
			case <-ctx.Done():
				log.Println("dhcp hook listener stopped")
				return
			case lease := <-n.hookCallbackChan:
				n.HandleDHCPHook(lease)
			}
		}
	}()

	log.Println("dhcp hook listener started")
}

// SubmitHookLease is called by the dhcpd hook HTTP handler to submit a lease update.
// Non-blocking; returns immediately after queueing (or drops if channel is full).
func (n *Net) SubmitHookLease(lease Lease) error {
	select {
	case n.hookCallbackChan <- lease:
		return nil
	default:
		log.Printf("warning: dhcp hook callback channel full, dropping lease %s", lease.MAC)
		return fmt.Errorf("hook callback channel full")
	}
}

// parseDHCPLeasesFromBytes parses lease entries from byte data.
func parseDHCPLeasesFromBytes(data []byte) []Lease {
	var leases []Lease
	var current Lease
	inLease := false

	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)

		if strings.HasPrefix(line, "lease ") {
			inLease = true
			current = Lease{}
			parts := strings.Fields(line)
			if len(parts) >= 2 {
				current.IP = parts[1]
			}
		}

		if inLease {
			if strings.HasPrefix(line, "hardware ethernet") {
				mac := strings.TrimSuffix(strings.TrimPrefix(line, "hardware ethernet "), ";")
				current.MAC = strings.ToUpper(strings.TrimSpace(mac))
			}
			if strings.HasPrefix(line, "client-hostname") {
				host := strings.TrimSuffix(strings.TrimPrefix(line, "client-hostname "), ";")
				current.Hostname = strings.Trim(strings.TrimSpace(host), "\"")
			}
			if line == "}" {
				if current.Hostname == "" {
					current.Hostname = "-NA-"
				}
				if current.MAC != "" && current.IP != "" {
					leases = append(leases, current)
				}
				inLease = false
			}
		}
	}
	return leases
}

// CPUTemp reads the CPU temperature in Celsius.
func CPUTemp() float64 {
	data, err := os.ReadFile("/sys/class/thermal/thermal_zone0/temp")
	if err != nil {
		return 0
	}
	var temp int
	fmt.Sscanf(strings.TrimSpace(string(data)), "%d", &temp)
	return float64(temp) / 1000.0
}

// Uptime returns the system uptime string.
func Uptime() string {
	out, err := exec.Command("uptime", "-p").Output()
	if err != nil {
		return "unknown"
	}
	return strings.TrimSpace(string(out))
}

// EnableIPForward enables IPv4 forwarding.
func EnableIPForward() error {
	return os.WriteFile("/proc/sys/net/ipv4/ip_forward", []byte("1"), 0644)
}

// CopyConfig copies config files to system locations.
func CopyConfig(dataDir string) error {
	copies := map[string]string{
		"dhcpd.conf":  "/etc/dhcp/dhcpd.conf",
		"hostapd.conf": "/etc/hostapd/hostapd.conf",
	}
	for src, dst := range copies {
		srcPath := filepath.Join(dataDir, "conf", src)
		if _, err := os.Stat(srcPath); err != nil {
			continue
		}
		data, err := os.ReadFile(srcPath)
		if err != nil {
			return err
		}
		if err := os.WriteFile(dst, data, 0644); err != nil {
			return err
		}
	}
	return nil
}
