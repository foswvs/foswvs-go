package network

import (
	"fmt"
	"log"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

// InterfaceConfig represents a single network interface configuration.
type InterfaceConfig struct {
	Name    string `json:"name"`             // eth0, eth1, wlan0, vlan10, etc.
	IP      string `json:"ip"`               // Static IP address
	Netmask string `json:"netmask"`          // Network mask
	Gateway string `json:"gateway,omitempty"`// Default gateway (optional)
	DHCP    bool   `json:"dhcp"`             // Use DHCP instead of static
	Enabled bool   `json:"enabled"`          // Interface is active
}

// NetworkConfig represents network interface and DHCP configuration.
type NetworkConfig struct {
	Hostname       string              `json:"hostname"`
	Interfaces     []InterfaceConfig   `json:"interfaces"`     // Multiple interfaces
	Roles          []InterfaceRole     `json:"roles"`          // Role assignments
	PrimaryNetwork string              `json:"primary_network"` // e.g., "wlan0" - the AP interface
	DHCPRangeStart string              `json:"dhcp_range_start"`   // e.g., 10.0.0.10
	DHCPRangeEnd   string              `json:"dhcp_range_end"`     // e.g., 10.0.0.250
	DHCPSubnetMask string              `json:"dhcp_subnet_mask"`
	DHCPBroadcastAddr string            `json:"dhcp_broadcast_addr"`
	DNSServers     []string            `json:"dns_servers"` // Can be multiple

	// Legacy fields for backwards compatibility
	InterfaceIP      string `json:"interface_ip,omitempty"`       // wlan0 IP (deprecated)
	InterfaceNetmask string `json:"interface_netmask,omitempty"`  // wlan0 netmask (deprecated)
	GatewayIP        string `json:"gateway_ip,omitempty"`         // gateway (deprecated)
}

// GetNetworkConfig reads current network configuration from system.
func (n *Net) GetNetworkConfig() (*NetworkConfig, error) {
	cfg := &NetworkConfig{
		Interfaces:     []InterfaceConfig{},
		Roles:          []InterfaceRole{},
		PrimaryNetwork: n.Iface,
	}

	// Read hostname
	if hostname, err := os.ReadFile("/etc/hostname"); err == nil {
		cfg.Hostname = strings.TrimSpace(string(hostname))
	}

	// Parse interface configs
	if ifaces, dnsServers, err := parseNetworkInterfaces(); err == nil {
		cfg.Interfaces = ifaces
		cfg.DNSServers = dnsServers

		// Set legacy fields for backwards compatibility (primary interface)
		for _, iface := range ifaces {
			if iface.Name == n.Iface && !iface.DHCP {
				cfg.InterfaceIP = iface.IP
				cfg.InterfaceNetmask = iface.Netmask
				cfg.GatewayIP = iface.Gateway
				break
			}
		}
	}

	// Parse DHCP config
	if dhcp, err := parseDHCPConfig("/etc/dhcp/dhcpd.conf"); err == nil {
		cfg.DHCPRangeStart = dhcp.RangeStart
		cfg.DHCPRangeEnd = dhcp.RangeEnd
		cfg.DHCPSubnetMask = dhcp.SubnetMask
		cfg.DHCPBroadcastAddr = dhcp.BroadcastAddr
	}

	return cfg, nil
}

func parseNetworkInterfaces() ([]InterfaceConfig, []string, error) {
	data, err := os.ReadFile("/etc/network/interfaces")
	if err != nil {
		return nil, nil, err
	}

	var interfaces []InterfaceConfig
	var currentIface *InterfaceConfig
	var dns []string

	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		// New interface definition
		if strings.HasPrefix(line, "auto ") || strings.HasPrefix(line, "iface ") {
			if strings.HasPrefix(line, "auto ") {
				name := strings.TrimSpace(strings.TrimPrefix(line, "auto"))
				currentIface = &InterfaceConfig{Name: name, Enabled: true}
			} else if strings.HasPrefix(line, "iface ") {
				parts := strings.Fields(line)
				if len(parts) >= 2 {
					name := parts[1]
					method := "static"
					if len(parts) >= 4 {
						method = parts[3]
					}
					if currentIface == nil || currentIface.Name != name {
						currentIface = &InterfaceConfig{Name: name}
					}
					currentIface.DHCP = (method == "dhcp")
				}
			}
			if currentIface != nil && !containsInterface(interfaces, currentIface.Name) {
				interfaces = append(interfaces, *currentIface)
			}
		}

		// Interface properties (indented)
		if strings.HasPrefix(line, "  ") || strings.HasPrefix(line, "\t") {
			line = strings.TrimLeft(line, " \t")

			if currentIface != nil {
				if strings.HasPrefix(line, "address ") {
					currentIface.IP = strings.TrimSpace(strings.TrimPrefix(line, "address"))
				} else if strings.HasPrefix(line, "netmask ") {
					currentIface.Netmask = strings.TrimSpace(strings.TrimPrefix(line, "netmask"))
				} else if strings.HasPrefix(line, "gateway ") {
					currentIface.Gateway = strings.TrimSpace(strings.TrimPrefix(line, "gateway"))
				} else if strings.HasPrefix(line, "dns-nameservers ") {
					servers := strings.TrimSpace(strings.TrimPrefix(line, "dns-nameservers"))
					dns = strings.Fields(servers)
				}
			}
		}
	}

	return interfaces, dns, nil
}

func containsInterface(ifaces []InterfaceConfig, name string) bool {
	for _, i := range ifaces {
		if i.Name == name {
			return true
		}
	}
	return false
}

type dhcpConfig struct {
	RangeStart    string
	RangeEnd      string
	SubnetMask    string
	BroadcastAddr string
}

func parseDHCPConfig(path string) (*dhcpConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	cfg := &dhcpConfig{}
	content := string(data)

	// Extract DHCP range
	rangeRe := regexp.MustCompile(`range\s+(\d+\.\d+\.\d+\.\d+)\s+(\d+\.\d+\.\d+\.\d+)`)
	if matches := rangeRe.FindStringSubmatch(content); len(matches) == 3 {
		cfg.RangeStart = matches[1]
		cfg.RangeEnd = matches[2]
	}

	// Extract subnet mask
	maskRe := regexp.MustCompile(`subnet-mask\s+(\d+\.\d+\.\d+\.\d+)`)
	if matches := maskRe.FindStringSubmatch(content); len(matches) == 2 {
		cfg.SubnetMask = matches[1]
	}

	// Extract broadcast address
	bcastRe := regexp.MustCompile(`broadcast-address\s+(\d+\.\d+\.\d+\.\d+)`)
	if matches := bcastRe.FindStringSubmatch(content); len(matches) == 2 {
		cfg.BroadcastAddr = matches[1]
	}

	return cfg, nil
}

// EnsureDHCPConfigWithHook generates/updates dhcpd.conf with the hook block.
// This is called on startup to ensure the hook is configured.
func EnsureDHCPConfigWithHook(dataDir string) error {
	// Read current network config to get gateway IP and other settings
	data, err := os.ReadFile("/etc/dhcp/dhcpd.conf")
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("read dhcpd.conf: %w", err)
	}

	content := string(data)

	// Check if both hook AND DNS are already configured
	hasHook := strings.Contains(content, "on commit") && strings.Contains(content, "dhcp_hook")
	hasDNS := strings.Contains(content, "domain-name-servers")

	if hasHook && hasDNS {
		return nil // Hook and DNS already configured
	}

	// Read existing config or create default
	cfg := &NetworkConfig{
		DHCPRangeStart:    "10.0.0.10",
		DHCPRangeEnd:      "10.0.0.250",
		DHCPSubnetMask:    "255.255.240.0",
		DHCPBroadcastAddr: "10.0.15.255",
		InterfaceIP:       "10.0.0.1",
		PrimaryNetwork:    "wlan0",
		DNSServers:        []string{"94.140.14.14", "1.1.1.1", "8.8.8.8"},
	}

	// Try to parse existing config to preserve settings
	if len(data) > 0 {
		// Extract existing values if they exist
		rangeRe := regexp.MustCompile(`range\s+(\d+\.\d+\.\d+\.\d+)\s+(\d+\.\d+\.\d+\.\d+)`)
		if matches := rangeRe.FindStringSubmatch(content); len(matches) == 3 {
			cfg.DHCPRangeStart = matches[1]
			cfg.DHCPRangeEnd = matches[2]
		}

		maskRe := regexp.MustCompile(`subnet-mask\s+(\d+\.\d+\.\d+\.\d+)`)
		if matches := maskRe.FindStringSubmatch(content); len(matches) == 2 {
			cfg.DHCPSubnetMask = matches[1]
		}

		bcastRe := regexp.MustCompile(`broadcast-address\s+(\d+\.\d+\.\d+\.\d+)`)
		if matches := bcastRe.FindStringSubmatch(content); len(matches) == 2 {
			cfg.DHCPBroadcastAddr = matches[1]
		}

		routersRe := regexp.MustCompile(`option routers\s+(\d+\.\d+\.\d+\.\d+)`)
		if matches := routersRe.FindStringSubmatch(content); len(matches) == 2 {
			cfg.InterfaceIP = matches[1]
		}

		// Parse existing DNS servers if configured
		dnsRe := regexp.MustCompile(`option domain-name-servers\s+([^;]+)`)
		if matches := dnsRe.FindStringSubmatch(content); len(matches) == 2 {
			dnsStr := strings.TrimSpace(matches[1])
			dnsServers := strings.FieldsFunc(dnsStr, func(r rune) bool {
				return r == ',' || r == ' '
			})
			if len(dnsServers) > 0 {
				cfg.DNSServers = dnsServers
			}
		}
	}

	// Ensure DNS servers are set to defaults if not configured
	if len(cfg.DNSServers) == 0 {
		cfg.DNSServers = []string{"94.140.14.14", "1.1.1.1", "8.8.8.8"}
	}

	// Write the config with hook
	if err := writeDHCPConfig(cfg, dataDir); err != nil {
		return fmt.Errorf("write dhcpd.conf: %w", err)
	}

	log.Println("dhcp: ensured hook is configured in dhcpd.conf")
	return nil
}

// SetNetworkConfig writes network configuration to system files.
func (n *Net) SetNetworkConfig(cfg *NetworkConfig, dataDir string) error {
	// Validate configuration
	if err := validateNetworkConfig(cfg); err != nil {
		return err
	}

	// Write hostname
	if cfg.Hostname != "" {
		if err := os.WriteFile("/etc/hostname", []byte(cfg.Hostname+"\n"), 0644); err != nil {
			return fmt.Errorf("write hostname: %w", err)
		}
		if err := exec.Command("hostname", cfg.Hostname).Run(); err != nil {
			return fmt.Errorf("set hostname: %w", err)
		}
	}

	// Write network interfaces config
	if err := writeNetworkInterfaces(cfg); err != nil {
		return err
	}

	// Write DHCP config to both data dir and system
	if err := writeDHCPConfig(cfg, dataDir); err != nil {
		return err
	}

	// Reload services
	if err := reloadNetworkServices(); err != nil {
		return fmt.Errorf("reload services: %w", err)
	}

	return nil
}

func validateNetworkConfig(cfg *NetworkConfig) error {
	// Validate interfaces
	for _, iface := range cfg.Interfaces {
		if !iface.DHCP {
			if iface.IP != "" && net.ParseIP(iface.IP) == nil {
				return fmt.Errorf("invalid IP for %s: %s", iface.Name, iface.IP)
			}
			if iface.Netmask != "" && net.ParseIP(iface.Netmask) == nil {
				return fmt.Errorf("invalid netmask for %s: %s", iface.Name, iface.Netmask)
			}
		}
		if iface.Gateway != "" && net.ParseIP(iface.Gateway) == nil {
			return fmt.Errorf("invalid gateway for %s: %s", iface.Name, iface.Gateway)
		}
	}

	// Validate legacy fields for backwards compatibility
	if cfg.InterfaceIP != "" && net.ParseIP(cfg.InterfaceIP) == nil {
		return fmt.Errorf("invalid interface IP: %s", cfg.InterfaceIP)
	}
	if cfg.InterfaceNetmask != "" && net.ParseIP(cfg.InterfaceNetmask) == nil {
		return fmt.Errorf("invalid netmask: %s", cfg.InterfaceNetmask)
	}
	if cfg.GatewayIP != "" && net.ParseIP(cfg.GatewayIP) == nil {
		return fmt.Errorf("invalid gateway IP: %s", cfg.GatewayIP)
	}

	// Validate DHCP
	if cfg.DHCPRangeStart != "" && net.ParseIP(cfg.DHCPRangeStart) == nil {
		return fmt.Errorf("invalid DHCP range start: %s", cfg.DHCPRangeStart)
	}
	if cfg.DHCPRangeEnd != "" && net.ParseIP(cfg.DHCPRangeEnd) == nil {
		return fmt.Errorf("invalid DHCP range end: %s", cfg.DHCPRangeEnd)
	}
	if cfg.DHCPSubnetMask != "" && net.ParseIP(cfg.DHCPSubnetMask) == nil {
		return fmt.Errorf("invalid DHCP subnet mask: %s", cfg.DHCPSubnetMask)
	}

	// Validate DNS servers
	for _, dns := range cfg.DNSServers {
		if net.ParseIP(dns) == nil {
			return fmt.Errorf("invalid DNS server: %s", dns)
		}
	}

	// Validate DHCP range order
	if cfg.DHCPRangeStart != "" && cfg.DHCPRangeEnd != "" {
		start := net.ParseIP(cfg.DHCPRangeStart).To4()
		end := net.ParseIP(cfg.DHCPRangeEnd).To4()
		if start != nil && end != nil {
			if ipLessThan(end, start) {
				return fmt.Errorf("DHCP range end must be >= start")
			}
		}
	}

	return nil
}

func ipLessThan(ip1, ip2 net.IP) bool {
	for i := 0; i < 4; i++ {
		if ip1[i] < ip2[i] {
			return true
		}
		if ip1[i] > ip2[i] {
			return false
		}
	}
	return false
}

func writeNetworkInterfaces(cfg *NetworkConfig) error {
	content := "# Network interface configuration\n"
	content += "auto lo\niface lo inet loopback\n\n"

	// Write all interfaces
	if len(cfg.Interfaces) > 0 {
		for _, iface := range cfg.Interfaces {
			if !iface.Enabled && iface.Name != "lo" {
				continue
			}

			content += fmt.Sprintf("auto %s\n", iface.Name)
			if iface.DHCP {
				content += fmt.Sprintf("iface %s inet dhcp\n", iface.Name)
			} else {
				content += fmt.Sprintf("iface %s inet static\n", iface.Name)
				if iface.IP != "" {
					content += fmt.Sprintf("  address %s\n", iface.IP)
				}
				if iface.Netmask != "" {
					content += fmt.Sprintf("  netmask %s\n", iface.Netmask)
				}
				if iface.Gateway != "" {
					content += fmt.Sprintf("  gateway %s\n", iface.Gateway)
				}
			}
			content += "\n"
		}
	} else {
		// Fallback for legacy format: use deprecated fields
		content += "auto wlan0\niface wlan0 inet static\n"
		if cfg.InterfaceIP != "" {
			content += fmt.Sprintf("  address %s\n", cfg.InterfaceIP)
		}
		if cfg.InterfaceNetmask != "" {
			content += fmt.Sprintf("  netmask %s\n", cfg.InterfaceNetmask)
		}
		if cfg.GatewayIP != "" {
			content += fmt.Sprintf("  gateway %s\n", cfg.GatewayIP)
		}
		content += "\n"
	}

	// DNS servers are global, not per-interface in this format
	if len(cfg.DNSServers) > 0 {
		// Add to primary interface
		if len(cfg.Interfaces) > 0 {
			// Find the primary interface and add DNS to it
			content += fmt.Sprintf("# DNS servers for %s\n", cfg.PrimaryNetwork)
		}
		content += fmt.Sprintf("dns-nameservers %s\n", strings.Join(cfg.DNSServers, " "))
	}

	if err := os.WriteFile("/etc/network/interfaces", []byte(content), 0644); err != nil {
		return fmt.Errorf("write /etc/network/interfaces: %w", err)
	}

	return nil
}

func writeDHCPConfig(cfg *NetworkConfig, dataDir string) error {
	// Determine the gateway IP for DHCP options (from primary interface)
	gatewayIP := cfg.InterfaceIP // Legacy field
	for _, iface := range cfg.Interfaces {
		if iface.Name == cfg.PrimaryNetwork && !iface.DHCP && iface.IP != "" {
			gatewayIP = iface.IP
			break
		}
	}

	content := `# PisoWiFi DHCP config (isc-dhcp-server) — see INSTALL.md.
# Must match the static IP given to the primary interface.

ddns-update-style none;
authoritative;

default-lease-time 43200;   # 12h
max-lease-time 86400;       # 24h

`

	// Use configured subnet mask (default: 255.255.240.0 for 10.0.0.0/20 network)
	subnetMask := cfg.DHCPSubnetMask
	if subnetMask == "" {
		subnetMask = "255.255.240.0"
	}

	content += fmt.Sprintf("subnet 10.0.0.0 netmask %s {\n", subnetMask)

	if cfg.DHCPRangeStart != "" && cfg.DHCPRangeEnd != "" {
		content += fmt.Sprintf("  range %s %s;\n", cfg.DHCPRangeStart, cfg.DHCPRangeEnd)
	}
	if gatewayIP != "" {
		content += fmt.Sprintf("  option routers %s;\n", gatewayIP)
	}
	if len(cfg.DNSServers) > 0 {
		content += fmt.Sprintf("  option domain-name-servers %s;\n", strings.Join(cfg.DNSServers, ", "))
	}
	if cfg.DHCPSubnetMask != "" {
		content += fmt.Sprintf("  option subnet-mask %s;\n", cfg.DHCPSubnetMask)
	}
	if cfg.DHCPBroadcastAddr != "" {
		content += fmt.Sprintf("  option broadcast-address %s;\n", cfg.DHCPBroadcastAddr)
	}

	content += "}\n"

	// Add DHCP hook for lease tracking
	content += "\n# DHCP hook: notifies the foswvs-go API of lease commits\n"
	content += "on commit {\n"
	content += "  execute(\"/home/pi/foswvs-go/api/dhcp_hook\", binary-to-ascii(10,8,\".\",leased-address), binary-to-ascii(16,8,\":\",substring(hardware,1,6)), pick-first-value(option host-name,\"-NA-\"));\n"
	content += "}\n"

	// Write to project conf dir
	confPath := filepath.Join(dataDir, "conf", "dhcpd.conf")
	if err := os.WriteFile(confPath, []byte(content), 0644); err != nil {
		return fmt.Errorf("write conf/dhcpd.conf: %w", err)
	}

	// Write to system location
	if err := os.WriteFile("/etc/dhcp/dhcpd.conf", []byte(content), 0644); err != nil {
		return fmt.Errorf("write /etc/dhcp/dhcpd.conf: %w", err)
	}

	return nil
}

func reloadNetworkServices() error {
	// Reload networking
	if err := exec.Command("systemctl", "reload-or-restart", "networking").Run(); err != nil {
		// Don't fail if systemctl doesn't exist (dev environment)
		if !strings.Contains(err.Error(), "not found") {
			return fmt.Errorf("reload networking: %w", err)
		}
	}

	// Reload DHCP server
	if err := exec.Command("systemctl", "reload-or-restart", "isc-dhcp-server").Run(); err != nil {
		if !strings.Contains(err.Error(), "not found") {
			return fmt.Errorf("reload DHCP: %w", err)
		}
	}

	// Reload hostapd
	if err := exec.Command("systemctl", "reload-or-restart", "hostapd").Run(); err != nil {
		if !strings.Contains(err.Error(), "not found") {
			return fmt.Errorf("reload hostapd: %w", err)
		}
	}

	return nil
}

// ApplyRoleConfiguration applies role-based network configuration.
// This modifies the network interfaces based on assigned roles (WAN, LAN, etc.)
func (n *Net) ApplyRoleConfiguration(cfg *NetworkConfig) error {
	if cfg == nil || len(cfg.Roles) == 0 {
		return nil // No roles to apply
	}

	// Find WAN interface (internet source)
	var wanRole *InterfaceRole
	for i := range cfg.Roles {
		if cfg.Roles[i].Role == "wan" {
			wanRole = &cfg.Roles[i]
			break
		}
	}

	// Find LAN interface (local gateway/DHCP)
	var lanRole *InterfaceRole
	for i := range cfg.Roles {
		if cfg.Roles[i].Role == "lan" && cfg.Roles[i].Primary {
			lanRole = &cfg.Roles[i]
			break
		}
	}

	// Update interface configurations based on roles
	for i := range cfg.Interfaces {
		iface := &cfg.Interfaces[i]

		// WAN interface: typically gets IP from ISP (DHCP or static)
		if wanRole != nil && iface.Name == wanRole.InterfaceName {
			iface.Enabled = true
			// Keep existing DHCP setting or use DHCP by default for WAN
			if iface.IP == "" && iface.DHCP == false {
				iface.DHCP = true
			}
		}

		// LAN interface: gets static IP from DHCP server's network
		if lanRole != nil && iface.Name == lanRole.InterfaceName {
			iface.Enabled = true
			iface.DHCP = false
			// If not already configured, use the primary network IP
			if iface.IP == "" {
				iface.IP = "10.0.0.1"
				iface.Netmask = "255.255.255.0"
			}
			cfg.PrimaryNetwork = iface.Name
		}
	}

	return nil
}

// GetConfigurationForRole returns the interface configuration for a specific role.
func (n *Net) GetConfigurationForRole(cfg *NetworkConfig, role string) *InterfaceConfig {
	if cfg == nil || len(cfg.Roles) == 0 {
		return nil
	}

	for _, roleAssignment := range cfg.Roles {
		if roleAssignment.Role == role {
			for i := range cfg.Interfaces {
				if cfg.Interfaces[i].Name == roleAssignment.InterfaceName {
					return &cfg.Interfaces[i]
				}
			}
		}
	}

	return nil
}

// GenerateRoleBasedDHCPConfig generates DHCP configuration for a given primary interface.
// This is typically the LAN interface serving DHCP to local clients.
func GenerateRoleBasedDHCPConfig(lanIface *InterfaceConfig, dnsServers []string) string {
	if lanIface == nil {
		return ""
	}

	// Derive DHCP range from interface IP
	rangeStart := deriveSubnetAddress(lanIface.IP, lanIface.Netmask, 10)
	rangeEnd := deriveSubnetAddress(lanIface.IP, lanIface.Netmask, 250)
	broadcastAddr := deriveSubnetAddress(lanIface.IP, lanIface.Netmask, 255)

	content := fmt.Sprintf(`# DHCP config for %s (LAN interface)
# Generated for role-based configuration

ddns-update-style none;
authoritative;

default-lease-time 43200;   # 12h
max-lease-time 86400;       # 24h

subnet %s netmask %s {
`, lanIface.Name, deriveSubnetAddress(lanIface.IP, lanIface.Netmask, 0), lanIface.Netmask)

	content += fmt.Sprintf("  range %s %s;\n", rangeStart, rangeEnd)
	content += fmt.Sprintf("  option routers %s;\n", lanIface.IP)

	if len(dnsServers) > 0 {
		content += fmt.Sprintf("  option domain-name-servers %s;\n", strings.Join(dnsServers, ", "))
	}

	content += fmt.Sprintf("  option subnet-mask %s;\n", lanIface.Netmask)
	content += fmt.Sprintf("  option broadcast-address %s;\n", broadcastAddr)
	content += "}\n"

	// Add DHCP hook for lease tracking
	content += "\n# DHCP hook: notifies the foswvs-go API of lease commits\n"
	content += "on commit {\n"
	content += "  execute(\"/home/pi/foswvs-go/api/dhcp_hook\", binary-to-ascii(10,8,\".\",leased-address), binary-to-ascii(16,8,\":\",substring(hardware,1,6)), pick-first-value(option host-name,\"-NA-\"));\n"
	content += "}\n"

	return content
}

// deriveSubnetAddress derives a specific address in a subnet (e.g., network, gateway, broadcast).
// hostPart is the host part of the address (0 for network, 255 for broadcast, etc.)
func deriveSubnetAddress(ip, netmask string, hostPart int) string {
	ipAddr := net.ParseIP(ip)
	maskAddr := net.ParseIP(netmask)

	if ipAddr == nil || maskAddr == nil {
		return ""
	}

	ipv4 := ipAddr.To4()
	maskv4 := maskAddr.To4()

	if ipv4 == nil || maskv4 == nil {
		return ""
	}

	// Calculate network address
	network := make(net.IP, 4)
	for i := 0; i < 4; i++ {
		network[i] = ipv4[i] & maskv4[i]
	}

	// Set host part
	network[3] = byte(hostPart)

	return network.String()
}
