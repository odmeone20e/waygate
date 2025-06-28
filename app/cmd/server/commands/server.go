package commands

import (
	"github.com/spf13/cobra"
)

var forceServerCreation = false
var quietServerCreation = false
var dockerSubnet = ""

var ServerCmd = &cobra.Command{
	Use:   "server",
	Short: "waygate server commands",
	Long:  `Manage connected waygate server nodes and create join-requests for connecting new servers to the waygate network`,
}

var NewServerCmd = &cobra.Command{
	Use:   "new",
	Short: "Create a new join-request for connecting a server to waygate network",
	Long:  `Create a new join-request for connecting a server to waygate network. The join-request will generate a token that can be used to join the network (see 'waygate join' command)`,
	Run: func(cmd *cobra.Command, _ []string) {
		commandsService.ServerNew(nodesRepository, joinRequestsRepository, cmd.OutOrStdout(), cmd.ErrOrStderr(), forceServerCreation, quietServerCreation, dockerSubnet)
	},
}

var StartServerCmd = &cobra.Command{
	Use:   "start",
	Short: "Start the waygate server",
	Long:  `Start the waygate server. This command is only relevant for server nodes after they joined the network.`,
	Run: func(cmd *cobra.Command, _ []string) {
		commandsService.ServerStart(nodesRepository, cmd.OutOrStdout(), cmd.ErrOrStderr())
	},
}

func init() {
	NewServerCmd.Flags().BoolVarP(&forceServerCreation, "force", "f", false, "Force the creation of a new server, bypassing the join request generation")
	NewServerCmd.Flags().StringVar(&dockerSubnet, "docker-subnet", "", "Specify a custom Docker subnet for the server (e.g. 172.20.0.0/16)")
	NewServerCmd.Flags().BoolVarP(&quietServerCreation, "quiet", "q", false, "Quiet mode, don't print any output except for the join request token")

	ServerCmd.AddCommand(NewServerCmd)
	ServerCmd.AddCommand(StartServerCmd)
}
