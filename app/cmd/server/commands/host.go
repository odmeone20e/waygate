package commands

import (
	"fmt"
	"net"
	"net/http"

	"waygate/cmd/server/config"
	"waygate/internal/logger"
	"waygate/internal/nodes/types"
	"waygate/internal/routes"

	"github.com/spf13/cobra"
)

var HostStartConfigureOnly bool = false

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
			logger.Fatal("Failed to get public IP: %v", err)
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
			logger.Error("waygate host node start failed: %v", err)
			logger.Fatal("Failed to ensure host node: %v", err)
			return
		}

		publicServices := public_services_repository.GetAll()

		err = hostNode.SaveConfigs(publicServices, true)

		if err != nil {
			logger.Fatal("Failed to save configs: %v", err)
			return
		}

		if !HostStartConfigureOnly {
			logger.Info("waygate server has started on host: %s", *hostNode.WGPublicIp)
		} else {
			logger.Info("waygate has been configured on the host: %s", *hostNode.WGPublicIp)
		}

		if !HostStartConfigureOnly {
			// Block on the server error channel
			if err := <-serverError; err != nil {
				logger.Fatal("Server error: %v", err)
			}
		}
	},
}

func init() {
	HostCmd.AddCommand(StartHostCmd)
	StartHostCmd.Flags().BoolVar(&HostStartConfigureOnly, "configure", false, "Configure waygate in host mode without making it available for external connections")
}
