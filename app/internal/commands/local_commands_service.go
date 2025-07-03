package commands

import (
	"bytes"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"
	"waygate/cmd/server/config"
	"waygate/internal/dockerutils"
	"waygate/internal/encryption/mtls"
	"waygate/internal/joinrequests"
	joinrequeststypes "waygate/internal/joinrequests/types"
	"waygate/internal/networkapps"
	nodes "waygate/internal/nodes"
	"waygate/internal/nodes/types"
	node_types "waygate/internal/nodes/types"
	"waygate/internal/publicservices"
	"waygate/internal/ssh"

	"github.com/google/uuid"
)

type LocalCommandsService struct {
	NodesRepository          *nodes.Repository
	PublicServicesRepository *publicservices.Repository
	JoinRequestsRepository   *joinrequests.Repository
}

// gateway

func (s *LocalCommandsService) GatewayStart(gatewayPublicIP string, stdOut io.Writer, errOut io.Writer, gatewayStartConfigureOnly bool, router http.Handler) {
	gatewayNode, err := s.NodesRepository.EnsureGatewayNode(types.IPMarshable{
		IP: net.ParseIP(gatewayPublicIP),
	}, config.Config.WGPublicPort, gatewayPublicIP, config.Config.ControlServerPort)

	if err != nil {
		fmt.Fprintf(errOut, "waygate gateway node start failed: %v\n", err)
		return
	}

	if gatewayNode.GatewayCertBundle == nil {
		fmt.Fprintf(errOut, "waygate gateway node start failed: no gateway cert bundle found\n")
		return
	}

	serverError := make(chan error, 1)

	if !gatewayStartConfigureOnly {
		go func() {
			var tlsConfig *tls.Config

			tlsConfig, err = gatewayNode.GatewayCertBundle.GetServerTLSConfig()

			if err != nil {
				serverError <- fmt.Errorf("Failed to get TLS config for the gateway control server: %v", err)
				return
			}

			server := &http.Server{
				Addr:      fmt.Sprintf(":%d", config.Config.ControlServerPort),
				Handler:   router,
				TLSConfig: tlsConfig,
			}

			if err = server.ListenAndServeTLS("", ""); err != nil {
				serverError <- err
			}
		}()
	}

	publicServices := s.PublicServicesRepository.GetAll()

	err = gatewayNode.SaveConfigs(publicServices, true)

	if err != nil {
		fmt.Fprintf(errOut, "Failed to save configs: %v\n", err)
		return
	}

	if !gatewayStartConfigureOnly {
		fmt.Fprintf(stdOut, "waygate server has started with mTLS on gateway: %s\n", gatewayPublicIP)
	} else {
		fmt.Fprintf(stdOut, "waygate has been configured on the gateway: %s\n", gatewayPublicIP)
	}

	if !gatewayStartConfigureOnly {
		// Block on the server error channel
		if err := <-serverError; err != nil {
			fmt.Fprintf(errOut, "Server error: %v\n", err)
		}
	}
}

func (s *LocalCommandsService) GatewayStatus(creds *ssh.Credentials, stdOut io.Writer) {
	sshService := ssh.NewService()

	fmt.Fprintf(stdOut, "🔍 Checking waygate Gateway Status\n")
	fmt.Fprintf(stdOut, "================================\n\n")

	// SSH Connection Check
	fmt.Fprintf(stdOut, "📡 SSH Connection\n")
	fmt.Fprintf(stdOut, "   Gateway: %s@%s:%d\n", creds.Username, creds.Host, creds.Port)

	err := sshService.Connect(creds)
	if err != nil {
		fmt.Fprintf(stdOut, "   Status: ❌ Failed\n")
		fmt.Fprintf(stdOut, "   Error:  %v\n\n", err)
		return
	}

	defer sshService.Close()
	fmt.Fprintf(stdOut, "   Status: ✅ Connected\n\n")

	// Docker Installation Check
	fmt.Fprintf(stdOut, "🐳 Docker Installation\n")
	dockerInstalled, err := sshService.IsDockerInstalled()

	if err != nil {
		fmt.Fprintf(stdOut, "   Status: ❌ Check Failed\n")
		fmt.Fprintf(stdOut, "   Error:  %v\n\n", err)
		return
	}

	var dockerVersion string

	if dockerInstalled {
		fmt.Fprintf(stdOut, "   Status: ✅ Installed\n")

		// Get Docker version
		dockerVersion, err = sshService.GetDockerVersion()
		if err == nil {
			fmt.Fprintf(stdOut, "   Version: %s\n", dockerVersion)
		}
	} else {
		fmt.Fprintf(stdOut, "   Status: ❌ Not Installed\n\n")
		fmt.Fprintf(stdOut, "💡 Install Docker to continue with waygate setup.\n\n")
		return
	}

	// Docker Permissions Check
	fmt.Fprintf(stdOut, "   Permissions: ")
	dockerAccessible, err := sshService.IsDockerAccessible()

	if err != nil {
		fmt.Fprintf(stdOut, "❌ Check Failed\n")
		fmt.Fprintf(stdOut, "   Error:  %v\n\n", err)
		return
	}

	if dockerAccessible {
		fmt.Fprintf(stdOut, "✅ User has access\n")
	} else {
		fmt.Fprintf(stdOut, "❌ User lacks permissions\n")
		fmt.Fprintf(stdOut, "💡 Add user to docker group.\n\n")
		return
	}
	fmt.Fprintf(stdOut, "\n")

	// waygate Status Check
	fmt.Fprintf(stdOut, "🚀 waygate Gateway Status\n")
	isRunning, err := sshService.IsWireportGatewayContainerRunning()
	if err != nil {
		fmt.Fprintf(stdOut, "   Status: ❌ Check Failed\n")
		fmt.Fprintf(stdOut, "   Error:  %v\n\n", err)
		return
	}

	var containerStatus string

	if isRunning {
		fmt.Fprintf(stdOut, "   Status: ✅ Running\n")

		// Get detailed container status
		containerStatus, err = sshService.GetWireportContainerStatus()
		if err == nil && containerStatus != "" {
			fmt.Fprintf(stdOut, "   Details: %s\n", containerStatus)
		}
	} else {
		fmt.Fprintf(stdOut, "   Status: ❌ Not Running\n")

		// Check if container exists but is stopped
		containerStatus, err = sshService.GetWireportContainerStatus()
		if err == nil && containerStatus != "" {
			fmt.Fprintf(stdOut, "   Details: %s\n", containerStatus)
		}

		fmt.Fprintf(stdOut, "   💡 Run 'waygate gateway up %s@%s:%d' to bootstrap waygate gateway and start it.\n", creds.Username, creds.Host, creds.Port)
	}
	fmt.Fprintf(stdOut, "\n")

	// Docker Network Status Check
	fmt.Fprintf(stdOut, "🌐 waygate Docker Network\n")
	networkStatus, err := sshService.GetWireportNetworkStatus()
	if err != nil {
		fmt.Fprintf(stdOut, "   Status: ❌ Check Failed\n")
		fmt.Fprintf(stdOut, "   Error:  %v\n\n", err)
		return
	}

	if networkStatus != "" {
		fmt.Fprintf(stdOut, "   Network: ✅ '%s' exists\n", strings.TrimSpace(networkStatus))
	} else {
		fmt.Fprintf(stdOut, "   Network: ❌ '%s' not found\n", config.Config.DockerNetworkName)
		fmt.Fprintf(stdOut, "💡 Network will be created when waygate starts.\n")
	}
	fmt.Fprintf(stdOut, "\n")

	fmt.Fprintf(stdOut, "✨ Gateway Status check completed successfully!\n")
}

func (s *LocalCommandsService) GatewayUp(creds *ssh.Credentials, stdOut io.Writer, errOut io.Writer) {
	sshService := ssh.NewService()

	fmt.Fprintf(errOut, "🚀 waygate Gateway Bootstrapping\n")
	fmt.Fprintf(errOut, "=================================\n\n")

	// SSH Connection
	fmt.Fprintf(errOut, "📡 Connecting to gateway...\n")
	fmt.Fprintf(errOut, "   Gateway: %s@%s:%d\n", creds.Username, creds.Host, creds.Port)

	err := sshService.Connect(creds)

	if err != nil {
		fmt.Fprintf(errOut, "   Status: ❌ Failed\n")
		fmt.Fprintf(errOut, "   Error:  %v\n\n", err)
		return
	}

	defer sshService.Close()

	fmt.Fprintf(errOut, "   Status: ✅ Connected\n\n")
	fmt.Fprintf(errOut, "🔍 Checking current status...\n")

	isRunning, err := sshService.IsWireportGatewayContainerRunning()
	if err != nil {
		fmt.Fprintf(errOut, "   Status: ❌ Check Failed\n")
		fmt.Fprintf(errOut, "   Error:  %v\n\n", err)
		return
	}

	if isRunning {
		fmt.Fprintf(errOut, "   Status: ✅ Already Running\n")
		fmt.Fprintf(errOut, "   💡 waygate gateway container is already running on this gateway and bootstrapping is not required.\n\n")
		return
	}

	fmt.Fprintf(errOut, "   Status: ❌ Not Running\n")
	fmt.Fprintf(errOut, "   💡 Proceeding with installation...\n\n")
	fmt.Fprintf(errOut, "📦 Installing waygate gateway...\n")
	fmt.Fprintf(errOut, "   Gateway: %s@%s:%d\n", creds.Username, creds.Host, creds.Port)

	_, clientJoinToken, err := sshService.InstallWireportGateway()

	if err != nil {
		fmt.Fprintf(errOut, "   Status: ❌ Installation Failed\n")
		fmt.Fprintf(errOut, "   Error:  %v\n\n", err)
		return
	}

	fmt.Fprintf(errOut, "   Status: ✅ Installation Completed\n\n")
	fmt.Fprintf(errOut, "✅ Verifying installation...\n")

	installationConfirmed, err := sshService.IsWireportGatewayContainerRunning()
	if err != nil {
		fmt.Fprintf(errOut, "   Status: ❌ Verification Failed\n")
		fmt.Fprintf(errOut, "   Error:  %v\n\n", err)
		return
	}

	if installationConfirmed {
		fmt.Fprintf(errOut, "   Status: ✅ Verified Successfully, Running\n")
		fmt.Fprintf(errOut, "   🎉 waygate has been successfully installed and started on the gateway!\n\n")
	} else {
		fmt.Fprintf(errOut, "   Status: ❌ Verified Failed\n")
		fmt.Fprintf(errOut, "   💡 waygate container was not found running after installation.\n\n")
	}

	if clientJoinToken != nil {
		fmt.Fprintf(errOut, "   🔑 Applying Client Join Token: %s...\n", (*clientJoinToken)[:20])

		s.Join(stdOut, errOut, *clientJoinToken)

		fmt.Fprintf(errOut, "   ✅ Client Join Token Applied\n\n")
	}

	fmt.Fprintf(errOut, "✨ Gateway Bootstrapping completed successfully!\n")
}

func (s *LocalCommandsService) GatewayDown(creds *ssh.Credentials, stdOut io.Writer, errOut io.Writer) {
	// First, try to determine if we are executing on a gateway node or locally
	currentNode, err := s.NodesRepository.GetCurrentNode()

	if err == nil && currentNode != nil && currentNode.Role == node_types.NodeRoleGateway {
		// Local execution – just detach and remove docker network like in ServerDown
		err = dockerutils.DetachDockerNetworkFromAllContainers()
		if err != nil {
			fmt.Fprintf(errOut, "Error detaching waygate docker network: %v\n", err)
			return
		}

		err = dockerutils.RemoveDockerNetwork()
		if err != nil {
			fmt.Fprintf(errOut, "Error removing waygate docker network: %v\n", err)
			return
		}

		return
	}

	// Remote execution – credentials are required
	if creds == nil {
		fmt.Fprintf(errOut, "Error: SSH credentials are required\n")
		return
	}

	sshService := ssh.NewService()

	fmt.Fprintf(stdOut, "🚀 waygate Gateway Teardown\n")
	fmt.Fprintf(stdOut, "============================\n\n")

	// SSH Connection
	fmt.Fprintf(stdOut, "📡 Connecting to gateway...\n")
	fmt.Fprintf(stdOut, "   Gateway: %s@%s:%d\n", creds.Username, creds.Host, creds.Port)

	err = sshService.Connect(creds)
	if err != nil {
		fmt.Fprintf(stdOut, "   Status: ❌ Failed\n")
		fmt.Fprintf(stdOut, "   Error:  %v\n\n", err)
		return
	}

	defer sshService.Close()
	fmt.Fprintf(stdOut, "   Status: ✅ Connected\n\n")

	// Check if gateway is running
	isRunning, err := sshService.IsWireportGatewayContainerRunning()
	if err != nil {
		fmt.Fprintf(stdOut, "   Status: ❌ Check Failed\n")
		fmt.Fprintf(stdOut, "   Error:  %v\n\n", err)
		return
	}

	if !isRunning {
		fmt.Fprintf(stdOut, "   Status: ❌ Not Running\n")
		fmt.Fprintf(stdOut, "   💡 waygate gateway is not running, teardown is not required\n\n")
		return
	}

	// Teardown waygate gateway
	fmt.Fprintf(stdOut, "🛑 Gateway node Teardown\n")
	fmt.Fprintf(stdOut, "   Gateway: %s@%s:%d\n", creds.Username, creds.Host, creds.Port)

	success, err := sshService.TeardownWireportGateway()

	if err != nil || !success {
		fmt.Fprintf(stdOut, "   Status: ❌ Teardown Failed\n")
		fmt.Fprintf(stdOut, "   Error:  %v\n\n", err)
		return
	}

	fmt.Fprintf(stdOut, "   Status: ✅ Teardown Completed\n\n")

	// reset local state
	fmt.Fprintf(stdOut, "🔄 Resetting local state...\n")
	err = s.NodesRepository.DeleteAll()

	if err != nil {
		fmt.Fprintf(stdOut, "   Status: ❌ Failed\n")
		fmt.Fprintf(stdOut, "   Error:  %v\n\n", err)
		return
	}

	fmt.Fprintf(stdOut, "   Status: ✅ Completed\n\n")

	fmt.Fprintf(stdOut, "✨ Gateway Teardown completed successfully!\n")
}

func (s *LocalCommandsService) GatewayUpgrade(creds *ssh.Credentials, stdOut io.Writer, _ io.Writer) {
	sshService := ssh.NewService()

	fmt.Fprintf(stdOut, "🔄 waygate Gateway Upgrade\n")
	fmt.Fprintf(stdOut, "===========================\n\n")

	// SSH Connection
	fmt.Fprintf(stdOut, "📡 Connecting to gateway...\n")
	fmt.Fprintf(stdOut, "   Gateway: %s@%s:%d\n", creds.Username, creds.Host, creds.Port)

	err := sshService.Connect(creds)

	if err != nil {
		fmt.Fprintf(stdOut, "   Status: ❌ Failed\n")
		fmt.Fprintf(stdOut, "   Error:  %v\n\n", err)
		return
	}

	defer sshService.Close()
	fmt.Fprintf(stdOut, "   Status: ✅ Connected\n\n")

	// Upgrade waygate gateway
	fmt.Fprintf(stdOut, "🔄 Upgrading waygate gateway...\n")
	fmt.Fprintf(stdOut, "   Gateway: %s@%s:%d\n", creds.Username, creds.Host, creds.Port)

	success, err := sshService.UpgradeWireportGateway()

	if err != nil {
		fmt.Fprintf(stdOut, "   Status: ❌ Failed\n")
		fmt.Fprintf(stdOut, "   Error:  %v\n\n", err)
		return
	}

	if success {
		fmt.Fprintf(stdOut, "   Status: ✅ Upgraded Successfully\n")
		fmt.Fprintf(stdOut, "   🎉 waygate gateway node has been successfully upgraded!\n\n")
	} else {
		fmt.Fprintf(stdOut, "   Status: ❌ Failed\n")
		fmt.Fprintf(stdOut, "   💡 Upgrade process completed but may not have been successful.\n\n")
	}

	fmt.Fprintf(stdOut, "✨ Gateway Upgrade completed!\n")
}

// server

func (s *LocalCommandsService) ServerNew(forceServerCreation bool, quietServerCreation bool, dockerSubnet string, stdOut io.Writer, errOut io.Writer) {
	totalDockerSubnets, availableDockerSubnets, err := s.NodesRepository.TotalAndAvailableDockerSubnets()

	if err != nil {
		fmt.Fprintf(errOut, "Failed to count available Docker subnets: %v\n", err)
		return
	}

	totalServerRoleJoinRequests := s.JoinRequestsRepository.CountServerJoinRequests()

	if availableDockerSubnets <= 0 || totalServerRoleJoinRequests >= availableDockerSubnets {
		fmt.Fprintf(errOut, "No Docker subnets available. Please delete some server nodes (total used: %d) or server join-requests (total used: %d) to free up some subnets.\n", totalDockerSubnets, totalServerRoleJoinRequests)
		return
	}

	totalWireguardClients, availableWireguardClients, err := s.NodesRepository.TotalAvailableWireguardClients()

	if err != nil {
		fmt.Fprintf(errOut, "Failed to count available WireGuard clients: %v\n", err)
		return
	}

	totalJoinRequests := s.JoinRequestsRepository.CountAll()

	if availableWireguardClients <= 0 || totalJoinRequests >= availableWireguardClients {
		fmt.Fprintf(errOut, "No WireGuard clients available. Please delete some client/server nodes (total used: %d) or client/server join-requests (total used: %d) to free up some clients.\n", totalWireguardClients, totalJoinRequests)
		return
	}

	var dockerSubnetPtr *string
	var parsedDockerSubnet *types.IPNetMarshable

	if dockerSubnet != "" {
		// validate the subnet format
		parsedDockerSubnet, err = types.ParseIPNetMarshable(dockerSubnet, true)

		if err != nil {
			fmt.Fprintf(errOut, "Failed to parse Docker subnet: %v\n", err)
			return
		}

		if !s.NodesRepository.IsDockerSubnetAvailable(parsedDockerSubnet) {
			fmt.Fprintf(errOut, "Docker subnet %s is already in use\n", dockerSubnet)
			return
		}

		dockerSubnetPtr = &dockerSubnet

		if !quietServerCreation {
			fmt.Fprintf(stdOut, "Using custom Docker subnet: %s\n", dockerSubnet)
		}
	}

	if forceServerCreation {
		_, err = s.NodesRepository.CreateServer(dockerSubnetPtr)

		if err != nil {
			fmt.Fprintf(errOut, "Failed to create server node: %v\n", err)
			return
		}

		if !quietServerCreation {
			fmt.Fprintf(stdOut, "Server node created\n")
		}

		return
	}

	gatewayNode, err := s.NodesRepository.GetGatewayNode()

	if err != nil {
		fmt.Fprintf(errOut, "Failed to get gateway node: %v\n", err)
		return
	}

	joinRequestID := uuid.New().String()

	err = gatewayNode.GatewayCertBundle.AddClient(mtls.Options{
		CommonName: joinRequestID,
		Expiry:     config.Config.CertExpiry,
	})

	if err != nil {
		fmt.Fprintf(errOut, "Failed to add client to gateway cert bundle: %v\n", err)
		return
	}

	err = s.NodesRepository.SaveNode(gatewayNode)

	if err != nil {
		fmt.Fprintf(errOut, "Failed to save gateway node: %v\n", err)
		return
	}

	clientCertBundle, err := gatewayNode.GatewayCertBundle.GetClientBundlePublic(joinRequestID)

	if err != nil {
		fmt.Fprintf(errOut, "Failed to get client cert bundle: %v\n", err)
		return
	}

	joinRequest, err := s.JoinRequestsRepository.Create(joinRequestID, types.UDPAddrMarshable{
		UDPAddr: net.UDPAddr{
			IP:   net.ParseIP(*gatewayNode.WGPublicIP),
			Port: int(config.Config.ControlServerPort),
		},
	}, dockerSubnetPtr, types.NodeRoleServer, clientCertBundle)

	if err != nil {
		fmt.Fprintf(errOut, "Failed to create join request: %v\n", err)
		return
	}

	joinRequestBase64, err := joinRequest.ToBase64()

	if err != nil {
		fmt.Fprintf(errOut, "Failed to encode join request: %v\n", err)
		return
	}

	if !quietServerCreation {
		fmt.Fprintf(stdOut, "✅ Server created, execute the command below on the server to join the network:\n\nwaygate join %s\n", *joinRequestBase64)
	} else {
		fmt.Fprintf(stdOut, "%s\n", *joinRequestBase64)
	}
}

func (s *LocalCommandsService) ServerRemove(stdOut io.Writer, errOut io.Writer, serverNodeID string) {
	err := s.NodesRepository.DeleteServer(serverNodeID)

	if err != nil {
		fmt.Fprintf(errOut, "Failed to remove server node '%s': %v\n", serverNodeID, err)
		return
	}

	fmt.Fprintf(stdOut, "Server node '%s' removed successfully\n", serverNodeID)
}

func (s *LocalCommandsService) ServerStart(stdOut io.Writer, errOut io.Writer) {
	fmt.Fprintf(stdOut, "Starting waygate server\n")

	currentNode, err := s.NodesRepository.GetCurrentNode()

	if err != nil {
		fmt.Fprintf(errOut, "Failed to get current node: %v\n", err)
		return
	}

	if currentNode == nil {
		fmt.Fprintf(errOut, "No current node found\n")
		return
	}

	if currentNode.Role != types.NodeRoleServer {
		fmt.Fprintf(errOut, "Current node is not a server node\n")
		return
	}

	publicServices := []*publicservices.PublicService{}

	err = currentNode.SaveConfigs(publicServices, true)

	if err != nil {
		fmt.Fprintf(errOut, "Failed to save server node configs: %v\n", err)
		return
	}

	fmt.Fprintf(stdOut, "Server node configs saved to the disk successfully\n")

	for {
		fmt.Fprintf(stdOut, "Ensuring docker network is attached to all containers\n")

		err = dockerutils.EnsureDockerNetworkIsAttachedToAllContainers()

		if err != nil {
			fmt.Fprintf(errOut, "Failed to ensure docker network is attached to all containers: %v\n", err)
		}

		time.Sleep(time.Second * 30)
	}
}

func (s *LocalCommandsService) ServerStatus(creds *ssh.Credentials, stdOut io.Writer) {
	sshService := ssh.NewService()

	fmt.Fprintf(stdOut, "🔍 Checking waygate Server Status\n")
	fmt.Fprintf(stdOut, "==================================\n\n")

	// SSH Connection Check
	fmt.Fprintf(stdOut, "📡 SSH Connection\n")
	fmt.Fprintf(stdOut, "   Server: %s@%s:%d\n", creds.Username, creds.Host, creds.Port)

	err := sshService.Connect(creds)
	if err != nil {
		fmt.Fprintf(stdOut, "   Status: ❌ Failed\n")
		fmt.Fprintf(stdOut, "   Error:  %v\n\n", err)
		return
	}

	defer sshService.Close()
	fmt.Fprintf(stdOut, "   Status: ✅ Connected\n\n")

	// Docker Installation Check
	fmt.Fprintf(stdOut, "🐳 Docker Installation\n")
	dockerInstalled, err := sshService.IsDockerInstalled()

	if err != nil {
		fmt.Fprintf(stdOut, "   Status: ❌ Check Failed\n")
		fmt.Fprintf(stdOut, "   Error:  %v\n\n", err)
		return
	}

	var dockerVersion string

	if dockerInstalled {
		fmt.Fprintf(stdOut, "   Status: ✅ Installed\n")

		// Get Docker version
		dockerVersion, err = sshService.GetDockerVersion()
		if err == nil {
			fmt.Fprintf(stdOut, "   Version: %s\n", dockerVersion)
		}
	} else {
		fmt.Fprintf(stdOut, "   Status: ❌ Not Installed\n\n")
		fmt.Fprintf(stdOut, "💡 Install Docker to continue with waygate setup.\n\n")
		return
	}

	// Docker Permissions Check
	fmt.Fprintf(stdOut, "   Permissions: ")
	dockerAccessible, err := sshService.IsDockerAccessible()

	if err != nil {
		fmt.Fprintf(stdOut, "❌ Check Failed\n")
		fmt.Fprintf(stdOut, "   Error:  %v\n\n", err)
		return
	}

	if dockerAccessible {
		fmt.Fprintf(stdOut, "✅ User has access\n")
	} else {
		fmt.Fprintf(stdOut, "❌ User lacks permissions\n")
		fmt.Fprintf(stdOut, "💡 Add user to docker group.\n\n")
		return
	}
	fmt.Fprintf(stdOut, "\n")

	// waygate Server Status Check
	fmt.Fprintf(stdOut, "🚀 waygate Server Status\n")
	isRunning, err := sshService.IsWireportServerContainerRunning()
	if err != nil {
		fmt.Fprintf(stdOut, "   Status: ❌ Check Failed\n")
		fmt.Fprintf(stdOut, "   Error:  %v\n\n", err)
		return
	}

	var containerStatus string

	if isRunning {
		fmt.Fprintf(stdOut, "   Status: ✅ Running\n")

		// Get detailed container status
		containerStatus, err = sshService.GetWireportServerContainerStatus()
		if err == nil && containerStatus != "" {
			fmt.Fprintf(stdOut, "   Details: %s\n", containerStatus)
		}
	} else {
		fmt.Fprintf(stdOut, "   Status: ❌ Not Running\n")

		// Check if container exists but is stopped
		containerStatus, err = sshService.GetWireportServerContainerStatus()
		if err == nil && containerStatus != "" {
			fmt.Fprintf(stdOut, "   Details: %s\n", containerStatus)
		}

		fmt.Fprintf(stdOut, "   💡 Run 'waygate server up %s@%s:%d' to bootstrap waygate server and start it.\n", creds.Username, creds.Host, creds.Port)
	}
	fmt.Fprintf(stdOut, "\n")

	// Docker Network Status Check
	fmt.Fprintf(stdOut, "🌐 waygate Docker Network\n")
	networkStatus, err := sshService.GetWireportNetworkStatus()
	if err != nil {
		fmt.Fprintf(stdOut, "   Status: ❌ Check Failed\n")
		fmt.Fprintf(stdOut, "   Error:  %v\n\n", err)
		return
	}

	if networkStatus != "" {
		fmt.Fprintf(stdOut, "   Network: ✅ '%s' exists\n", strings.TrimSpace(networkStatus))
	} else {
		fmt.Fprintf(stdOut, "   Network: ❌ %s not found\n", config.Config.DockerNetworkName)
		fmt.Fprintf(stdOut, "💡 Network will be created when waygate server starts.\n")
	}
	fmt.Fprintf(stdOut, "\n")

	fmt.Fprintf(stdOut, "✨ Server Status check completed successfully!\n")
}

func (s *LocalCommandsService) ServerUp(creds *ssh.Credentials, stdOut io.Writer, errOut io.Writer, dockerSubnet string, commandsService *Service) {
	sshService := ssh.NewService()

	fmt.Fprintf(stdOut, "🚀 waygate Server Bootstrapping\n")
	fmt.Fprintf(stdOut, "================================\n\n")

	// SSH Connection
	fmt.Fprintf(stdOut, "📡 Connecting to server...\n")
	fmt.Fprintf(stdOut, "   Server: %s@%s:%d\n", creds.Username, creds.Host, creds.Port)

	err := sshService.Connect(creds)

	if err != nil {
		fmt.Fprintf(stdOut, "   Status: ❌ Failed\n")
		fmt.Fprintf(stdOut, "   Error:  %v\n\n", err)
		return
	}

	defer sshService.Close()
	fmt.Fprintf(stdOut, "   Status: ✅ Connected\n\n")

	stdOutWriter := bytes.NewBufferString("")
	errOutWriter := bytes.NewBufferString("")

	commandsService.ServerNew(stdOutWriter, errOutWriter, false, true, dockerSubnet)

	if len(errOutWriter.String()) > 0 || len(stdOutWriter.String()) == 0 {
		fmt.Fprintf(errOut, "%s\n", errOutWriter.String())
		fmt.Fprintf(stdOut, "%s\n", stdOutWriter.String())
		fmt.Fprintf(stdOut, "❌ Failed to connect server node to the waygate network\n")
		return
	}

	serverJoinToken := stdOutWriter.String()

	// Connection
	fmt.Fprintf(stdOut, "📦 Installing waygate server...\n")
	fmt.Fprintf(stdOut, "   Server: %s@%s:%d\n", creds.Username, creds.Host, creds.Port)

	_, err = sshService.InstallWireportServer(serverJoinToken)

	if err != nil {
		fmt.Fprintf(stdOut, "   Status: ❌ Connection Failed\n")
		fmt.Fprintf(stdOut, "   Error:  %v\n\n", err)
		return
	}

	fmt.Fprintf(stdOut, "   Status: ✅ Connection Completed\n\n")

	// Verification
	fmt.Fprintf(stdOut, "✅ Verifying installation...\n")
	installationConfirmed, err := sshService.IsWireportServerContainerRunning()
	if err != nil {
		fmt.Fprintf(stdOut, "   Status: ❌ Verification Failed\n")
		fmt.Fprintf(stdOut, "   Error:  %v\n\n", err)
		return
	}

	if installationConfirmed {
		fmt.Fprintf(stdOut, "   Status: ✅ Verified Successfully, Running\n")
		fmt.Fprintf(stdOut, "   🎉 Server has been successfully installed and connected to the waygate network!\n\n")
	} else {
		fmt.Fprintf(stdOut, "   Status: ❌ Verification Failed\n")
		fmt.Fprintf(stdOut, "   💡 Server node container was not found running after installation, please check the logs on the server node for more details.\n\n")
	}

	fmt.Fprintf(stdOut, "✨ Server Bootstrapping completed successfully!\n")
}

func (s *LocalCommandsService) ServerDown(creds *ssh.Credentials, stdOut io.Writer, errOut io.Writer) {
	currentNode, err := s.NodesRepository.GetCurrentNode()

	if err != nil {
		fmt.Fprintf(errOut, "Error getting current node: %v\n", err)
		return
	}

	switch currentNode.Role {
	case node_types.NodeRoleServer:
		err = dockerutils.DetachDockerNetworkFromAllContainers()

		if err != nil {
			fmt.Fprintf(errOut, "Error detaching docker network: %v\n", err)
			return
		}

		err = dockerutils.RemoveDockerNetwork()

		if err != nil {
			fmt.Fprintf(errOut, "Error removing docker network: %v\n", err)
			return
		}

		return
	}

	if creds == nil {
		fmt.Fprintf(errOut, "Error: SSH credentials are required\n")
		return
	}

	sshService := ssh.NewService()

	fmt.Fprintf(stdOut, "🚀 waygate Server Teardown\n")
	fmt.Fprintf(stdOut, "===========================\n\n")

	// SSH Connection
	fmt.Fprintf(stdOut, "📡 Connecting to server...\n")
	fmt.Fprintf(stdOut, "   Server: %s@%s:%d\n", creds.Username, creds.Host, creds.Port)

	err = sshService.Connect(creds)

	if err != nil {
		fmt.Fprintf(stdOut, "   Status: ❌ Failed\n")
		fmt.Fprintf(stdOut, "   Error:  %v\n\n", err)
		return
	}

	defer sshService.Close()
	fmt.Fprintf(stdOut, "   Status: ✅ Connected\n\n")

	// Check if server is running
	isRunning, err := sshService.IsWireportServerContainerRunning()
	if err != nil {
		fmt.Fprintf(stdOut, "   Status: ❌ Check Failed\n")
		fmt.Fprintf(stdOut, "   Error:  %v\n\n", err)
		return
	}

	if !isRunning {
		fmt.Fprintf(stdOut, "   Status: ❌ Not Running\n")
		fmt.Fprintf(stdOut, "   💡 waygate server is not running\n\n")
		return
	}

	// Teardown waygate server
	fmt.Fprintf(stdOut, "🛑 Server node Teardown\n")
	fmt.Fprintf(stdOut, "   Server: %s@%s:%d\n", creds.Username, creds.Host, creds.Port)

	_, err = sshService.TeardownWireportServer()
	if err != nil {
		fmt.Fprintf(stdOut, "   Status: ❌ Teardown Failed\n")
		fmt.Fprintf(stdOut, "   Error:  %v\n\n", err)
		return
	}

	fmt.Fprintf(stdOut, "   Status: ✅ Teardown Completed\n\n")

	fmt.Fprintf(stdOut, "✨ Server Teardown completed successfully!\n")
}

func (s *LocalCommandsService) ServerList(requestFromNodeID *string, stdOut io.Writer, errOut io.Writer) {
	serverNodes, err := s.NodesRepository.GetNodesByRole(node_types.NodeRoleServer)

	if err != nil {
		fmt.Fprintf(errOut, "Error getting nodes: %v\n", err)
		return
	}

	fmt.Fprintf(stdOut, "SERVER PRIVATE IP\n")
	fmt.Fprintf(stdOut, "%s\n", strings.Repeat("=", 80))

	if len(serverNodes) > 0 {
		for _, serverNode := range serverNodes {
			if requestFromNodeID != nil && serverNode.ID == *requestFromNodeID {
				fmt.Fprintf(stdOut, "%s*\n", serverNode.WGConfig.Interface.Address.String())
			} else {
				fmt.Fprintf(stdOut, "%s\n", serverNode.WGConfig.Interface.Address.String())
			}
		}
	} else {
		fmt.Fprintf(stdOut, "No servers are registered on this gateway.\nUse 'waygate server new' command to create a new server node join request.\n")
	}
}

func (s *LocalCommandsService) ServerUpgrade(creds *ssh.Credentials, stdOut io.Writer, _ io.Writer) {
	sshService := ssh.NewService()

	fmt.Fprintf(stdOut, "🔄 waygate Server Upgrade\n")
	fmt.Fprintf(stdOut, "==========================\n\n")

	// SSH Connection
	fmt.Fprintf(stdOut, "📡 Connecting to server...\n")
	fmt.Fprintf(stdOut, "   Server: %s@%s:%d\n", creds.Username, creds.Host, creds.Port)

	err := sshService.Connect(creds)

	if err != nil {
		fmt.Fprintf(stdOut, "   Status: ❌ Failed\n")
		fmt.Fprintf(stdOut, "   Error:  %v\n\n", err)
		return
	}

	defer sshService.Close()
	fmt.Fprintf(stdOut, "   Status: ✅ Connected\n\n")

	// Upgrade waygate server
	fmt.Fprintf(stdOut, "🔄 Upgrading waygate server...\n")
	fmt.Fprintf(stdOut, "   Server: %s@%s:%d\n", creds.Username, creds.Host, creds.Port)

	success, err := sshService.UpgradeWireportServer()

	if err != nil {
		fmt.Fprintf(stdOut, "   Status: ❌ Failed\n")
		fmt.Fprintf(stdOut, "   Error:  %v\n\n", err)
		return
	}

	if success {
		fmt.Fprintf(stdOut, "   Status: ✅ Upgraded Successfully\n")
		fmt.Fprintf(stdOut, "   🎉 waygate server node has been successfully upgraded!\n\n")
	} else {
		fmt.Fprintf(stdOut, "   Status: ❌ Failed\n")
		fmt.Fprintf(stdOut, "   💡 Upgrade process completed but may not have been successful.\n\n")
	}

	fmt.Fprintf(stdOut, "✨ Server Upgrade completed!\n")
}

// client

func (s *LocalCommandsService) ClientNew(stdOut io.Writer, errOut io.Writer, joinRequestClientCreation bool, quietClientCreation bool, waitClientCreation bool) {
	currentNode, err := s.NodesRepository.GetCurrentNode()

	if err != nil || currentNode == nil {
		if !waitClientCreation {
			fmt.Fprintf(errOut, "Current node not found, skipping client creation\n")
			return
		}

		// wait for 10 seconds with retries every 1 second
		for range 10 {
			time.Sleep(1 * time.Second)

			currentNode, err = s.NodesRepository.GetCurrentNode()

			if err == nil && currentNode != nil {
				if !quietClientCreation {
					fmt.Fprintf(stdOut, "Current node found, creating client\n")
				}

				break
			}
		}

		if err != nil || currentNode == nil {
			fmt.Fprintf(errOut, "Failed to get current node after waiting and multiple retries: %v\n", err)
			return
		}
	}

	switch currentNode.Role {
	case types.NodeRoleGateway:
		totalWireguardClients, availableWireguardClients, err := s.NodesRepository.TotalAvailableWireguardClients()

		if err != nil {
			fmt.Fprintf(errOut, "Failed to count available wireguard clients: %v\n", err)
			return
		}

		totalJoinRequests := s.JoinRequestsRepository.CountAll()

		if availableWireguardClients <= 0 || totalJoinRequests >= availableWireguardClients {
			fmt.Fprintf(errOut, "No available wireguard client slots. Please delete some client/server nodes (total used: %d) or client/server join-requests (total used: %d) to free up some wireguard client slots.\n", totalWireguardClients, totalJoinRequests)
			return
		}

		if joinRequestClientCreation {
			// create join request
			joinRequestID := uuid.New().String()

			err = currentNode.GatewayCertBundle.AddClient(mtls.Options{
				CommonName: joinRequestID,
				Expiry:     config.Config.CertExpiry,
			})

			if err != nil {
				fmt.Fprintf(errOut, "Failed to add client to gateway cert bundle: %v\n", err)
				return
			}

			err = s.NodesRepository.SaveNode(currentNode)

			if err != nil {
				fmt.Fprintf(errOut, "Failed to save gateway node: %v\n", err)
				return
			}

			var clientCertBundle *mtls.FullClientBundle
			clientCertBundle, err = currentNode.GatewayCertBundle.GetClientBundlePublic(joinRequestID)

			if err != nil {
				fmt.Fprintf(errOut, "Failed to get client cert bundle: %v\n", err)
				return
			}

			var joinRequest *joinrequeststypes.JoinRequest

			joinRequest, err = s.JoinRequestsRepository.Create(joinRequestID, types.UDPAddrMarshable{
				UDPAddr: net.UDPAddr{
					IP:   net.ParseIP(*currentNode.WGPublicIP),
					Port: int(config.Config.ControlServerPort),
				},
			}, nil, types.NodeRoleClient, clientCertBundle)

			if err != nil {
				fmt.Fprintf(errOut, "Failed to create join request: %v\n", err)
				return
			}

			var joinRequestBase64 *string

			joinRequestBase64, err = joinRequest.ToBase64()

			if err != nil {
				fmt.Fprintf(errOut, "Failed to encode join request: %v\n", err)
				return
			}

			if !quietClientCreation {
				fmt.Fprintf(stdOut, "New client created, use the following command to connect to the network:\n\nwaygate join %s\n", *joinRequestBase64)
			} else {
				fmt.Fprintf(stdOut, "%s\n", *joinRequestBase64)
			}
		} else {
			var clientNode *node_types.Node

			clientNode, err = s.NodesRepository.CreateClient()

			if err != nil {
				fmt.Fprintf(errOut, "Failed to create client: %v\n", err)
				return
			}

			// save configs & restart services
			publicServices := s.PublicServicesRepository.GetAll()

			currentNode, err = s.NodesRepository.GetCurrentNode()

			if err != nil {
				fmt.Fprintf(errOut, "Failed to get a fresh current node instance after creating client: %v\n", err)
				return
			}

			err = currentNode.SaveConfigs(publicServices, false)

			if err != nil {
				fmt.Fprintf(errOut, "Failed to save gateway configs: %v\n", err)
				return
			}

			err = networkapps.RestartNetworkApps(true, false, false)

			if err != nil {
				fmt.Fprintf(errOut, "Failed to restart services: %v\n", err)
			}

			wireguardConfig, _ := clientNode.GetFormattedWireguardConfig()

			if !quietClientCreation {
				fmt.Fprintf(stdOut, "# New waygate client has been successfully created!\n")
				fmt.Fprintf(stdOut, "# Use the WireGuard config below on your client device to connect to the waygate network:\n\n")
				fmt.Fprintf(stdOut, "%s\n", *wireguardConfig)
			} else {
				fmt.Fprintf(stdOut, "%s\n", *wireguardConfig)
			}
		}
	}
}

func (s *LocalCommandsService) ClientList(requestFromNodeID *string, stdOut io.Writer, errOut io.Writer) {
	clientNodes, err := s.NodesRepository.GetNodesByRole(types.NodeRoleClient)

	if err != nil {
		fmt.Fprintf(errOut, "Error getting nodes: %v\n", err)
		return
	}

	fmt.Fprintf(stdOut, "CLIENT PRIVATE IP\n")
	fmt.Fprintf(stdOut, "%s\n", strings.Repeat("=", 80))

	if len(clientNodes) > 0 {
		for _, clientNode := range clientNodes {
			if requestFromNodeID != nil && clientNode.ID == *requestFromNodeID {
				fmt.Fprintf(stdOut, "%s*\n", clientNode.WGConfig.Interface.Address.String())
			} else {
				fmt.Fprintf(stdOut, "%s\n", clientNode.WGConfig.Interface.Address.String())
			}
		}
	} else {
		fmt.Fprintf(stdOut, "No clients are registered on this gateway\n")
	}
}

// service

func (s *LocalCommandsService) ServicePublish(stdOut io.Writer, errOut io.Writer, localProtocol string, localHost string, localPort uint16, publicProtocol string, publicHost string, publicPort uint16) {
	err := s.PublicServicesRepository.Save(&publicservices.PublicService{
		LocalProtocol:  localProtocol,
		LocalHost:      localHost,
		LocalPort:      localPort,
		PublicProtocol: publicProtocol,
		PublicHost:     publicHost,
		PublicPort:     publicPort,
	})

	if err != nil {
		fmt.Fprintf(errOut, "Error creating public service: %v\n", err)
		return
	}

	gatewayNode, err := s.NodesRepository.GetGatewayNode()

	if err != nil {
		fmt.Fprintf(errOut, "Error getting gateway node: %v\n", err)
		return
	}

	err = gatewayNode.SaveConfigs(s.PublicServicesRepository.GetAll(), false)

	if err != nil {
		fmt.Fprintf(errOut, "Error saving gateway node configs: %v\n", err)
		return
	}

	err = networkapps.RestartNetworkApps(false, false, true)

	if err != nil {
		fmt.Fprintf(errOut, "Error restarting services: %v\n", err)
		return
	}

	fmt.Fprintf(stdOut, "✅ Service %s://%s:%d is now published on\n\n\t\t%s://%s:%d\n\n\n", localProtocol, localHost, localPort, publicProtocol, publicHost, publicPort)
}

func (s *LocalCommandsService) ServiceUnpublish(stdOut io.Writer, errOut io.Writer, publicProtocol string, publicHost string, publicPort uint16) {
	serviceDeleted := s.PublicServicesRepository.Delete(publicProtocol, publicHost, publicPort)

	if serviceDeleted {
		gatewayNode, err := s.NodesRepository.GetGatewayNode()

		if err != nil {
			fmt.Fprintf(errOut, "Error getting gateway node: %v\n", err)
			return
		}

		err = gatewayNode.SaveConfigs(s.PublicServicesRepository.GetAll(), false)

		if err != nil {
			fmt.Fprintf(errOut, "Error saving gateway node configs: %v\n", err)
			return
		}

		err = networkapps.RestartNetworkApps(false, false, true)

		if err != nil {
			fmt.Fprintf(errOut, "Error restarting services: %v\n", err)
			return
		}

		fmt.Fprintf(stdOut, "✅ Service %s://%s:%d is now unpublished\n", publicProtocol, publicHost, publicPort)
	} else {
		fmt.Fprintf(stdOut, "❌ Service %s://%s:%d was not found or was already unpublished earliner\n", publicProtocol, publicHost, publicPort)
	}
}

func (s *LocalCommandsService) ServiceList(stdOut io.Writer, _ io.Writer) {
	services := s.PublicServicesRepository.GetAll()

	fmt.Fprintf(stdOut, "PUBLIC\t->\tLOCAL\n")
	fmt.Fprintf(stdOut, "%s\n", strings.Repeat("=", 80))

	if len(services) > 0 {
		for _, service := range services {
			fmt.Fprintf(stdOut, "%s://%s:%d\t->\t%s://%s:%d\n", service.PublicProtocol, service.PublicHost, service.PublicPort, service.LocalProtocol, service.LocalHost, service.LocalPort)
		}
	} else {
		fmt.Fprintf(stdOut, "No services are published on the gateway.\nUse 'waygate service publish' to publish a new service.\n")
	}
}

// service params

func (s *LocalCommandsService) ServiceParamNew(stdOut io.Writer, errOut io.Writer, publicProtocol string, publicHost string, publicPort uint16, paramType publicservices.PublicServiceParamType, paramValue string) {
	added := s.PublicServicesRepository.AddParam(publicProtocol, publicHost, publicPort, paramType, paramValue)

	if added {
		gatewayNode, err := s.NodesRepository.GetGatewayNode()

		if err != nil {
			fmt.Fprintf(errOut, "Error getting gateway node: %v\n", err)
			return
		}

		err = gatewayNode.SaveConfigs(s.PublicServicesRepository.GetAll(), false)

		if err != nil {
			fmt.Fprintf(errOut, "Error saving gateway node configs: %v\n", err)
			return
		}

		err = networkapps.RestartNetworkApps(false, false, true)

		if err != nil {
			fmt.Fprintf(errOut, "Error restarting services: %v\n", err)
			return
		}

		fmt.Fprintf(stdOut, "✅ Parameter '%s' successfully added to service %s://%s:%d\n", paramValue, publicProtocol, publicHost, publicPort)
	} else {
		fmt.Fprintf(stdOut, "❌ Parameter '%s' was not added to service %s://%s:%d (probably already exists)\n", paramValue, publicProtocol, publicHost, publicPort)
	}
}

func (s *LocalCommandsService) ServiceParamRemove(stdOut io.Writer, errOut io.Writer, publicProtocol string, publicHost string, publicPort uint16, paramType publicservices.PublicServiceParamType, paramValue string) {
	removed := s.PublicServicesRepository.RemoveParam(publicProtocol, publicHost, publicPort, paramType, paramValue)

	if removed {
		gatewayNode, err := s.NodesRepository.GetGatewayNode()

		if err != nil {
			fmt.Fprintf(errOut, "Error getting gateway node: %v\n", err)
			return
		}

		err = gatewayNode.SaveConfigs(s.PublicServicesRepository.GetAll(), false)

		if err != nil {
			fmt.Fprintf(errOut, "Error saving gateway node configs: %v\n", err)
			return
		}

		err = networkapps.RestartNetworkApps(false, false, true)

		if err != nil {
			fmt.Fprintf(errOut, "Error restarting services: %v\n", err)
			return
		}

		fmt.Fprintf(stdOut, "✅ Parameter '%s' successfully removed from service %s://%s:%d\n", paramValue, publicProtocol, publicHost, publicPort)
	} else {
		fmt.Fprintf(stdOut, "❌ Parameter '%s' was not removed from service %s://%s:%d (probably not found)\n", paramValue, publicProtocol, publicHost, publicPort)
	}
}

func (s *LocalCommandsService) ServiceParamList(stdOut io.Writer, errOut io.Writer, publicProtocol string, publicHost string, publicPort uint16) {
	service, err := s.PublicServicesRepository.Get(publicProtocol, publicHost, publicPort)

	if err != nil {
		fmt.Fprintf(errOut, "Error getting service: %v\n", err)
		return
	}

	fmt.Fprintf(stdOut, "SERVICE PARAMS: %s://%s:%d\n", service.PublicProtocol, service.PublicHost, service.PublicPort)
	fmt.Fprintf(stdOut, "%s\n", strings.Repeat("=", 80))

	if len(service.Params) > 0 {
		for _, param := range service.Params {
			fmt.Fprintf(stdOut, "%s\n", param.ParamValue)
		}
	} else {
		fmt.Fprintf(stdOut, "No params are set for this service\nUse 'waygate service param new' to add a new param.\n")
	}

	fmt.Fprintf(stdOut, "\n")
}

// join

func (s *LocalCommandsService) Join(stdOut io.Writer, errOut io.Writer, joinToken string) {
	joinRequest := &joinrequeststypes.JoinRequest{}

	err := joinRequest.FromBase64(joinToken)

	if err != nil {
		fmt.Fprintf(errOut, "Failed to parse join request: %v\n", err)
		return
	}

	joinRequestsService := joinrequests.NewAPIService(&joinRequest.ClientCertBundle)

	response, err := joinRequestsService.Join(joinToken, joinRequest)

	if err != nil {
		fmt.Fprintf(errOut, "Failed to join network: %v\n", err)
		return
	}

	currentNode := response.NodeConfig

	if currentNode == nil {
		fmt.Fprintf(errOut, "Failed to get node config from join response\n")
		return
	}

	currentNode.IsCurrentNode = true

	err = s.NodesRepository.SaveNode(currentNode)

	if err != nil {
		fmt.Fprintf(errOut, "Failed to save node config: %v\n", err)
		return
	}

	switch currentNode.Role {
	case types.NodeRoleServer:
		fmt.Fprintf(stdOut, "Setting up server node (configs and network)\n")

		if currentNode.DockerSubnet == nil {
			fmt.Fprintf(errOut, "Failed to get docker subnet from node config\n")
			return
		}

		dockerSubnet, err := types.ParseIPNetMarshable(currentNode.DockerSubnet.String(), true)

		if err != nil {
			fmt.Fprintf(errOut, "Failed to parse docker subnet: %v\n", err)
			return
		}

		err = dockerutils.EnsureDockerNetworkExistsAndAttached(dockerSubnet)

		if err != nil {
			fmt.Fprintf(errOut, "Failed to ensure docker network %s with subnet %s exists and is attached: %v\n", config.Config.DockerNetworkName, dockerSubnet.String(), err)
			return
		}

		publicServices := []*publicservices.PublicService{}

		err = currentNode.SaveConfigs(publicServices, true)

		if err != nil {
			fmt.Fprintf(errOut, "Failed to save node configs: %v\n", err)
			return
		}

		fmt.Fprintf(stdOut, "Successfully saved node config to the database\n")
	case types.NodeRoleClient:
		wireguardConfig, err := currentNode.GetFormattedWireguardConfig()

		if err != nil {
			fmt.Fprintf(errOut, "Failed to get wireguard config: %v\n", err)
			return
		}

		fmt.Fprintf(stdOut, "\n%s\n", *wireguardConfig)
		fmt.Fprintf(errOut, "\n⤵ waygate WireGuard config has been dumped\n\n")
	default:
		fmt.Fprintf(errOut, "Invalid node role: %s\n", currentNode.Role)
		return
	}
}
