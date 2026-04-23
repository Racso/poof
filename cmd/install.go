package cmd

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
)

var installCmd = &cobra.Command{
	Use:   "install",
	Short: "Set up a Poof! server on this machine",
	Long: `Install sets up a complete Poof! server environment:

  1. Verifies Docker is installed and running
  2. Checks port 80/443 availability (detects existing web servers)
  3. Starts a Caddy container (or reuses an existing one)
  4. Builds the Poof! Docker image from this binary
  5. Generates a config with a random auth token
  6. Starts the Poof! container

Requires Docker. Everything else is handled automatically.`,
	Run: runInstall,
}

func init() {
	rootCmd.AddCommand(installCmd)
	installCmd.Flags().Bool("yes", false, "accept all defaults, no prompts")
	installCmd.Flags().String("use-caddy", "", "use an existing Caddy container by name")
	installCmd.Flags().String("token", "", "use this auth token instead of generating one")
	installCmd.Flags().String("domain", "", "domain for the Poof! API (e.g. poof.example.com)")
}

func runInstall(cmd *cobra.Command, args []string) {
	yes, _ := cmd.Flags().GetBool("yes")
	useCaddy, _ := cmd.Flags().GetString("use-caddy")
	tokenFlag, _ := cmd.Flags().GetString("token")
	domain, _ := cmd.Flags().GetString("domain")

	// ── 1. Check Docker ──────────────────────────────────────────────
	printStep("Checking Docker")

	if _, err := exec.LookPath("docker"); err != nil {
		printFail("Docker is not installed")
		fmt.Fprintln(os.Stderr, "")
		fmt.Fprintln(os.Stderr, "  Install it with:")
		fmt.Fprintln(os.Stderr, "    curl -fsSL https://get.docker.com | sh")
		fmt.Fprintln(os.Stderr, "")
		fmt.Fprintln(os.Stderr, "  Then re-run: poof install")
		os.Exit(1)
	}

	if err := exec.Command("docker", "info").Run(); err != nil {
		printFail("Docker is not running")
		fmt.Fprintln(os.Stderr, "")
		fmt.Fprintln(os.Stderr, "  Start it with:")
		fmt.Fprintln(os.Stderr, "    sudo systemctl start docker")
		fmt.Fprintln(os.Stderr, "")
		fmt.Fprintln(os.Stderr, "  Then re-run: poof install")
		os.Exit(1)
	}

	printOK("Docker is running")

	// ── 2. Check ports 80/443 ────────────────────────────────────────
	printStep("Checking ports 80 and 443")

	caddyContainer := useCaddy
	skipCaddySetup := false

	if caddyContainer != "" {
		// User explicitly named a Caddy container to reuse.
		skipCaddySetup = true
	} else {
		for _, port := range []string{"80", "443"} {
			proc, containerName := checkPort(port)
			if proc == "" {
				continue // port is free
			}

			if containerName != "" {
				// Port is used by a Docker container. Could be Caddy.
				caddyContainer = containerName
				skipCaddySetup = true
				break
			}

			// Port used by a non-Docker process.
			stopHint := stopCommand(proc)
			printFail(fmt.Sprintf("Port %s is in use by %s", port, proc))
			fmt.Fprintln(os.Stderr, "")
			if stopHint != "" {
				fmt.Fprintf(os.Stderr, "  Stop it first:\n    %s\n", stopHint)
			} else {
				fmt.Fprintln(os.Stderr, "  Free the port and re-run: poof install")
			}
			fmt.Fprintln(os.Stderr, "")
			os.Exit(1)
		}
	}

	// ── 3. Existing Caddy container ──────────────────────────────────
	if skipCaddySetup {
		printStep(fmt.Sprintf("Found existing container %q on ports 80/443", caddyContainer))

		issues := validateCaddyContainer(caddyContainer)
		if len(issues) > 0 {
			printFail(fmt.Sprintf("Container %q needs changes to work with Poof!", caddyContainer))
			fmt.Fprintln(os.Stderr, "")
			for _, issue := range issues {
				fmt.Fprintf(os.Stderr, "  - %s\n", issue)
			}
			fmt.Fprintln(os.Stderr, "")
			fmt.Fprintln(os.Stderr, "  Fix these and re-run, or let Poof! set up its own Caddy:")
			fmt.Fprintf(os.Stderr, "    docker stop %s && poof install\n", caddyContainer)
			fmt.Fprintln(os.Stderr, "")
			os.Exit(1)
		}

		if !yes {
			if !promptYN(fmt.Sprintf("Use existing container %q for Caddy? [Y/n] ", caddyContainer), true) {
				fmt.Println("Aborted. Stop the container and re-run to let Poof! set up its own Caddy.")
				os.Exit(1)
			}
		}

		printOK(fmt.Sprintf("Using existing Caddy container %q", caddyContainer))
	} else {
		printOK("Ports 80 and 443 are free")
	}

	// ── 4. Create poof-net ───────────────────────────────────────────
	printStep("Setting up Docker network")

	if networkExists("poof-net") {
		printOK("Network poof-net already exists")
	} else {
		if out, err := exec.Command("docker", "network", "create", "poof-net").CombinedOutput(); err != nil {
			printFail("Failed to create network poof-net")
			fmt.Fprintf(os.Stderr, "  %s\n", strings.TrimSpace(string(out)))
			os.Exit(1)
		}
		printOK("Created network poof-net")
	}

	// ── 5. Start Caddy (if needed) ───────────────────────────────────
	if !skipCaddySetup {
		printStep("Starting Caddy")

		// Create a minimal Caddyfile with admin API enabled.
		caddyfileDir := "/etc/caddy/conf.d"
		os.MkdirAll(caddyfileDir, 0755)

		caddyfilePath := "/opt/caddy/Caddyfile"
		os.MkdirAll(filepath.Dir(caddyfilePath), 0755)
		caddyfileContent := "{\n\tadmin 0.0.0.0:2019\n}\nimport /etc/caddy/conf.d/*.Caddyfile\n"
		if err := os.WriteFile(caddyfilePath, []byte(caddyfileContent), 0644); err != nil {
			printFail(fmt.Sprintf("Failed to write Caddyfile: %v", err))
			os.Exit(1)
		}

		// Create static dir for Poof.
		os.MkdirAll("/var/lib/poof/static", 0755)

		out, err := exec.Command("docker", "run", "-d",
			"--name", "caddy-proxy",
			"--restart", "always",
			"--network", "poof-net",
			"-p", "80:80",
			"-p", "443:443",
			"-v", "caddy_data:/data",
			"-v", caddyfilePath+":/etc/caddy/Caddyfile:ro",
			"-v", caddyfileDir+":"+caddyfileDir+":ro",
			"-v", "/var/lib/poof/static:/var/lib/poof/static:ro",
			"caddy:alpine",
		).CombinedOutput()
		if err != nil {
			printFail("Failed to start Caddy container")
			fmt.Fprintf(os.Stderr, "  %s\n", strings.TrimSpace(string(out)))
			os.Exit(1)
		}
		caddyContainer = "caddy-proxy"
		printOK("Caddy is running")
	}

	// ── 6. Ask for Poof! API domain ──────────────────────────────────
	if domain == "" && !yes {
		fmt.Println("")
		fmt.Println("  What domain will the Poof! API be reachable at?")
		fmt.Println("  (e.g. poof.example.com — your CLI will connect to this)")
		fmt.Println("")
		fmt.Print("  Domain: ")
		reader := bufio.NewReader(os.Stdin)
		answer, _ := reader.ReadString('\n')
		domain = strings.TrimSpace(answer)
	}

	// Normalize: strip protocol prefix if pasted.
	domain = strings.TrimPrefix(domain, "https://")
	domain = strings.TrimPrefix(domain, "http://")
	domain = strings.TrimRight(domain, "/")

	publicURL := ""
	if domain != "" {
		publicURL = "https://" + domain
	}

	// ── 7. Build Poof! Docker image ─────────────────────────────────
	printStep("Building Poof! Docker image")

	exe, err := os.Executable()
	if err != nil {
		printFail(fmt.Sprintf("Cannot determine executable path: %v", err))
		os.Exit(1)
	}
	exe, _ = filepath.EvalSymlinks(exe)

	tmpDir, err := os.MkdirTemp("", "poof-install-*")
	if err != nil {
		printFail(fmt.Sprintf("Cannot create temp dir: %v", err))
		os.Exit(1)
	}
	defer os.RemoveAll(tmpDir)

	// Copy the running binary into the build context.
	if err := copyFile(exe, filepath.Join(tmpDir, "poof")); err != nil {
		printFail(fmt.Sprintf("Cannot copy binary: %v", err))
		os.Exit(1)
	}

	dockerfile := "FROM docker:27-cli\nCOPY poof /usr/local/bin/poof\nENTRYPOINT [\"poof\"]\nCMD [\"server\"]\n"
	if err := os.WriteFile(filepath.Join(tmpDir, "Dockerfile"), []byte(dockerfile), 0644); err != nil {
		printFail(fmt.Sprintf("Cannot write Dockerfile: %v", err))
		os.Exit(1)
	}

	out, err := exec.Command("docker", "build", "-t", "poof:latest", tmpDir, "-q").CombinedOutput()
	if err != nil {
		printFail("Failed to build Docker image")
		fmt.Fprintf(os.Stderr, "  %s\n", strings.TrimSpace(string(out)))
		os.Exit(1)
	}
	printOK("Image poof:latest built")

	// ── 8. Generate config ───────────────────────────────────────────
	printStep("Configuring Poof!")

	token := tokenFlag
	configCreated := false

	if _, err := os.Stat("/etc/poof/poof.toml"); err == nil {
		printOK("Config already exists at /etc/poof/poof.toml")
	} else {
		os.MkdirAll("/etc/poof", 0755)

		if token == "" {
			token = generateToken()
		}

		var configLines []string
		configLines = append(configLines, fmt.Sprintf("token = %q", token))
		if publicURL != "" {
			configLines = append(configLines, fmt.Sprintf("public_url = %q", publicURL))
		}

		if err := os.WriteFile("/etc/poof/poof.toml", []byte(strings.Join(configLines, "\n")+"\n"), 0600); err != nil {
			printFail(fmt.Sprintf("Failed to write config: %v", err))
			os.Exit(1)
		}
		configCreated = true
		printOK("Config written to /etc/poof/poof.toml")
	}

	// ── 9. Start Poof! container ─────────────────────────────────────
	printStep("Starting Poof!")

	os.MkdirAll("/var/lib/poof", 0755)

	// Remove existing container if stopped.
	exec.Command("docker", "rm", "-f", "poof").Run()

	out, err = exec.Command("docker", "run", "-d",
		"--name", "poof",
		"--restart", "always",
		"--network", "poof-net",
		"-p", "127.0.0.1:9000:9000",
		"-v", "/etc/poof/poof.toml:/etc/poof/poof.toml:ro",
		"-v", "/var/lib/poof:/var/lib/poof",
		"-v", "/var/run/docker.sock:/var/run/docker.sock",
		"poof:latest",
	).CombinedOutput()
	if err != nil {
		printFail("Failed to start Poof! container")
		fmt.Fprintf(os.Stderr, "  %s\n", strings.TrimSpace(string(out)))
		os.Exit(1)
	}
	printOK("Poof! is running")

	// ── 10. Print summary ───────────────────────────────────────────
	fmt.Println("")
	fmt.Println("  ✓ Poof! is ready.")
	fmt.Println("")

	if publicURL == "" {
		fmt.Println("  ⚠ No API domain was configured.")
		fmt.Println("    The Poof! API is only reachable from this machine (localhost:9000).")
		fmt.Println("    To allow remote CLI access, set a domain and restart:")
		fmt.Println("")
		fmt.Println("      1. Add  public_url = \"https://poof.example.com\"  to /etc/poof/poof.toml")
		fmt.Println("      2. Point that domain's DNS to this server")
		fmt.Println("      3. docker restart poof")
		fmt.Println("")
	}

	if configCreated {
		fmt.Printf("  API token: %s\n", token)
		fmt.Println("")
	}

	fmt.Println("  From your machine, install the CLI and connect:")
	fmt.Println("")
	fmt.Println("    curl -fsSL https://poof.rac.so/install | sh -s client")
	if publicURL != "" {
		fmt.Printf("    poof config set server %s\n", publicURL)
	} else {
		fmt.Println("    poof config set server https://<your-poof-domain>")
	}
	if configCreated {
		fmt.Printf("    poof config set token  %s\n", token)
	} else {
		fmt.Println("    poof config set token  <your-token>")
	}
	fmt.Println("")
}

// ── Helpers ──────────────────────────────────────────────────────────────

// checkPort returns the process name using the given port (empty if free)
// and the Docker container name if it's a container (empty otherwise).
func checkPort(port string) (procName, containerName string) {
	out, err := exec.Command("ss", "-tlnp", "sport", "=", ":"+port).CombinedOutput()
	if err != nil {
		return "", ""
	}

	lines := strings.Split(string(out), "\n")
	for _, line := range lines {
		if !strings.Contains(line, "LISTEN") {
			continue
		}

		// Extract process name from the users:(...) field.
		if idx := strings.Index(line, "users:"); idx >= 0 {
			field := line[idx:]
			// Format: users:(("name",pid=123,fd=4))
			if start := strings.Index(field, "((\""); start >= 0 {
				rest := field[start+3:]
				if end := strings.Index(rest, "\""); end >= 0 {
					procName = rest[:end]
				}
			}
		}

		if procName == "" {
			continue
		}

		// If the process is docker-proxy, find which container owns it.
		if procName == "docker-proxy" {
			containerName = findContainerOnPort(port)
		}

		return procName, containerName
	}

	return "", ""
}

// findContainerOnPort finds a Docker container publishing the given host port.
func findContainerOnPort(port string) string {
	out, err := exec.Command(
		"docker", "ps", "--format", "{{.Names}}\t{{.Ports}}",
	).Output()
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		parts := strings.SplitN(line, "\t", 2)
		if len(parts) != 2 {
			continue
		}
		// Ports field looks like: 0.0.0.0:80->80/tcp, 0.0.0.0:443->443/tcp
		if strings.Contains(parts[1], ":"+port+"->") {
			return parts[0]
		}
	}
	return ""
}

// stopCommand returns a hint for stopping a known web server process.
func stopCommand(proc string) string {
	switch proc {
	case "caddy":
		return "sudo systemctl stop caddy"
	case "apache2", "httpd":
		return "sudo systemctl stop apache2  # or: sudo systemctl stop httpd"
	case "nginx":
		return "sudo systemctl stop nginx"
	default:
		return ""
	}
}

// validateCaddyContainer checks whether an existing Caddy container is usable
// by Poof and returns a list of issues (empty = all good).
func validateCaddyContainer(name string) []string {
	var issues []string

	// Check if it's on poof-net.
	out, _ := exec.Command(
		"docker", "inspect", "--format",
		`{{range $net, $_ := .NetworkSettings.Networks}}{{$net}} {{end}}`,
		name,
	).Output()
	networks := strings.Fields(strings.TrimSpace(string(out)))
	onPoofNet := false
	for _, n := range networks {
		if n == "poof-net" {
			onPoofNet = true
			break
		}
	}
	if !onPoofNet {
		if networkExists("poof-net") {
			issues = append(issues, "Not on the poof-net network. Recreate with: docker network connect poof-net "+name)
		} else {
			issues = append(issues, "The poof-net Docker network doesn't exist yet (will be created)")
		}
	}

	// Check for static volume mount.
	mountOut, _ := exec.Command(
		"docker", "inspect", "--format",
		`{{range .Mounts}}{{.Destination}} {{end}}`,
		name,
	).Output()
	if !strings.Contains(string(mountOut), "/var/lib/poof/static") {
		issues = append(issues, "Missing volume mount: -v /var/lib/poof/static:/var/lib/poof/static:ro")
	}

	// Check admin API is reachable.
	adminOut, err := exec.Command(
		"docker", "exec", name, "wget", "-qO-", "--timeout=2", "http://localhost:2019/config/",
	).CombinedOutput()
	if err != nil || !strings.Contains(string(adminOut), "admin") {
		issues = append(issues, "Caddy admin API is not enabled. Add to Caddyfile: { admin 0.0.0.0:2019 }")
	}

	return issues
}

// networkExists checks if a Docker network exists.
func networkExists(name string) bool {
	return exec.Command("docker", "network", "inspect", name).Run() == nil
}

// generateToken creates a random 32-byte hex token.
func generateToken() string {
	f, err := os.Open("/dev/urandom")
	if err != nil {
		// Fallback: use a fixed-length read from crypto/rand via od.
		out, _ := exec.Command("od", "-An", "-tx1", "-N32", "/dev/urandom").Output()
		return strings.ReplaceAll(strings.TrimSpace(string(out)), " ", "")
	}
	defer f.Close()
	buf := make([]byte, 32)
	io.ReadFull(f, buf)
	return fmt.Sprintf("%x", buf)
}

// promptYN asks a yes/no question and returns the answer. defaultYes controls
// what happens when the user presses Enter without typing anything.
func promptYN(prompt string, defaultYes bool) bool {
	fmt.Print(prompt)
	reader := bufio.NewReader(os.Stdin)
	answer, _ := reader.ReadString('\n')
	answer = strings.TrimSpace(strings.ToLower(answer))
	if answer == "" {
		return defaultYes
	}
	return strings.HasPrefix(answer, "y")
}

// copyFile copies src to dst.
func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()

	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	return out.Chmod(0755)
}

// ── Output formatting ───────────────────────────────────────────────────

func printStep(msg string) {
	fmt.Printf("  ● %s...\n", msg)
}

func printOK(msg string) {
	fmt.Printf("  ✓ %s\n", msg)
}

func printFail(msg string) {
	fmt.Fprintf(os.Stderr, "  ✗ %s\n", msg)
}
