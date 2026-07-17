package bandwidth

import (
	"bytes"
	"fmt"
	"log"
	"os/exec"
)

// Shaper manages tc-based bandwidth limits.
type Shaper struct {
	iface        string
	tcConfig     TrafficControlConfig
	activeIPsMap map[string]bool // track active IPs for dynamic mode
}

func New(iface string) *Shaper {
	return &Shaper{iface: iface, activeIPsMap: make(map[string]bool)}
}

// NewWithConfig creates a Shaper with traffic control config
func NewWithConfig(iface string, dataDir string) *Shaper {
	tcConfig := LoadTrafficControlConfig(dataDir)

	// Use interface from config if set and available, otherwise use provided or auto-detect
	if tcConfig.InterfaceName != "" {
		if err := ValidateInterface(tcConfig.InterfaceName); err == nil {
			iface = tcConfig.InterfaceName
		}
	}

	// If no valid interface, auto-select
	if iface == "" || iface == "eth0" {
		iface = SelectInterfaceForTrafficControl(iface)
	}

	tcConfig.InterfaceName = iface
	return &Shaper{iface: iface, tcConfig: tcConfig, activeIPsMap: make(map[string]bool)}
}

// Apply sets download/upload limits in Kbps using configured qdisc (htb or cake).
func (s *Shaper) Apply(dspeedKbps, uspeedKbps int) error {
	_ = s.Clear() // ignore error on first run

	// Choose qdisc based on config
	if s.tcConfig.QdiscType == "cake" {
		return s.applyCake(dspeedKbps, uspeedKbps)
	}
	return s.applyHTB(dspeedKbps, uspeedKbps)
}

// applyHTB sets up bandwidth limits using HTB (Hierarchical Token Bucket).
func (s *Shaper) applyHTB(dspeedKbps, uspeedKbps int) error {
	if uspeedKbps > 0 {
		cmds := [][]string{
			{"qdisc", "add", "dev", s.iface, "root", "handle", "1:", "htb", "default", "20"},
			{"class", "add", "dev", s.iface, "parent", "1:", "classid", "1:1", "htb",
				"rate", fmt.Sprintf("%dkbit", uspeedKbps)},
			{"class", "add", "dev", s.iface, "parent", "1:1", "classid", "1:20", "htb",
				"rate", fmt.Sprintf("%dkbit", uspeedKbps*40/100),
				"ceil", fmt.Sprintf("%dkbit", uspeedKbps*95/100)},
			{"qdisc", "add", "dev", s.iface, "parent", "1:20", "handle", "20:", "sfq", "perturb", "10"},
			{"filter", "add", "dev", s.iface, "parent", "1:", "protocol", "ip", "prio", "18",
				"u32", "match", "ip", "dst", "0.0.0.0/0", "flowid", "1:20"},
		}
		for _, args := range cmds {
			if err := tc(args...); err != nil {
				return fmt.Errorf("tc uplink %v: %w", args, err)
			}
		}
	}

	if dspeedKbps > 0 {
		cmds := [][]string{
			{"qdisc", "add", "dev", s.iface, "handle", "ffff:", "ingress"},
			{"filter", "add", "dev", s.iface, "parent", "ffff:", "protocol", "ip",
				"u32", "match", "u32", "0", "0", "action", "mirred", "egress", "redirect", "dev", "ifb0"},
		}
		// Set up ifb
		if err := exec.Command("sudo", "modprobe", "ifb", "numifbs=1").Run(); err != nil {
			log.Printf("modprobe ifb failed: %v", err)
		}
		if err := exec.Command("sudo", "ip", "link", "set", "dev", "ifb0", "up").Run(); err != nil {
			log.Printf("ip link ifb0 up failed: %v", err)
		}

		for _, args := range cmds {
			if err := tc(args...); err != nil {
				return fmt.Errorf("tc downlink %v: %w", args, err)
			}
		}
		// Shape on ifb0
		shaperCmds := [][]string{
			{"qdisc", "add", "dev", "ifb0", "root", "handle", "2:", "htb"},
			{"class", "add", "dev", "ifb0", "parent", "2:", "classid", "2:1", "htb",
				"rate", fmt.Sprintf("%dkbit", dspeedKbps)},
			{"filter", "add", "dev", "ifb0", "protocol", "ip", "parent", "2:", "prio", "1",
				"u32", "match", "ip", "src", "0.0.0.0/0", "flowid", "2:1"},
		}
		for _, args := range shaperCmds {
			if err := tc(args...); err != nil {
				return fmt.Errorf("tc ifb0 shaper %v: %w", args, err)
			}
		}
	}

	return nil
}

// applyCake sets up bandwidth limits using CAKE (Common Applications Kept Enhanced).
// CAKE provides simpler configuration and better per-flow fairness than HTB.
func (s *Shaper) applyCake(dspeedKbps, uspeedKbps int) error {
	log.Printf("applying CAKE to interface %s (upload: %d Kbps, download: %d Kbps)", s.iface, uspeedKbps, dspeedKbps)

	// Upload shaping (egress on main interface)
	if uspeedKbps > 0 {
		args := []string{
			"qdisc", "add", "dev", s.iface, "root", "cake",
			"bandwidth", fmt.Sprintf("%dkbit", uspeedKbps),
			"overhead", fmt.Sprintf("%d", s.tcConfig.OverheadBytes),
			"mpu", "64",
			"besteffort",
		}
		if err := tc(args...); err != nil {
			log.Printf("CAKE upload setup failed on %s: %v", s.iface, err)
			return fmt.Errorf("tc cake upload on %s: %w", s.iface, err)
		}
		log.Printf("CAKE upload configured on %s: %d Kbps", s.iface, uspeedKbps)
	}

	// Download shaping (ingress) - only if enabled
	if dspeedKbps > 0 {
		if !s.tcConfig.EnableIngress {
			log.Printf("CAKE download shaping disabled (EnableIngress is false)")
		}
	}
	if dspeedKbps > 0 && s.tcConfig.EnableIngress {
		log.Printf("setting up ingress on ifb0 for download shaping")
		cmds := [][]string{
			{"qdisc", "add", "dev", s.iface, "handle", "ffff:", "ingress"},
			{"filter", "add", "dev", s.iface, "parent", "ffff:", "protocol", "ip",
				"u32", "match", "u32", "0", "0", "action", "mirred", "egress", "redirect", "dev", "ifb0"},
		}

		// Set up ifb
		if err := exec.Command("sudo", "modprobe", "ifb", "numifbs=1").Run(); err != nil {
			log.Printf("modprobe ifb failed: %v", err)
		}
		if err := exec.Command("sudo", "ip", "link", "set", "dev", "ifb0", "up").Run(); err != nil {
			log.Printf("ip link ifb0 up failed: %v", err)
		}

		for _, args := range cmds {
			if err := tc(args...); err != nil {
				log.Printf("ingress setup failed: %v", err)
				return fmt.Errorf("tc cake ingress setup: %w", err)
			}
		}

		// Set up CAKE on ifb0 for download
		ifbArgs := []string{
			"qdisc", "add", "dev", "ifb0", "root", "cake",
			"bandwidth", fmt.Sprintf("%dkbit", dspeedKbps),
			"overhead", fmt.Sprintf("%d", s.tcConfig.OverheadBytes),
			"mpu", "64",
			"besteffort",
			"ingress",
		}
		if err := tc(ifbArgs...); err != nil {
			log.Printf("CAKE download setup failed on ifb0: %v", err)
			return fmt.Errorf("tc cake download on ifb0: %w", err)
		}
		log.Printf("CAKE download configured on ifb0: %d Kbps", dspeedKbps)
	}

	log.Printf("CAKE setup completed successfully on %s", s.iface)
	return nil
}

// Clear removes all tc rules.
func (s *Shaper) Clear() error {
	tc("qdisc", "del", "dev", s.iface, "root")
	tc("qdisc", "del", "dev", s.iface, "ingress")
	tc("qdisc", "del", "dev", "ifb0", "root")
	return nil
}

// TrackIP registers an active IP for dynamic mode bandwidth calculation
func (s *Shaper) TrackIP(ip string) {
	s.activeIPsMap[ip] = true
}

// UntrackIP removes an IP from active tracking
func (s *Shaper) UntrackIP(ip string) {
	delete(s.activeIPsMap, ip)
}

// GetActiveIPCount returns the number of currently active IPs
func (s *Shaper) GetActiveIPCount() int {
	return len(s.activeIPsMap)
}

// GetTrafficControlConfig returns the current traffic control configuration
func (s *Shaper) GetTrafficControlConfig() TrafficControlConfig {
	return s.tcConfig
}

// SetTrafficControlConfig updates and persists traffic control configuration
func (s *Shaper) SetTrafficControlConfig(cfg TrafficControlConfig, dataDir string) error {
	// Validate interface before making any changes
	if cfg.InterfaceName != "" {
		if err := ValidateInterface(cfg.InterfaceName); err != nil {
			return fmt.Errorf("interface validation failed: %w", err)
		}
	} else {
		return fmt.Errorf("interface name is required")
	}

	// Try to apply the configuration first (before saving)
	dspeedKbps := cfg.TotalBandwidthMbps * 1000
	uspeedKbps := cfg.TotalBandwidthMbps * 1000

	// Update interface and config for Apply()
	oldIface := s.iface
	s.iface = cfg.InterfaceName
	s.tcConfig = cfg

	if err := s.Apply(dspeedKbps, uspeedKbps); err != nil {
		// Revert changes if Apply fails
		s.iface = oldIface
		return fmt.Errorf("apply failed: %w", err)
	}

	// Only save if Apply succeeded
	if err := SaveTrafficControlConfig(dataDir, cfg); err != nil {
		return fmt.Errorf("save failed: %w", err)
	}

	return nil
}

// CalculateDynamicPerIPLimit calculates per-IP limit in Mbps based on active users and total capacity,
// respecting the maximum dynamic Mbps cap.
func (s *Shaper) CalculateDynamicPerIPLimit(totalCapacityMbps int) int {
	activeCount := s.GetActiveIPCount()
	if activeCount == 0 {
		return totalCapacityMbps
	}
	// Allocate bandwidth fairly across active IPs
	perIPLimit := totalCapacityMbps / activeCount
	if perIPLimit < 1 {
		perIPLimit = 1
	}
	// Apply maximum cap from config
	if s.tcConfig.MaximumDynamicMbps > 0 && perIPLimit > s.tcConfig.MaximumDynamicMbps {
		perIPLimit = s.tcConfig.MaximumDynamicMbps
	}
	return perIPLimit
}

// GetEffectivePerIPLimit returns the per-IP limit in Mbps using dynamic fair use
func (s *Shaper) GetEffectivePerIPLimit(totalCapacityMbps int) int {
	return s.CalculateDynamicPerIPLimit(totalCapacityMbps)
}

// RestoreSavedConfiguration applies the traffic control configuration that was saved on disk.
// This is called on application startup to restore previous settings.
func (s *Shaper) RestoreSavedConfiguration() error {
	if s.tcConfig.TotalBandwidthMbps <= 0 {
		log.Printf("no saved bandwidth configuration to restore")
		return nil
	}
	dspeedKbps := s.tcConfig.TotalBandwidthMbps * 1000
	uspeedKbps := s.tcConfig.TotalBandwidthMbps * 1000
	log.Printf("restoring traffic control configuration: %d Mbps on interface %s", s.tcConfig.TotalBandwidthMbps, s.iface)
	return s.Apply(dspeedKbps, uspeedKbps)
}

func tc(args ...string) error {
	var stderr bytes.Buffer
	cmd := exec.Command("sudo", append([]string{"tc"}, args...)...)
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		errMsg := stderr.String()
		if errMsg != "" {
			return fmt.Errorf("tc command failed: %v (stderr: %s)", err, errMsg)
		}
		return fmt.Errorf("tc command failed: %v", err)
	}
	return nil
}
