package cmd

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"strings"
	"time"
)

// Docker's built-in default address pools hand out 15 × /16 (from 172.17/12)
// plus 16 × /20 (from 192.168/16) — a hard ceiling of 31 networks. Poof gives
// every project its own network, so a server crosses that ceiling at ~30
// projects and every further deploy fails with "all predefined address pools
// have been fully subnetted".
//
// We replace the defaults with a single /19 carved into /28s: 512 networks of
// 13 usable addresses each. A Poof network holds two members (the project
// container and Caddy), so /28 is generous, and the whole pool occupies 8192
// addresses instead of the ~1M the defaults reserve.
const (
	poolChunkSize = 28
	poolPrefixLen = 19
)

// poolCandidates lists preferred bases, most-likely-free first. 172.16.0.0/16
// is the sweet spot: Docker's own defaults start at 172.17, so this block sits
// inside the range the ecosystem already associates with Docker while being
// conventionally untouched. The 10.x fallbacks avoid 10.0/10.1 (common cloud
// VPC defaults) and are only reached if 172.16 is occupied.
var poolCandidates = []string{
	"172.16.0.0/19", "172.16.32.0/19", "172.16.64.0/19", "172.16.96.0/19",
	"172.16.128.0/19", "172.16.160.0/19", "172.16.192.0/19", "172.16.224.0/19",
	"10.210.0.0/19", "10.211.0.0/19", "10.212.0.0/19",
}

// ensureAddressPools configures Docker's default-address-pools if they haven't
// been set. Idempotent: if the key is already present (whatever its value) it
// leaves the file untouched and does not restart Docker.
func ensureAddressPools() {
	printStep("Checking Docker address pools")

	const path = "/etc/docker/daemon.json"
	cfg := map[string]interface{}{}
	existing, readErr := os.ReadFile(path)
	if readErr == nil && len(strings.TrimSpace(string(existing))) > 0 {
		if err := json.Unmarshal(existing, &cfg); err != nil {
			printWarn(fmt.Sprintf("%s exists but is not valid JSON — leaving it alone", path))
			printWarn("  Configure default-address-pools by hand, or Poof will stop creating networks at ~30 projects.")
			return
		}
	}

	if _, ok := cfg["default-address-pools"]; ok {
		printOK("Docker address pools already configured")
		return
	}

	base := pickFreePool()
	if base == "" {
		printWarn("Could not find a free private range for Docker's address pool — leaving defaults")
		printWarn("  Docker's defaults allow only 31 networks; Poof needs one per project.")
		return
	}

	// Restarting Docker restarts every container on the host. On a fresh
	// install that's free; on a populated one it is not, so ask.
	if n := runningContainerCount(); n > 0 {
		fmt.Printf("  Docker's default address pools allow only 31 networks (Poof uses one per project).\n")
		fmt.Printf("  Fixing this needs a Docker restart, which will restart %d running container(s).\n", n)
		if !promptYN("  Configure address pools now?", true) {
			printWarn("Skipped — deploys will fail once you pass ~30 projects")
			printWarn("  Re-run 'poof install' later, or set default-address-pools in /etc/docker/daemon.json.")
			return
		}
	}

	if readErr == nil {
		backup := path + ".poof-backup"
		if err := os.WriteFile(backup, existing, 0644); err == nil {
			fmt.Printf("  Backed up existing config to %s\n", backup)
		}
	}

	cfg["default-address-pools"] = []map[string]interface{}{
		{"base": base, "size": poolChunkSize},
	}
	out, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		printWarn(fmt.Sprintf("Could not encode %s: %v", path, err))
		return
	}
	if err := os.MkdirAll("/etc/docker", 0755); err != nil {
		printWarn(fmt.Sprintf("Could not create /etc/docker: %v", err))
		return
	}
	if err := os.WriteFile(path, append(out, '\n'), 0644); err != nil {
		printWarn(fmt.Sprintf("Could not write %s: %v", path, err))
		return
	}

	if err := restartDocker(); err != nil {
		printWarn(fmt.Sprintf("Wrote %s but could not restart Docker: %v", path, err))
		printWarn("  Restart Docker manually for the new address pool to take effect.")
		return
	}
	printOK(fmt.Sprintf("Docker address pool set to %s in /%d blocks (512 networks)", base, poolChunkSize))
}

// pickFreePool returns the first candidate range that doesn't overlap any
// subnet already in use by a Docker network or a host route.
func pickFreePool() string {
	inUse := usedSubnets()
	for _, c := range poolCandidates {
		_, candidate, err := net.ParseCIDR(c)
		if err != nil {
			continue
		}
		free := true
		for _, u := range inUse {
			if netsOverlap(candidate, u) {
				free = false
				break
			}
		}
		if free {
			return c
		}
	}
	return ""
}

// usedSubnets collects subnets from existing Docker networks and the host
// routing table, so we never hand Docker a range that collides with a VPC,
// VPN, or LAN the operator already depends on.
func usedSubnets() []*net.IPNet {
	var out []*net.IPNet
	add := func(s string) {
		if _, n, err := net.ParseCIDR(strings.TrimSpace(s)); err == nil {
			out = append(out, n)
		}
	}

	if raw, err := exec.Command("docker", "network", "ls", "--format", "{{.Name}}").Output(); err == nil {
		for _, name := range strings.Fields(string(raw)) {
			sub, err := exec.Command("docker", "network", "inspect", "-f",
				`{{range .IPAM.Config}}{{.Subnet}} {{end}}`, name).Output()
			if err != nil {
				continue
			}
			for _, s := range strings.Fields(string(sub)) {
				add(s)
			}
		}
	}

	if raw, err := exec.Command("ip", "-4", "route").Output(); err == nil {
		for _, line := range strings.Split(string(raw), "\n") {
			fields := strings.Fields(line)
			if len(fields) > 0 && strings.Contains(fields[0], "/") {
				add(fields[0])
			}
		}
	}
	return out
}

// netsOverlap reports whether two CIDR blocks intersect at all.
func netsOverlap(a, b *net.IPNet) bool {
	return a.Contains(b.IP) || b.Contains(a.IP)
}

func runningContainerCount() int {
	out, err := exec.Command("docker", "ps", "-q").Output()
	if err != nil {
		return 0
	}
	return len(strings.Fields(string(out)))
}

// restartDocker restarts the daemon and waits for it to accept commands again.
func restartDocker() error {
	var lastErr error
	for _, args := range [][]string{
		{"systemctl", "restart", "docker"},
		{"service", "docker", "restart"},
	} {
		if _, err := exec.LookPath(args[0]); err != nil {
			continue
		}
		if out, err := exec.Command(args[0], args[1:]...).CombinedOutput(); err != nil {
			lastErr = fmt.Errorf("%s: %s", args[0], strings.TrimSpace(string(out)))
			continue
		}
		for i := 0; i < 60; i++ {
			if err := exec.Command("docker", "info").Run(); err == nil {
				return nil
			}
			time.Sleep(2 * time.Second)
		}
		return fmt.Errorf("docker did not come back within 120s")
	}
	if lastErr != nil {
		return lastErr
	}
	return fmt.Errorf("no init system found (tried systemctl and service)")
}
