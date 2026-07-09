package handlers

import (
	"context"
	"log"
	"time"

	"github.com/foswvs/foswvs-go/internal/network"
	"github.com/foswvs/foswvs-go/internal/ws"
)

// DHCPWatcher polls the ARP table and registers new devices into the database.
// Replaces the PHP DHCP on-commit hook + infinite loop.
func (a *App) DHCPWatcher(ctx context.Context, net *network.Net) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	log.Println("bg: DHCP watcher started")

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			lockdown := a.Maintenance.Get().Mode == MaintenanceLockdown

			leases := net.DHCPLeases()
			for _, lease := range leases {
				devID, err := a.Store.UpsertDevice(lease.MAC, lease.IP, lease.Hostname)
				if err != nil {
					log.Printf("bg: upsert device %s: %v", lease.MAC, err)
					continue
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

				// Auto-reconnect if device has remaining data (paid, or
				// previously granted by maintenance free mode)
				du, _ := a.Store.GetDataUsage(devID)
				if du.MBLimit > du.MBUsed {
					if !a.IPT.IsConnected(lease.IP) {
						a.IPT.AddClient(lease.IP)
						log.Printf("bg: auto-reconnected %s (%s) - %.1fMB remaining",
							lease.MAC, lease.IP, du.MBLimit-du.MBUsed)
					}
				}
			}
		}
	}
}

// UsagePoller reads iptables byte counters, updates the DB, disconnects
// exhausted clients, and pushes real-time usage updates via WebSocket.
// Replaces the PHP `api/clients` infinite loop.
func (a *App) UsagePoller(ctx context.Context) {
	ticker := time.NewTicker(3 * time.Second)
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
		du, _ := a.Store.GetDataUsage(devID)
		if du.MBLimit <= du.MBUsed {
			a.IPT.RemoveClient(ip)
			a.Hub.SendToIP(ip, ws.MsgNetworkStatus, map[string]string{"status": "disconnected"})
			a.Hub.SendToIP(ip, ws.MsgAlert, map[string]string{"message": "data exhausted"})
			log.Printf("bg: disconnected %s (data exhausted)", ip)
		} else {
			// Push live usage update to connected client
			mac, _ := a.Store.GetDeviceMAC(devID)
			a.Hub.SendToIP(ip, ws.MsgDataUsage, map[string]interface{}{
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
