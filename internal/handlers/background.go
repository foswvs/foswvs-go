package handlers

import (
	"context"
	"log"
	"time"

	"github.com/foswvs/foswvs-go/internal/network"
	"github.com/foswvs/foswvs-go/internal/ws"
)

// leaseChanged checks if a lease's IP or hostname has changed.
func leaseChanged(prev, current network.Lease) bool {
	return prev.IP != current.IP || prev.Hostname != current.Hostname
}

// DHCPWatcher manages device registration based on DHCP lease updates.
// Behavior depends on the lease monitoring mode:
// - hook mode: processes leases from the hook callback (async)
// - file_poll mode: polls the dhcpd.leases file periodically
//
// In hook mode, this still serves as a background loop that reacts to device updates.
// In file_poll mode, this actively polls for lease changes.
func (a *App) DHCPWatcher(ctx context.Context, net *network.Net) {
	// In hook mode: use a longer tick for fallback ARP polling
	// In file_poll mode: use a shorter tick for active file polling
	tickInterval := 30 * time.Second
	if net.GetLeaseMonitorMode() == network.FilePollMode {
		tickInterval = 5 * time.Second
	}

	ticker := time.NewTicker(tickInterval)
	defer ticker.Stop()

	log.Printf("bg: DHCP watcher started (mode: %s)", net.GetLeaseMonitorMode())

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			// In file_poll mode: actively poll the file
			if net.GetLeaseMonitorMode() == network.FilePollMode {
				net.PollDHCPLeasesFile()
			}

			// Process current leases (from cache, populated by either hook or polling)
			lockdown := a.Maintenance.Get().Mode == MaintenanceLockdown
			leases := net.DHCPLeases()

			// Track changes to avoid unnecessary DB writes
			a.PrevLeasesMu.RLock()
			prevLeases := a.PrevLeases
			a.PrevLeasesMu.RUnlock()

			for _, lease := range leases {
				var devID int64
				var err error

				// Only upsert if this is a new lease or if the data changed
				if prev, exists := prevLeases[lease.MAC]; !exists || leaseChanged(prev, lease) {
					devID, err = a.Store.UpsertDevice(lease.MAC, lease.IP, lease.Hostname)
					if err != nil {
						log.Printf("bg: upsert device %s: %v", lease.MAC, err)
						continue
					}
				} else {
					// Lease unchanged; just look up the device ID
					devID, err = a.Store.GetDeviceIDByMAC(lease.MAC)
					if err != nil {
						log.Printf("bg: get device id %s: %v", lease.MAC, err)
						continue
					}
					if devID == 0 {
						// Device not in DB; upsert it
						devID, err = a.Store.UpsertDevice(lease.MAC, lease.IP, lease.Hostname)
						if err != nil {
							log.Printf("bg: upsert device %s: %v", lease.MAC, err)
							continue
						}
					}
				}

				// Defense in depth: the admin API already sweeps everyone
				// off on the moment lockdown is enabled, but catch any
				// stragglers (e.g. reconnected between ticks) here too.
				if lockdown {
					if a.IPT.IsConnected(lease.IP) {
						a.IPT.RemoveClient(lease.IP)
						log.Printf("bg: disconnected %s (%s) - maintenance lockdown", lease.MAC, lease.IP)
					}
					continue
				}

				// Auto-reconnect only if device is present/connected AND has remaining data
				// (paid, or previously granted by maintenance free mode)
				du, _ := a.Store.GetDataUsage(devID)
				if du.MBLimit > du.MBUsed && net.MACForIP(lease.IP) != "" {
					if !a.IPT.IsConnected(lease.IP) {
						a.IPT.AddClient(lease.IP)
						log.Printf("bg: auto-reconnected %s (%s) - %.1fMB remaining",
							lease.MAC, lease.IP, du.MBLimit-du.MBUsed)
					}
				}
			}

			// Update tracked leases for next iteration
			newLeaseMap := make(map[string]network.Lease)
			for _, lease := range leases {
				newLeaseMap[lease.MAC] = lease
			}
			a.PrevLeasesMu.Lock()
			a.PrevLeases = newLeaseMap
			a.PrevLeasesMu.Unlock()
		}
	}
}

// UsagePoller reads iptables byte counters, updates the DB, disconnects
// exhausted clients, and pushes real-time usage updates via WebSocket.
// Replaces the PHP `api/clients` infinite loop.
func (a *App) UsagePoller(ctx context.Context) {
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()

	log.Println("bg: usage poller started")

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			a.pollUsage()
		}
	}
}

func (a *App) pollUsage() {
	counters, err := a.IPT.GetForwardByteCounters()
	if err != nil {
		return
	}

	if len(counters) == 0 {
		return
	}

	for ip, bytes := range counters {
		if bytes == 0 {
			continue
		}

		mbUsed := float64(bytes) / 1e6

		devID, _ := a.Store.GetDeviceIDByIP(ip)
		if devID == 0 {
			// Unknown device in FORWARD chain — remove it
			a.IPT.RemoveClient(ip)
			continue
		}

		// Update usage in DB
		if err := a.Store.UpdateMBUsed(devID, mbUsed); err != nil {
			log.Printf("bg: update mb %s: %v", ip, err)
			continue
		}

		// Check if exhausted
		mac, du, _ := a.Store.GetDeviceFullInfo(devID)
		if du.MBLimit <= du.MBUsed {
			a.IPT.RemoveClient(ip)
			a.Hub.SendToDevice(devID, ws.MsgNetworkStatus, map[string]string{"status": "disconnected"})
			a.Hub.SendToDevice(devID, ws.MsgAlert, map[string]string{"message": "data exhausted"})
			log.Printf("bg: disconnected %s (data exhausted)", ip)
		} else {
			// Push live usage update to connected client
			a.Hub.SendToDevice(devID, ws.MsgDataUsage, map[string]interface{}{
				"ip":       ip,
				"mac":      mac,
				"mb_limit": du.MBLimit,
				"mb_used":  du.MBUsed,
			})
		}
	}

	// Push active devices and live bandwidth to admin panels
	if a.Hub.HasAdminClients() {
		devs, _ := a.Store.GetActiveDevices()
		a.Hub.SendToAdmins(ws.MsgActiveDevices, devs)

		sysInfo := map[string]interface{}{
			"cpu_temp": network.CPUTemp(),
			"uptime":   network.Uptime(),
		}
		a.Hub.SendToAdmins(ws.MsgSystemInfo, sysInfo)

		a.pushAdminBandwidth()
	}
}

// ShareTxCleaner periodically removes expired share tokens.
func (a *App) ShareTxCleaner(ctx context.Context) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			a.Store.CleanExpiredShareTx()
		}
	}
}
