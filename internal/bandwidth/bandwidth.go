package bandwidth

import (
	"fmt"
	"os/exec"
)

// Shaper manages tc-based bandwidth limits.
type Shaper struct {
	iface string
}

func New(iface string) *Shaper {
	return &Shaper{iface: iface}
}

// Apply sets download/upload limits in Kbps using tc/htb.
func (s *Shaper) Apply(dspeedKbps, uspeedKbps int) error {
	_ = s.Clear() // ignore error on first run

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
		exec.Command("sudo", "modprobe", "ifb", "numifbs=1").Run()
		exec.Command("sudo", "ip", "link", "set", "dev", "ifb0", "up").Run()

		for _, args := range cmds {
			if err := tc(args...); err != nil {
				return fmt.Errorf("tc downlink %v: %w", args, err)
			}
		}
		// Shape on ifb0
		tc("qdisc", "add", "dev", "ifb0", "root", "handle", "2:", "htb")
		tc("class", "add", "dev", "ifb0", "parent", "2:", "classid", "2:1", "htb",
			"rate", fmt.Sprintf("%dkbit", dspeedKbps))
		tc("filter", "add", "dev", "ifb0", "protocol", "ip", "parent", "2:", "prio", "1",
			"u32", "match", "ip", "src", "0.0.0.0/0", "flowid", "2:1")
	}

	return nil
}

// Clear removes all tc rules.
func (s *Shaper) Clear() error {
	tc("qdisc", "del", "dev", s.iface, "root")
	tc("qdisc", "del", "dev", s.iface, "ingress")
	tc("qdisc", "del", "dev", "ifb0", "root")
	return nil
}

func tc(args ...string) error {
	return exec.Command("sudo", append([]string{"tc"}, args...)...).Run()
}
