package commands

import (
	"fmt"
	"waygate/cmd/server/config"
	"waygate/internal/nodes/types"
	"waygate/internal/ssh"
	"waygate/version"

	"github.com/spf13/cobra"
)

var forceServerCreation = false
var quietServerCreation = false
var dockerSubnet = ""
var ServerSSHKeyPassEmpty = false
var ServerDockerImage = config.Config.WireportServerContainerImage
var ServerDockerImageTag = version.Version
var forceServerTeardown = false

var ServerCmd = &cobra.Command{
	Use:   "server",
	Short: "waygate server commands",
	Long:  `Manage connected waygate server nodes and create join-requests for connecting new servers to the waygate network.`,
}

var NewServerCmd = &cobra.Command{
	Use:   "new",
	Short: "Create a new join-request for connecting a server to waygate network",
	Long:  `Create a new join-request for connecting a server to waygate network. The join-request will generate a token that can be used to join the network (see 'waygate join' command help)`,
	Run: func(cmd *cobra.Command, _ []string) {
		commandsService.ServerNew(cmd.OutOrStdout(), cmd.ErrOrStderr(), forceServerCreation, quietServerCreation, dockerSubnet)
	},
}

var StartServerCmd = &cobra.Command{
	Use:   "start",
	Short: "Start waygate in server mode",
	Long:  `Start waygate in server mode. This command is only relevant for server nodes after they joined the network.`,
	Run: func(cmd *cobra.Command, _ []string) {
		commandsService.ServerStart(cmd.OutOrStdout(), cmd.ErrOrStderr())
	},
}

var StatusServerCmd = &cobra.Command{
	Use:   "status username@hostname[:port]",
	Short: "Check waygate server node status",
	Long:  `Check the status of a waygate server node: SSH connection, Docker installation, and waygate server status.`,
	Args:  cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		// Build credentials from positional argument or flags
		creds, err := buildSSHCredentials(cmd, args, false, false, ServerSSHKeyPassEmpty)

		if err != nil {
			cmd.PrintErrf("❌ Error: %v\n", err)
			return
		}

		commandsService.ServerStatus(creds, cmd.OutOrStdout())
	},
}

var UpServerCmd = &cobra.Command{
	Use:   "up username@hostname[:port]",
	Short: "Bootstrap a waygate server node",
	Long:  `Bootstrap a waygate server node: install and configure waygate software in server mode on it.`,
	Args:  cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		creds, err := buildSSHCredentials(cmd, args, false, false, ServerSSHKeyPassEmpty)

		if err != nil {
			cmd.PrintErrf("❌ Error: %v\n", err)
			return
		}

		if dockerSubnet != "" {
			// validate the subnet format
			_, err := types.ParseIPNetMarshable(dockerSubnet, true)

			if err != nil {
				cmd.PrintErrf("❌ Failed to parse Docker subnet: %v\n", err)
				return
			}
		}

		commandsService.ServerUp(creds, ServerDockerImage, ServerDockerImageTag, cmd.OutOrStdout(), cmd.ErrOrStderr(), dockerSubnet)
	},
}

var DownServerCmd = &cobra.Command{
	Use:   "down username@hostname[:port]",
	Short: "Teardown waygate server node",
	Long:  `Teardown waygate server node: stop the waygate server software and remove all the data and configuration from the server node, deregister the server node from the waygate network.`,
	Args:  cobra.MaximumNArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		var creds *ssh.Credentials
		var err error

		if !forceServerTeardown {
			cmd.Printf("🔴 WARNING: This command will destroy all waygate data and configuration on the server node.\nAre you sure you want to continue? (y/n): ")

			var confirm string
			_, err = fmt.Scanln(&confirm)

			if err != nil {
				cmd.PrintErrf("❌ Error: %v\n", err)
				return
			}

			if confirm != "y" {
				cmd.PrintErrf("❌ Aborted\n")
				return
			}
		}

		if len(args) > 0 {
			var err error
			creds, err = buildSSHCredentials(cmd, args, false, false, ServerSSHKeyPassEmpty)

			if err != nil {
				cmd.PrintErrf("❌ Error: %v\n", err)
				return
			}
		}

		commandsService.ServerDown(creds, cmd.OutOrStdout(), cmd.ErrOrStderr())
	},
}

var ListServerCmd = &cobra.Command{
	Use:   "list",
	Short: "List all servers",
	Long:  `List all servers that are connected to the waygate network`,
	Run: func(cmd *cobra.Command, _ []string) {
		commandsService.ServerList(nil, cmd.OutOrStdout(), cmd.ErrOrStderr())
	},
}

var UpgradeServerCmd = &cobra.Command{
	Use:   "upgrade username@hostname[:port]",
	Short: "Upgrade a server",
	Long:  `Upgrade a server. This command will upgrade the waygate server software to the latest version. This command is only relevant for server nodes after they joined the network.`,
	Args:  cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		creds, err := buildSSHCredentials(cmd, args, false, false, ServerSSHKeyPassEmpty)

		if err != nil {
			cmd.PrintErrf("❌ Error: %v\n", err)
			return
		}

		commandsService.ServerUpgrade(creds, ServerDockerImage, ServerDockerImageTag, cmd.OutOrStdout(), cmd.ErrOrStderr())
	},
}

func init() {
	NewServerCmd.Flags().BoolVarP(&forceServerCreation, "force", "f", false, "Force the creation of a new server, bypassing the join request generation")
	NewServerCmd.Flags().StringVar(&dockerSubnet, "docker-subnet", "", "Specify a custom Docker subnet for the server (e.g. 172.20.0.0/16)")
	NewServerCmd.Flags().BoolVarP(&quietServerCreation, "quiet", "q", false, "Quiet mode, don't print any output except for the join request token")

	ServerCmd.AddCommand(NewServerCmd)
	ServerCmd.AddCommand(StartServerCmd)
	ServerCmd.AddCommand(StatusServerCmd)
	ServerCmd.AddCommand(UpServerCmd)
	ServerCmd.AddCommand(DownServerCmd)
	ServerCmd.AddCommand(ListServerCmd)
	ServerCmd.AddCommand(UpgradeServerCmd)

	StatusServerCmd.Flags().String("ssh-key-path", "", "Path to SSH private key file (for passwordless authentication)")
	StatusServerCmd.Flags().BoolVar(&ServerSSHKeyPassEmpty, "ssh-key-pass-empty", false, "Skip SSH key passphrase prompt (for passwordless SSH keys)")

	UpServerCmd.Flags().String("ssh-key-path", "", "Path to SSH private key file (for passwordless authentication)")
	UpServerCmd.Flags().BoolVar(&ServerSSHKeyPassEmpty, "ssh-key-pass-empty", false, "Skip SSH key passphrase prompt (for passwordless SSH keys)")
	UpServerCmd.Flags().StringVar(&dockerSubnet, "docker-subnet", "", "Specify a custom Docker subnet for the server (e.g. 172.20.0.0/16)")
	UpServerCmd.Flags().StringVar(&ServerDockerImage, "image", config.Config.WireportServerContainerImage, "Docker image to use for the waygate server container")
	UpServerCmd.Flags().StringVar(&ServerDockerImageTag, "image-tag", version.Version, "Image tag to use for the waygate server container")

	DownServerCmd.Flags().String("ssh-key-path", "", "Path to SSH private key file (for passwordless authentication)")
	DownServerCmd.Flags().BoolVar(&ServerSSHKeyPassEmpty, "ssh-key-pass-empty", false, "Skip SSH key passphrase prompt (for passwordless SSH keys)")
	DownServerCmd.Flags().BoolVarP(&forceServerTeardown, "force", "f", false, "Force the teardown of the server node, bypassing the confirmation prompt")

	UpgradeServerCmd.Flags().String("ssh-key-path", "", "Path to SSH private key file (for passwordless authentication)")
	UpgradeServerCmd.Flags().BoolVar(&ServerSSHKeyPassEmpty, "ssh-key-pass-empty", false, "Skip SSH key passphrase prompt (for passwordless SSH keys)")
	UpgradeServerCmd.Flags().StringVar(&ServerDockerImage, "image", config.Config.WireportServerContainerImage, "Docker image to use for the waygate server container")
	UpgradeServerCmd.Flags().StringVar(&ServerDockerImageTag, "image-tag", version.Version, "Image tag to use for the waygate server container")
}
