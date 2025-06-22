package commands

import (
	"net"

	"waygate/cmd/server/config"
	"waygate/internal/nodes/types"
	public_services "waygate/internal/public-services"

	"github.com/spf13/cobra"
)

var force bool = false
var dockerSubnet string = ""

var ServerCmd = &cobra.Command{
	Use:   "server",
	Short: "waygate server commands",
	Long:  `Manage connected waygate server nodes and create join-requests for connecting new servers to the waygate network`,
}

var NewServerCmd = &cobra.Command{
	Use:   "new",
	Short: "Create a new join-request for connecting a server to waygate network",
	Long:  `Create a new join-request for connecting a server to waygate network. The join-request will generate a token that can be used to join the network (see 'waygate join' command)`,
	Run: func(cmd *cobra.Command, args []string) {
		totalDockerSubnets, availableDockerSubnets, err := nodes_repository.TotalAndAvailableDockerSubnets()

		if err != nil {
			cmd.PrintErrf("Failed to count available Docker subnets: %v\n", err)
			return
		}

		totalJoinRequests := join_requests_repository.Count()

		if availableDockerSubnets <= 0 || totalJoinRequests >= availableDockerSubnets {
			cmd.PrintErrf("No Docker subnets available. Please delete some server nodes (total: %d) or join-requests (total: %d) to free up some subnets.\n", totalDockerSubnets, totalJoinRequests)
			return
		}

		var dockerSubnetPtr *string

		if dockerSubnet != "" {
			// validate the subnet format
			parsedDockerSubnet, err := types.ParseIPNetMarshable(dockerSubnet, true)

			if err != nil {
				cmd.PrintErrf("Failed to parse Docker subnet: %v\n", err)
				return
			}

			if !nodes_repository.IsDockerSubnetAvailable(parsedDockerSubnet) {
				cmd.PrintErrf("Docker subnet %s is already in use\n", dockerSubnet)
				return
			}

			dockerSubnetPtr = &dockerSubnet

			cmd.Printf("Using custom Docker subnet: %s\n", dockerSubnet)
		}

		if force {
			cmd.Printf("Force flag detected, creating server node without generating a join request\n")

			_, err := nodes_repository.CreateServer(dockerSubnetPtr)

			if err != nil {
				cmd.PrintErrf("Failed to create server node: %v\n", err)
				return
			}

			cmd.Printf("Server node created without join request\n")
			return
		}

		hostNode, err := nodes_repository.GetHostNode()

		if err != nil {
			cmd.PrintErrf("Failed to get host node: %v\n", err)
			return
		}

		joinRequest, err := join_requests_repository.Create(types.UDPAddrMarshable{
			UDPAddr: net.UDPAddr{
				IP:   net.ParseIP(*hostNode.WGPublicIp),
				Port: int(config.Config.ControlServerPort),
			},
		}, dockerSubnetPtr, types.NodeRoleServer)

		if err != nil {
			cmd.PrintErrf("Failed to create join request: %v\n", err)
			return
		}

		joinRequestBase64, err := joinRequest.ToBase64()

		if err != nil {
			cmd.PrintErrf("Failed to encode join request: %v\n", err)
			return
		}

		cmd.Printf("waygate:\n\nServer created, execute the command below on the server to join the network:\n\nwaygate join %s\n", *joinRequestBase64)
	},
}

var StartServerCmd = &cobra.Command{
	Use:   "start",
	Short: "Start the waygate server",
	Long:  `Start the waygate server. This command is only relevant for server nodes after they joined the network.`,
	Run: func(cmd *cobra.Command, args []string) {
		cmd.Printf("Starting waygate server\n")

		currentNode, err := nodes_repository.GetCurrentNode()

		if err != nil {
			cmd.PrintErrf("Failed to get current node: %v\n", err)
			return
		}

		if currentNode == nil {
			cmd.PrintErrf("No current node found\n")
			return
		}

		if currentNode.Role != types.NodeRoleServer {
			cmd.PrintErrf("Current node is not a server node\n")
			return
		}

		publicServices := []*public_services.PublicService{}

		currentNode.SaveConfigs(publicServices, true)

		cmd.Printf("Server node configs saved to the disk successfully\n")
	},
}

func init() {
	NewServerCmd.Flags().BoolVarP(&force, "force", "f", false, "Force the creation of a new server, bypassing the join request generation")
	NewServerCmd.Flags().StringVar(&dockerSubnet, "docker-subnet", "", "Specify a custom Docker subnet for the server (e.g. 172.20.0.0/16)")

	ServerCmd.AddCommand(NewServerCmd)
	ServerCmd.AddCommand(StartServerCmd)
}
