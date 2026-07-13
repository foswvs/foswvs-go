package network

import (
	"bufio"
	"fmt"
	"log"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

// InterfaceStatus represents the current state of a network interface.
type InterfaceStatus struct {
	Name              string `json:"name"`
	MAC               string `json:"mac"`
	IP                string `json:"ip"`
	Netmask           string `json:"netmask"`
	Status            string `json:"status"`       // up/down
	Speed             string `json:"speed"`        // 1000Mbps, 100Mbps, etc
	MTU               int    `json:"mtu"`
	IsVirtual         bool   `json:"is_virtual"`
	Driver            string `json:"driver"`
	Type              string `json:"type"`         // physical, virtual, loopback, vlan
	IsLoopback        bool   `json:"is_loopback"`
	IsVLAN            bool   `json:"is_vlan"`
	BaseIface         string `json:"base_iface,omitempty"` // For VLANs, the underlying interface
	IsWAN             bool   `json:"is_wan"`              // Interface has default route
	IsPrivateIP       bool   `json:"is_private_ip"`       // IP in private range
	IsHostapdCandidate bool  `json:"is_hostapd_candidate"`// Can run WiFi AP
	Role              string `json:"role"`                 // "wan", "lan", or "none"
}

// InterfaceRole represents an assigned role for an interface.
type InterfaceRole struct {
	InterfaceName string `json:"interface_name"`
	Role          string `json:"role"` // "wan", "lan", "none"
	Primary       bool   `json:"primary"` // Primary interface for this role
}

// RoleConfig represents the complete role configuration.
type RoleConfig struct {
	Roles []InterfaceRole `json:"roles"`
}

// DiscoverInterfaces finds all available network interfaces.
func (n *Net) DiscoverInterfaces() ([]InterfaceStatus, error) {
	entries, err := os.ReadDir("/sys/class/net")
	if err != nil {
		// Fallback: try to get interfaces from ip command
		log.Printf("DEBUG: /sys/class/net read failed: %v, using fallback discoverInterfacesViaIP()", err)
		return discoverInterfacesViaIP()
	}
	var interfaces []InterfaceStatus

	for _, entry := range entries {
		// Check if entry is a directory (follow symlinks with os.Stat)
		info, err := os.Stat(filepath.Join("/sys/class/net", entry.Name()))
		if err != nil || !info.IsDir() {
			continue
		}

		iface := InterfaceStatus{
			Name:       entry.Name(),
			IsLoopback: entry.Name() == "lo",
			Status:     "unknown",
			Speed:      "Unknown",
			MTU:        1500,
		}

		// Determine interface type
		if entry.Name() == "lo" {
			iface.Type = "loopback"
		} else if strings.HasPrefix(entry.Name(), "vlan") {
			iface.Type = "vlan"
			iface.IsVLAN = true
			iface.BaseIface = extractBaseInterface(entry.Name())
		} else if isVirtualInterface(entry.Name()) {
			iface.Type = "virtual"
			iface.IsVirtual = true
		} else {
			iface.Type = "physical"
		}

		// Get MAC address (optional - continue if fails)
		if mac, err := readMACAddress(entry.Name()); err == nil {
			iface.MAC = mac
		}

		// Get IP configuration (optional - continue if fails)
		if ip, netmask, err := readInterfaceIP(entry.Name()); err == nil {
			iface.IP = ip
			iface.Netmask = netmask
		} else {
			log.Printf("failed to read IP for %s: %v", entry.Name(), err)
		}

		// Get interface status (optional - continue if fails)
		if status, err := readInterfaceStatus(entry.Name()); err == nil {
			iface.Status = status
		} else {
			log.Printf("failed to read status for %s: %v", entry.Name(), err)
		}

		// Get speed (optional - continue if fails)
		if speed, err := readInterfaceSpeed(entry.Name()); err == nil {
			iface.Speed = speed
		}

		// Get MTU (optional - continue if fails)
		if mtu, err := readInterfaceMTU(entry.Name()); err == nil {
			iface.MTU = mtu
		}

		// Get driver (for physical interfaces, optional)
		if !iface.IsVirtual && !iface.IsLoopback {
			if driver, err := readDriverInfo(entry.Name()); err == nil {
				iface.Driver = driver
			}
		}

		interfaces = append(interfaces, iface)
	}

	if len(interfaces) == 0 {
		// If we got no interfaces from /sys, try ip command
		return discoverInterfacesViaIP()
	}

	return interfaces, nil
}

// DetectWANInterface finds the interface used by the default route.
func (n *Net) DetectWANInterface() string {
	out, err := exec.Command("ip", "route", "show").Output()
	if err != nil {
		return ""
	}

	for _, line := range strings.Split(string(out), "\n") {
		if strings.HasPrefix(line, "default via") {
			parts := strings.Fields(line)
			// Format: default via <gateway> dev <interface>
			for i, part := range parts {
				if part == "dev" && i+1 < len(parts) {
					return parts[i+1]
				}
			}
		}
	}
	return ""
}

// IsPrivateIP checks if an IP is in private ranges (10.0.0.0/8, 172.16.0.0/12, 192.168.0.0/16).
func IsPrivateIP(ipStr string) bool {
	ip := net.ParseIP(ipStr)
	if ip == nil {
		return false
	}

	private10 := net.IPNet{IP: net.ParseIP("10.0.0.0"), Mask: net.CIDRMask(8, 32)}
	private172 := net.IPNet{IP: net.ParseIP("172.16.0.0"), Mask: net.CIDRMask(12, 32)}
	private192 := net.IPNet{IP: net.ParseIP("192.168.0.0"), Mask: net.CIDRMask(16, 32)}

	return private10.Contains(ip) || private172.Contains(ip) || private192.Contains(ip)
}

// IsHostapdCandidate checks if interface can run hostapd (wireless interface).
func IsHostapdCandidate(ifaceName string) bool {
	return strings.HasPrefix(ifaceName, "wlan") || strings.HasPrefix(ifaceName, "wifi") ||
		strings.HasPrefix(ifaceName, "ath") || strings.HasPrefix(ifaceName, "mlan")
}

// HostapdConfig represents hostapd WiFi AP configuration.
type HostapdConfig struct {
	SSID     string `json:"ssid"`
	Channel  int    `json:"channel"`
	Mode     string `json:"mode"` // g, a, n, ac, ax
	Password string `json:"password,omitempty"`
	Enabled  bool   `json:"enabled"` // WiFi AP enabled/disabled
}

// ReadHostapdConfig reads current hostapd configuration.
func ReadHostapdConfig(confPath string) (*HostapdConfig, error) {
	data, err := os.ReadFile(confPath)
	if err != nil {
		return &HostapdConfig{SSID: "PisoWiFi", Channel: 7, Mode: "g"}, nil // Return defaults
	}

	cfg := &HostapdConfig{}
	content := string(data)

	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "#") || line == "" {
			continue
		}

		if strings.HasPrefix(line, "ssid=") {
			cfg.SSID = strings.TrimPrefix(line, "ssid=")
		} else if strings.HasPrefix(line, "channel=") {
			if ch, err := strconv.Atoi(strings.TrimPrefix(line, "channel=")); err == nil {
				cfg.Channel = ch
			}
		} else if strings.HasPrefix(line, "hw_mode=") {
			cfg.Mode = strings.TrimPrefix(line, "hw_mode=")
		} else if strings.HasPrefix(line, "wpa_passphrase=") {
			cfg.Password = strings.TrimPrefix(line, "wpa_passphrase=")
		}
	}

	// Set defaults if not found
	if cfg.SSID == "" {
		cfg.SSID = "PisoWiFi"
	}
	if cfg.Channel == 0 {
		cfg.Channel = 7
	}
	if cfg.Mode == "" {
		cfg.Mode = "g"
	}

	// Check if hostapd service is enabled
	out, err := exec.Command("systemctl", "is-enabled", "hostapd").Output()
	if err == nil && strings.TrimSpace(string(out)) == "enabled" {
		cfg.Enabled = true
	}

	return cfg, nil
}

// WriteHostapdConfig writes hostapd configuration.
func WriteHostapdConfig(confPath string, cfg *HostapdConfig) error {
	content := `# PisoWiFi access point config — see INSTALL.md.
#
# Open SSID by design: anyone can join the WiFi for free, but the captive
# portal (this app) gates actual internet access behind payment/sharing.

interface=wlan0
driver=nl80211
`

	content += fmt.Sprintf("ssid=%s\n", cfg.SSID)
	content += fmt.Sprintf("hw_mode=%s\n", cfg.Mode)
	content += fmt.Sprintf("channel=%d\n", cfg.Channel)

	content += `wmm_enabled=0
macaddr_acl=0
auth_algs=1
ignore_broadcast_ssid=0
`

	if err := os.WriteFile(confPath, []byte(content), 0644); err != nil {
		return fmt.Errorf("write hostapd config: %w", err)
	}

	return nil
}

// discoverInterfacesViaIP discovers interfaces using the ip command as fallback.
func discoverInterfacesViaIP() ([]InterfaceStatus, error) {
	out, err := exec.Command("ip", "link", "show").Output()
	if err != nil {
		return nil, fmt.Errorf("fallback interface discovery failed: %w", err)
	}

	var interfaces []InterfaceStatus
	scanner := bufio.NewScanner(strings.NewReader(string(out)))
	var currentIface *InterfaceStatus

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())

		// Parse interface definition lines like: 1: lo: <LOOPBACK,UP,LOWER_UP>
		if strings.Contains(line, ":") && !strings.HasPrefix(line, "link/") {
			parts := strings.SplitN(line, ":", 3)
			if len(parts) >= 2 {
				ifaceName := strings.TrimSpace(parts[1])

				if currentIface != nil {
					interfaces = append(interfaces, *currentIface)
				}

				currentIface = &InterfaceStatus{
					Name:   ifaceName,
					Type:   "physical",
					Status: "unknown",
					Speed:  "Unknown",
					MTU:    1500,
				}

				// Determine type
				if ifaceName == "lo" {
					currentIface.IsLoopback = true
					currentIface.Type = "loopback"
				} else if strings.HasPrefix(ifaceName, "vlan") {
					currentIface.IsVLAN = true
					currentIface.Type = "vlan"
				} else if isVirtualInterface(ifaceName) {
					currentIface.IsVirtual = true
					currentIface.Type = "virtual"
				}

				// Parse status from the line
				if strings.Contains(line, "<UP") {
					currentIface.Status = "up"
				} else if strings.Contains(line, "<DOWN") {
					currentIface.Status = "down"
				}
			}
		}
	}

	// Add last interface
	if currentIface != nil {
		interfaces = append(interfaces, *currentIface)
	}

	if len(interfaces) == 0 {
		return nil, fmt.Errorf("no interfaces discovered")
	}

	return interfaces, nil
}

// GetInterfaceDetails retrieves detailed information about a specific interface.
func (n *Net) GetInterfaceDetails(ifaceName string) (*InterfaceStatus, error) {
	interfaces, err := n.DiscoverInterfaces()
	if err != nil {
		return nil, err
	}

	for _, iface := range interfaces {
		if iface.Name == ifaceName {
			return &iface, nil
		}
	}

	return nil, fmt.Errorf("interface %s not found", ifaceName)
}

// readMACAddress reads the MAC address of an interface.
func readMACAddress(ifaceName string) (string, error) {
	path := filepath.Join("/sys/class/net", ifaceName, "address")
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(data)), nil
}

// readInterfaceIP reads the IP address of an interface.
func readInterfaceIP(ifaceName string) (string, string, error) {
	// Try to read from ip command
	out, err := exec.Command("ip", "addr", "show", ifaceName).Output()
	if err != nil {
		return "", "", err
	}

	var ip, netmask string
	scanner := bufio.NewScanner(strings.NewReader(string(out)))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if strings.HasPrefix(line, "inet ") {
			parts := strings.Fields(line)
			if len(parts) >= 2 {
				// parts[1] is in CIDR format like 10.0.0.1/24
				cidrParts := strings.Split(parts[1], "/")
				if len(cidrParts) == 2 {
					ip = cidrParts[0]
					// Convert CIDR prefix to netmask
					if prefix, err := strconv.Atoi(cidrParts[1]); err == nil {
						netmask = cidrToNetmask(prefix)
					}
				}
			}
			break
		}
	}

	return ip, netmask, nil
}

// readInterfaceStatus reads whether an interface is up or down.
func readInterfaceStatus(ifaceName string) (string, error) {
	path := filepath.Join("/sys/class/net", ifaceName, "operstate")
	data, err := os.ReadFile(path)
	if err == nil {
		status := strings.TrimSpace(string(data))
		if status != "" {
			return status, nil
		}
	}

	// Fallback: use ip link show
	out, err := exec.Command("ip", "link", "show", ifaceName).Output()
	if err != nil {
		return "", err
	}

	// Look for "UP" or "DOWN" in output
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if strings.Contains(line, "UP") {
			return "up", nil
		} else if strings.Contains(line, "DOWN") {
			return "down", nil
		}
	}

	return "unknown", nil
}

// readInterfaceSpeed reads the interface speed.
func readInterfaceSpeed(ifaceName string) (string, error) {
	// Try ethtool first (requires root or capabilities)
	out, err := exec.Command("ethtool", ifaceName).Output()
	if err == nil {
		scanner := bufio.NewScanner(strings.NewReader(string(out)))
		for scanner.Scan() {
			line := scanner.Text()
			if strings.Contains(line, "Speed:") {
				parts := strings.Split(line, ":")
				if len(parts) == 2 {
					return strings.TrimSpace(parts[1]), nil
				}
			}
		}
	}

	// Fallback: check /sys/class/net/*/speed
	path := filepath.Join("/sys/class/net", ifaceName, "speed")
	data, err := os.ReadFile(path)
	if err != nil {
		return "Unknown", nil
	}

	speedMbps := strings.TrimSpace(string(data))
	if speedMbps == "-1" {
		return "Unknown", nil
	}

	return speedMbps + "Mbps", nil
}

// readInterfaceMTU reads the MTU of an interface.
func readInterfaceMTU(ifaceName string) (int, error) {
	path := filepath.Join("/sys/class/net", ifaceName, "mtu")
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	mtu := 0
	fmt.Sscanf(strings.TrimSpace(string(data)), "%d", &mtu)
	return mtu, nil
}

// readDriverInfo reads the driver information for a physical interface.
func readDriverInfo(ifaceName string) (string, error) {
	path := filepath.Join("/sys/class/net", ifaceName, "device", "driver")
	link, err := os.Readlink(path)
	if err != nil {
		return "", err
	}
	// Extract driver name from path like ../../../drivers/net/driver_name
	parts := strings.Split(link, "/")
	if len(parts) > 0 {
		return parts[len(parts)-1], nil
	}
	return "", nil
}

// isVirtualInterface checks if an interface is virtual (docker, tun, tap, etc).
func isVirtualInterface(ifaceName string) bool {
	virtualPrefixes := []string{"docker", "br", "veth", "tun", "tap", "wg"}
	for _, prefix := range virtualPrefixes {
		if strings.HasPrefix(ifaceName, prefix) {
			return true
		}
	}
	return false
}

// extractBaseInterface extracts the base interface from a VLAN name.
func extractBaseInterface(vlanName string) string {
	// VLAN formats: eth0.10, eth0:10, vlan10, etc.
	if idx := strings.IndexAny(vlanName, ".:"); idx > 0 {
		return vlanName[:idx]
	}
	return vlanName
}

// cidrToNetmask converts CIDR prefix to netmask notation.
func cidrToNetmask(prefix int) string {
	if prefix < 0 || prefix > 32 {
		return "0.0.0.0"
	}
	mask := net.CIDRMask(prefix, 32)
	return net.IP(mask).String()
}

// GetInterfacesByRole returns all interfaces assigned to a specific role.
func (n *Net) GetInterfacesByRole(roleConfig *RoleConfig, role string) []InterfaceRole {
	var matching []InterfaceRole
	if roleConfig == nil {
		return matching
	}
	for _, r := range roleConfig.Roles {
		if r.Role == role {
			matching = append(matching, r)
		}
	}
	return matching
}

// ValidateRoleAssignment checks if a role assignment is valid.
func ValidateRoleAssignment(roleConfig *RoleConfig, ifaceName, role string) error {
	if roleConfig == nil {
		roleConfig = &RoleConfig{}
	}

	// Check for duplicate WAN assignments (only one WAN allowed)
	if role == "wan" {
		for _, r := range roleConfig.Roles {
			if r.Role == "wan" && r.InterfaceName != ifaceName {
				return fmt.Errorf("WAN role already assigned to %s", r.InterfaceName)
			}
		}
	}

	// Check for duplicate LAN assignments (only one primary LAN allowed)
	if role == "lan" {
		for _, r := range roleConfig.Roles {
			if r.Role == "lan" && r.Primary && r.InterfaceName != ifaceName {
				return fmt.Errorf("Primary LAN role already assigned to %s", r.InterfaceName)
			}
		}
	}

	// Cannot assign same interface to multiple primary roles
	for _, r := range roleConfig.Roles {
		if r.InterfaceName == ifaceName && r.Role != role {
			if role == "wan" || (role == "lan" && r.Primary) {
				return fmt.Errorf("interface %s already assigned to %s role", ifaceName, r.Role)
			}
		}
	}

	return nil
}

// AssignRole assigns a role to an interface.
func AssignRole(roleConfig *RoleConfig, ifaceName, role string, primary bool) error {
	if roleConfig == nil {
		roleConfig = &RoleConfig{}
	}

	// Validate assignment
	if err := ValidateRoleAssignment(roleConfig, ifaceName, role); err != nil {
		return err
	}

	// Remove existing role for this interface
	for i, r := range roleConfig.Roles {
		if r.InterfaceName == ifaceName {
			roleConfig.Roles = append(roleConfig.Roles[:i], roleConfig.Roles[i+1:]...)
			break
		}
	}

	// Add new role
	if role != "none" {
		roleConfig.Roles = append(roleConfig.Roles, InterfaceRole{
			InterfaceName: ifaceName,
			Role:          role,
			Primary:       primary,
		})
	}

	return nil
}

// FilterInterfacesByType returns interfaces of a specific type.
func FilterInterfacesByType(interfaces []InterfaceStatus, typeName string) []InterfaceStatus {
	var filtered []InterfaceStatus
	for _, iface := range interfaces {
		if iface.Type == typeName {
			filtered = append(filtered, iface)
		}
	}
	return filtered
}

// FilterUsableInterfaces returns physical and some virtual interfaces suitable for roles.
func FilterUsableInterfaces(interfaces []InterfaceStatus) []InterfaceStatus {
	var filtered []InterfaceStatus
	for _, iface := range interfaces {
		// Include physical interfaces and some virtual ones (VLAN, bridges)
		if iface.Type == "physical" || iface.Type == "vlan" {
			filtered = append(filtered, iface)
		}
	}
	return filtered
}
