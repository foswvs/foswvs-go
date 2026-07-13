package bandwidth

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
)

// TrafficControlConfig holds traffic control settings
type TrafficControlConfig struct {
	MaximumDynamicMbps int    `json:"maximum_dynamic_mbps"` // Maximum per-IP cap in Mbps
	TotalBandwidthMbps int    `json:"total_bandwidth_mbps"` // Total available bandwidth in Mbps
	QdiscType          string `json:"qdisc_type"`           // "htb" or "cake"
	OverheadBytes      int    `json:"overhead_bytes"`       // Frame overhead for CAKE (38 for Ethernet)
	EnableIngress      bool   `json:"enable_ingress"`       // Apply to ingress (download) traffic
	InterfaceName      string `json:"interface_name"`       // Network interface to shape (e.g., eth0, wlan0)
}

// DefaultTrafficControlConfig returns factory defaults
func DefaultTrafficControlConfig() TrafficControlConfig {
	return TrafficControlConfig{
		MaximumDynamicMbps: 50,
		TotalBandwidthMbps: 100,
		QdiscType:         "cake",
		OverheadBytes:     38,
		EnableIngress:     true,
		InterfaceName:     "eth0", // Will be auto-detected if not available
	}
}

func tcConfigPath(dataDir string) string {
	return filepath.Join(dataDir, "traffic_control.json")
}

// LoadTrafficControlConfig reads the traffic control config, falling back to defaults
func LoadTrafficControlConfig(dataDir string) TrafficControlConfig {
	data, err := os.ReadFile(tcConfigPath(dataDir))
	if err != nil {
		return DefaultTrafficControlConfig()
	}
	var cfg TrafficControlConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return DefaultTrafficControlConfig()
	}
	if cfg.MaximumDynamicMbps <= 0 || cfg.MaximumDynamicMbps > 1000 {
		cfg.MaximumDynamicMbps = 50
	}
	if cfg.TotalBandwidthMbps <= 0 || cfg.TotalBandwidthMbps > 10000 {
		cfg.TotalBandwidthMbps = 100
	}
	if cfg.QdiscType != "htb" && cfg.QdiscType != "cake" {
		cfg.QdiscType = "cake"
	}
	if cfg.OverheadBytes <= 0 || cfg.OverheadBytes > 256 {
		cfg.OverheadBytes = 38
	}
	return cfg
}

// SaveTrafficControlConfig persists the traffic control config
func SaveTrafficControlConfig(dataDir string, cfg TrafficControlConfig) error {
	data, err := json.Marshal(cfg)
	if err != nil {
		return err
	}
	return os.WriteFile(tcConfigPath(dataDir), data, 0644)
}

// NetworkInterface describes an available network interface
type NetworkInterface struct {
	Name   string `json:"name"`
	Status string `json:"status"` // "up" or "down"
	IP     string `json:"ip"`
	MAC    string `json:"mac"`
}

// DetectNetworkInterfaces returns a list of available non-loopback network interfaces
// Only includes interfaces with both a valid IP address and MAC address
func DetectNetworkInterfaces() []NetworkInterface {
	var ifaces []NetworkInterface

	systemIfaces, err := net.Interfaces()
	if err != nil {
		return ifaces
	}

	for _, iface := range systemIfaces {
		// Skip loopback
		if iface.Flags&net.FlagLoopback != 0 {
			continue
		}

		// Skip interfaces without a valid MAC address
		mac := iface.HardwareAddr.String()
		if mac == "" {
			continue
		}

		status := "down"
		if iface.Flags&net.FlagUp != 0 {
			status = "up"
		}

		// Try to get IP address
		ip := ""
		addrs, err := iface.Addrs()
		if err == nil && len(addrs) > 0 {
			// Get the first non-link-local address
			for _, addr := range addrs {
				ipnet, ok := addr.(*net.IPNet)
				if ok && !ipnet.IP.IsLinkLocalUnicast() {
					ip = ipnet.IP.String()
					break
				}
			}
		}

		// Only include interfaces with both valid IP and MAC addresses
		if ip == "" {
			continue
		}

		ifaces = append(ifaces, NetworkInterface{
			Name:   iface.Name,
			Status: status,
			IP:     ip,
			MAC:    mac,
		})
	}

	return ifaces
}

// SelectInterfaceForTrafficControl chooses the best available interface
// Priority: eth* > wlan* > first UP interface > default
func SelectInterfaceForTrafficControl(preferred string) string {
	ifaces := DetectNetworkInterfaces()

	// If preferred interface is available and UP, use it
	if preferred != "" {
		for _, iface := range ifaces {
			if iface.Name == preferred && iface.Status == "up" {
				return preferred
			}
		}
	}

	// First pass: look for UP ethernet interfaces
	for _, iface := range ifaces {
		if iface.Status == "up" && strings.HasPrefix(iface.Name, "eth") {
			return iface.Name
		}
	}

	// Second pass: look for UP wireless interfaces
	for _, iface := range ifaces {
		if iface.Status == "up" && (strings.HasPrefix(iface.Name, "wlan") || strings.HasPrefix(iface.Name, "wl")) {
			return iface.Name
		}
	}

	// Third pass: any UP interface
	for _, iface := range ifaces {
		if iface.Status == "up" {
			return iface.Name
		}
	}

	// Fallback to first available
	if len(ifaces) > 0 {
		return ifaces[0].Name
	}

	return "eth0" // ultimate fallback
}

// ValidateInterface checks if the interface exists and is suitable
func ValidateInterface(name string) error {
	ifaces := DetectNetworkInterfaces()
	for _, iface := range ifaces {
		if iface.Name == name {
			if iface.Status != "up" {
				return fmt.Errorf("interface %s is not UP", name)
			}
			return nil
		}
	}
	return fmt.Errorf("interface %s not found", name)
}
