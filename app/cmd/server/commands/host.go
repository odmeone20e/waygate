package commands

import (
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"
	"syscall"

	"waygate/cmd/server/config"
	"waygate/internal/nodes/types"
	"waygate/internal/routes"
	"waygate/internal/ssh"

	"github.com/spf13/cobra"
	"golang.org/x/term"
)

var HostStartConfigureOnly bool = false

func readPasswordSecurely(prompt string) (string, error) {
	// readPasswordSecurely reads a password from the terminal without echoing
	fmt.Print(prompt)
	bytePassword, err := term.ReadPassword(int(syscall.Stdin))
	fmt.Println() // Add newline after password input
	if err != nil {
		return "", err
	}
	return string(bytePassword), nil
}

// parseSSHURL parses an SSH URL in the format username@hostname:port or username@hostname
// Returns username, hostname, port, and any error
func parseSSHURL(sshURL string) (username, hostname string, port uint, err error) {
	// Default port
	port = 22

	// Check if URL contains port
	if strings.Contains(sshURL, ":") {
		parts := strings.Split(sshURL, ":")
		if len(parts) != 2 {
			return "", "", 0, fmt.Errorf("invalid SSH URL format: %s", sshURL)
		}

		// Parse port
		if portStr := parts[1]; portStr != "" {
			if parsedPort, err := strconv.ParseUint(portStr, 10, 32); err != nil {
				return "", "", 0, fmt.Errorf("invalid port number: %s", portStr)
			} else {
				port = uint(parsedPort)
			}
		}

		sshURL = parts[0]
	}

	// Parse username@hostname
	if strings.Contains(sshURL, "@") {
		parts := strings.Split(sshURL, "@")
		if len(parts) != 2 {
			return "", "", 0, fmt.Errorf("invalid SSH URL format: %s", sshURL)
		}
		username = parts[0]
		hostname = parts[1]
	} else {
		return "", "", 0, fmt.Errorf("username is required in SSH URL format: username@hostname[:port]")
	}

	if username == "" {
		return "", "", 0, fmt.Errorf("username cannot be empty")
	}
	if hostname == "" {
		return "", "", 0, fmt.Errorf("hostname cannot be empty")
	}

	return username, hostname, port, nil
}

// buildSSHCredentials builds SSH credentials from positional arguments or database
func buildSSHCredentials(cmd *cobra.Command, args []string) (*ssh.Credentials, error) {
	creds := &ssh.Credentials{}

	// Try to parse SSH URL from positional argument first
	if len(args) > 0 {
		username, hostname, port, err := parseSSHURL(args[0])
		if err != nil {
			return nil, fmt.Errorf("failed to parse SSH URL '%s': %v", args[0], err)
		}
		creds.Username = username
		creds.Host = hostname
		creds.Port = port
	} else {
		// If no positional argument, try to get from database
		hostNode, err := nodes_repository.GetHostNode()
		if err != nil {
			return nil, fmt.Errorf("failed to get host node from database: %v", err)
		}
		if hostNode == nil {
			return nil, fmt.Errorf("no host node found in database")
		}
		if hostNode.WGPublicIp == nil {
			return nil, fmt.Errorf("host node public IP not found in database")
		}

		// Use the public IP as the host
		creds.Host = *hostNode.WGPublicIp
		creds.Port = 22 // Default SSH port

		fmt.Printf("🔍 Using the bootstrapped host node: %s\n", creds.Host)
		fmt.Printf("👤 Enter SSH username: ")
		fmt.Scanln(&creds.Username)

		if creds.Username == "" {
			return nil, fmt.Errorf("SSH username is required")
		}
	}

	if creds.Host == "" {
		return nil, fmt.Errorf("SSH host is required. Use positional argument (username@hostname[:port])")
	}
	if creds.Username == "" {
		return nil, fmt.Errorf("SSH username is required. Use positional argument (username@hostname[:port])")
	}

	// Handle authentication method
	if keyPath := cmd.Flag("ssh-key-path").Value.String(); keyPath != "" {
		creds.PrivateKeyPath = keyPath
		if passphrase, err := readPasswordSecurely("🔒 Enter SSH key passphrase (leave empty if none): "); err == nil && passphrase != "" {
			creds.Passphrase = passphrase
		}
	} else {
		if password, err := readPasswordSecurely("🔒 Enter SSH password: "); err == nil {
			creds.Password = password
		} else {
			return nil, fmt.Errorf("failed to read password: %v", err)
		}
	}

	if creds.PrivateKeyPath == "" && creds.Password == "" {
		return nil, fmt.Errorf("SSH authentication is required. Use --ssh-key-path flag or provide password interactively")
	}

	return creds, nil
}

var HostCmd = &cobra.Command{
	Use:   "host",
	Short: "waygate host commands",
	Long:  `Manage waygate host node: configure the host node and start the waygate host node`,
}

var StartHostCmd = &cobra.Command{
	Use:   "start",
	Short: "Start waygate in host mode",
	Long:  `Start waygate in host mode. It will handle network connections and state management.`,
	Run: func(cmd *cobra.Command, args []string) {
		router := routes.Router(dbInstance)

		publicIP, err := join_requests_service.GetPublicIP()

		if err != nil {
			cmd.PrintErrf("Failed to get public IP: %v\n", err)
			return
		}

		serverError := make(chan error, 1)

		if !HostStartConfigureOnly {
			go func() {
				if err := http.ListenAndServe(fmt.Sprintf(":%d", config.Config.ControlServerPort), router); err != nil {
					serverError <- err
				}
			}()
		}

		hostNode, err := nodes_repository.EnsureHostNode(types.IPMarshable{
			IP: net.ParseIP(*publicIP),
		}, config.Config.WGPublicPort)

		if err != nil {
			cmd.PrintErrf("waygate host node start failed: %v\n", err)
			cmd.PrintErrf("Failed to ensure host node: %v\n", err)
			return
		}

		publicServices := public_services_repository.GetAll()

		err = hostNode.SaveConfigs(publicServices, true)

		if err != nil {
			cmd.PrintErrf("Failed to save configs: %v\n", err)
			return
		}

		if !HostStartConfigureOnly {
			cmd.Printf("waygate server has started on host: %s\n", *hostNode.WGPublicIp)
		} else {
			cmd.Printf("waygate has been configured on the host: %s\n", *hostNode.WGPublicIp)
		}

		if !HostStartConfigureOnly {
			// Block on the server error channel
			if err := <-serverError; err != nil {
				cmd.PrintErrf("Server error: %v\n", err)
			}
		}
	},
}

var StatusHostCmd = &cobra.Command{
	Use:   "status [username@hostname[:port]]",
	Short: "Check waygate host node status",
	Long: `Check the status of waygate host node: SSH connection, Docker installation, and waygate status.

If no username@hostname[:port] is provided, the command will use the bootstrapped host node.`,
	Args: cobra.MaximumNArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		sshService := ssh.NewService()

		// Build credentials from positional argument or flags
		creds, err := buildSSHCredentials(cmd, args)
		if err != nil {
			cmd.PrintErrf("Error: %v\n", err)
			return
		}

		cmd.Printf("🔍 Checking waygate Host Status\n")
		cmd.Printf("================================\n\n")

		// SSH Connection Check
		cmd.Printf("📡 SSH Connection\n")
		cmd.Printf("   Host: %s@%s:%d\n", creds.Username, creds.Host, creds.Port)

		err = sshService.Connect(creds)
		if err != nil {
			cmd.Printf("   Status: ❌ Failed\n")
			cmd.Printf("   Error:  %v\n\n", err)
			return
		}

		defer sshService.Close()
		cmd.Printf("   Status: ✅ Connected\n\n")

		// Docker Installation Check
		cmd.Printf("🐳 Docker Installation\n")
		dockerInstalled, err := sshService.IsDockerInstalled()
		if err != nil {
			cmd.Printf("   Status: ❌ Check Failed\n")
			cmd.Printf("   Error:  %v\n\n", err)
			return
		}

		if dockerInstalled {
			cmd.Printf("   Status: ✅ Installed\n")

			// Get Docker version
			dockerVersion, err := sshService.GetDockerVersion()
			if err == nil {
				cmd.Printf("   Version: %s\n", dockerVersion)
			}
		} else {
			cmd.Printf("   Status: ❌ Not Installed\n\n")
			cmd.Printf("💡 Install Docker to continue with waygate setup.\n\n")
			return
		}

		// Docker Permissions Check
		cmd.Printf("   Permissions: ")
		dockerAccessible, err := sshService.IsDockerAccessible()
		if err != nil {
			cmd.Printf("❌ Check Failed\n")
			cmd.Printf("   Error:  %v\n\n", err)
			return
		}

		if dockerAccessible {
			cmd.Printf("✅ User has access\n")
		} else {
			cmd.Printf("❌ User lacks permissions\n")
			cmd.Printf("💡 Add user to docker group or use sudo.\n\n")
			return
		}
		cmd.Printf("\n")

		// waygate Status Check
		cmd.Printf("🚀 waygate Status\n")
		isRunning, err := sshService.IsWireportHostContainerRunning()
		if err != nil {
			cmd.Printf("   Status: ❌ Check Failed\n")
			cmd.Printf("   Error:  %v\n\n", err)
			return
		}

		if isRunning {
			cmd.Printf("   Status: ✅ Running\n")

			// Get detailed container status
			containerStatus, err := sshService.GetWireportContainerStatus()
			if err == nil && containerStatus != "" {
				cmd.Printf("   Details: %s\n", containerStatus)
			}
		} else {
			cmd.Printf("   Status: ❌ Not Running\n")

			// Check if container exists but is stopped
			containerStatus, err := sshService.GetWireportContainerStatus()
			if err == nil && containerStatus != "" {
				cmd.Printf("   Details: %s\n", containerStatus)
			}

			cmd.Printf("   💡 Run 'waygate host bootstrap %s@%s:%d' to install and start waygate.\n", creds.Username, creds.Host, creds.Port)
		}
		cmd.Printf("\n")

		// Docker Network Status Check
		cmd.Printf("🌐 waygate Docker Network\n")
		networkStatus, err := sshService.GetWireportNetworkStatus()
		if err != nil {
			cmd.Printf("   Status: ❌ Check Failed\n")
			cmd.Printf("   Error:  %v\n\n", err)
			return
		}

		if networkStatus != "" {
			cmd.Printf("   Network: ✅ '%s' exists\n", strings.TrimSpace(networkStatus))
		} else {
			cmd.Printf("   Network: ❌ %s not found\n", config.Config.DockerNetworkName)
			cmd.Printf("💡 Network will be created when waygate starts.\n")
		}
		cmd.Printf("\n")

		cmd.Printf("✨ Status check completed successfully!\n")
	},
}

var BootstrapHostCmd = &cobra.Command{
	Use:   "bootstrap username@hostname[:port]",
	Short: "Bootstrap waygate host node",
	Long:  `Bootstrap waygate host node. It will install waygate on the host node.`,
	Args:  cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		sshService := ssh.NewService()

		creds, err := buildSSHCredentials(cmd, args)
		if err != nil {
			cmd.PrintErrf("❌ Error: %v\n", err)
			return
		}

		cmd.Printf("🚀 waygate Host Bootstrap\n")
		cmd.Printf("==========================\n\n")

		// SSH Connection
		cmd.Printf("📡 Connecting to host...\n")
		cmd.Printf("   Host: %s@%s:%d\n", creds.Username, creds.Host, creds.Port)

		err = sshService.Connect(creds)
		if err != nil {
			cmd.Printf("   Status: ❌ Failed\n")
			cmd.Printf("   Error:  %v\n\n", err)
			return
		}

		defer sshService.Close()
		cmd.Printf("   Status: ✅ Connected\n\n")

		// Check if already running
		cmd.Printf("🔍 Checking current status...\n")
		isRunning, err := sshService.IsWireportHostContainerRunning()
		if err != nil {
			cmd.Printf("   Status: ❌ Check Failed\n")
			cmd.Printf("   Error:  %v\n\n", err)
			return
		}

		if isRunning {
			cmd.Printf("   Status: ✅ Already Running\n")
			cmd.Printf("   💡 waygate host container is already running on this host and bootstrapping is not required.\n\n")
			return
		}

		cmd.Printf("   Status: ❌ Not Running\n")
		cmd.Printf("   💡 Proceeding with installation...\n\n")

		// Installation
		cmd.Printf("📦 Installing waygate...\n")
		cmd.Printf("   Host: %s@%s:%d\n", creds.Username, creds.Host, creds.Port)

		_, err = sshService.InstallWireport()
		if err != nil {
			cmd.Printf("   Status: ❌ Installation Failed\n")
			cmd.Printf("   Error:  %v\n\n", err)
			return
		}

		cmd.Printf("   Status: ✅ Installation Completed\n\n")

		// Verification
		cmd.Printf("✅ Verifying installation...\n")
		installationConfirmed, err := sshService.IsWireportHostContainerRunning()
		if err != nil {
			cmd.Printf("   Status: ❌ Verification Failed\n")
			cmd.Printf("   Error:  %v\n\n", err)
			return
		}

		if installationConfirmed {
			cmd.Printf("   Status: ✅ Verified Successfully, Running\n")
			cmd.Printf("   🎉 waygate has been successfully installed and started on the host!\n\n")
		} else {
			cmd.Printf("   Status: ❌ Verified Failed\n")
			cmd.Printf("   💡 waygate container was not found running after installation.\n\n")
		}

		cmd.Printf("✨ Bootstrap process completed!\n")
	},
}

func init() {
	HostCmd.AddCommand(StartHostCmd)
	HostCmd.AddCommand(StatusHostCmd)
	HostCmd.AddCommand(BootstrapHostCmd)

	StartHostCmd.Flags().BoolVar(&HostStartConfigureOnly, "configure", false, "Configure waygate in host mode without making it available for external connections")

	StatusHostCmd.Flags().String("ssh-key-path", "", "Path to SSH private key file (for passwordless authentication)")

	BootstrapHostCmd.Flags().String("ssh-key-path", "", "Path to SSH private key file (for passwordless authentication)")
}
