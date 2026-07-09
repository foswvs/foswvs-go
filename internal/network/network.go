package network

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Net provides network utility functions.
type Net struct {
	Iface string
}

func New(iface string) *Net {
	return &Net{Iface: iface}
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

func (n *Net) DHCPLeases() []Lease {
	// Try to read from common lease file locations
	paths := []string{
		"/var/lib/dhcp/dhcpd.leases",
		"/var/lib/dhcpd/dhcpd.leases",
	}

	for _, p := range paths {
		if leases := parseDHCPLeases(p); len(leases) > 0 {
			return leases
		}
	}

	// Fallback to ARP-based discovery
	return n.arpBasedLeases()
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

func parseDHCPLeases(path string) []Lease {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}

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
