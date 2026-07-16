package iptables

import (
	"fmt"
	"net"
	"os/exec"
	"strings"
	"sync"
)

// IPT manages iptables rules for client internet access.
type IPT struct {
	mu sync.Mutex
}

func New() *IPT {
	return &IPT{}
}

// validateIP ensures the IP is a valid IPv4 in the 10.0.0.0/12 subnet.
func validateIP(ip string) error {
	parsed := net.ParseIP(ip)
	if parsed == nil || parsed.To4() == nil {
		return fmt.Errorf("invalid IPv4: %s", ip)
	}
	_, subnet, _ := net.ParseCIDR("10.0.0.0/12")
	if !subnet.Contains(parsed) {
		return fmt.Errorf("IP %s not in managed subnet", ip)
	}
	return nil
}

func run(args ...string) error {
	cmd := exec.Command("sudo", append([]string{"iptables"}, args...)...)
	return cmd.Run()
}

func check(args ...string) bool {
	cmd := exec.Command("sudo", append([]string{"iptables"}, args...)...)
	return cmd.Run() == nil
}

// AddClient opens FORWARD + PREROUTING for the given IP.
func (ipt *IPT) AddClient(ip string) error {
	if err := validateIP(ip); err != nil {
		return err
	}

	ipt.mu.Lock()
	defer ipt.mu.Unlock()

	// PREROUTING ACCEPT (skip captive portal redirect)
	if !check("-t", "nat", "-C", "PREROUTING", "-s", ip, "-j", "ACCEPT") {
		if err := run("-t", "nat", "-I", "PREROUTING", "-s", ip, "-j", "ACCEPT"); err != nil {
			return fmt.Errorf("nat add: %w", err)
		}
	}

	// FORWARD in+out
	if !check("-C", "FORWARD", "-s", ip, "-j", "ACCEPT") {
		if err := run("-A", "FORWARD", "-s", ip, "-j", "ACCEPT"); err != nil {
			return fmt.Errorf("forward add src: %w", err)
		}
		if err := run("-A", "FORWARD", "-d", ip, "-j", "ACCEPT"); err != nil {
			return fmt.Errorf("forward add dst: %w", err)
		}
	}

	return nil
}

// RemoveClient removes all FORWARD + PREROUTING rules for the IP.
func (ipt *IPT) RemoveClient(ip string) error {
	if err := validateIP(ip); err != nil {
		return err
	}

	ipt.mu.Lock()
	defer ipt.mu.Unlock()

	// Remove all PREROUTING rules for this IP
	for check("-t", "nat", "-C", "PREROUTING", "-s", ip, "-j", "ACCEPT") {
		if err := run("-t", "nat", "-D", "PREROUTING", "-s", ip, "-j", "ACCEPT"); err != nil {
			return fmt.Errorf("nat del: %w", err)
		}
	}

	// Remove all FORWARD rules for this IP
	for check("-C", "FORWARD", "-s", ip, "-j", "ACCEPT") {
		if err := run("-D", "FORWARD", "-s", ip, "-j", "ACCEPT"); err != nil {
			return fmt.Errorf("forward del src: %w", err)
		}
	}
	for check("-C", "FORWARD", "-d", ip, "-j", "ACCEPT") {
		if err := run("-D", "FORWARD", "-d", ip, "-j", "ACCEPT"); err != nil {
			return fmt.Errorf("forward del dst: %w", err)
		}
	}

	return nil
}

// IsConnected checks if FORWARD rules exist for the IP.
func (ipt *IPT) IsConnected(ip string) bool {
	if err := validateIP(ip); err != nil {
		return false
	}
	return check("-C", "FORWARD", "-s", ip, "-j", "ACCEPT")
}

// GetForwardByteCounters parses iptables FORWARD chain for byte counts per IP.
// Returns map[ip] => bytes.
func (ipt *IPT) GetForwardByteCounters() (map[string]int64, error) {
	ipt.mu.Lock()
	defer ipt.mu.Unlock()

	cmd := exec.Command("sudo", "iptables", "-nvxL", "FORWARD", "--line-numbers", "-Z")
	out, err := cmd.Output()
	if err != nil {
		return nil, err
	}

	result := make(map[string]int64)
	lines := strings.Split(string(out), "\n")

	for _, line := range lines {
		fields := strings.Fields(line)
		if len(fields) < 10 {
			continue
		}

		// Skip header lines
		if fields[0] == "num" || fields[0] == "Chain" || strings.HasPrefix(line, "Zeroing") {
			continue
		}

		bytes := parseInt64(fields[2])
		if bytes == 0 {
			continue
		}

		src := fields[8]
		dst := fields[9]

		// Attribute traffic to the 10.0.x.x IP
		var clientIP string
		if validateIP(src) == nil {
			clientIP = src
		} else if validateIP(dst) == nil {
			clientIP = dst
		}
		if clientIP != "" {
			result[clientIP] += bytes
		}
	}

	return result, nil
}

func parseInt64(s string) int64 {
	var n int64
	for _, c := range s {
		if c >= '0' && c <= '9' {
			n = n*10 + int64(c-'0')
		}
	}
	return n
}

// RestoreBaseRules loads the default iptables rules for the captive portal.
func RestoreBaseRules() error {
	cmds := [][]string{
		// Default FORWARD DROP
		{"-P", "FORWARD", "DROP"},
		// Block traffic to private networks
		{"-I", "FORWARD", "-p", "tcp", "-d", "192.168.0.0/16", "-j", "REJECT"},
		// NAT: redirect DNS
		{"-t", "nat", "-A", "PREROUTING", "-p", "udp", "--dport", "53", "-j", "REDIRECT"},
		{"-t", "nat", "-A", "PREROUTING", "-p", "tcp", "--dport", "53", "-j", "REDIRECT"},		
		// NAT: captive portal redirect for HTTP/HTTPS
		{"-t", "nat", "-A", "PREROUTING", "-p", "tcp", "--dport", "80", "-j", "DNAT", "--to-destination", "10.0.0.1"},	
		{"-t", "nat", "-A", "PREROUTING", "-p", "tcp", "--dport", "443", "-j", "DNAT", "--to-destination", "10.0.0.1"},	
		// NAT: masquerade outbound
		{"-t", "nat", "-A", "POSTROUTING", "-j", "MASQUERADE"},
	}

	for _, args := range cmds {
		if err := run(args...); err != nil {
			return fmt.Errorf("iptables %v: %w", args, err)
		}
	}
	return nil
}
