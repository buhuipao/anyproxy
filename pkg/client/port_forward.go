package client

import (
	"fmt"

	"github.com/buhuipao/anyproxy/pkg/common/protocol"
	"github.com/buhuipao/anyproxy/pkg/logger"
)

// sendPortForwardingRequest sends port forwarding request
func (c *Client) sendPortForwardingRequest() error {
	if len(c.config.OpenPorts) == 0 {
		return nil
	}

	logger.Debug("Preparing port forwarding request", "client_id", c.getClientID(), "port_count", len(c.config.OpenPorts))

	// Build port configuration list
	ports := make([]protocol.PortConfig, 0, len(c.config.OpenPorts))
	for _, port := range c.config.OpenPorts {
		ports = append(ports, protocol.PortConfig{
			RemotePort: port.RemotePort,
			LocalPort:  port.LocalPort,
			LocalHost:  port.LocalHost,
			Protocol:   port.Protocol,
		})
	}

	// 🚀 Lock-free access to connection
	state := c.getConnState()
	if state == nil || state.conn == nil {
		return fmt.Errorf("connection not established")
	}

	// Send port forwarding request using binary format
	binaryMsg := protocol.PackPortForwardMessage(c.getClientID(), ports)
	return state.conn.WriteMessage(binaryMsg)
}

// handlePortForwardResponse handles port forwarding response
func (c *Client) handlePortForwardResponse(msg map[string]interface{}) {
	success, ok := msg["success"].(bool)
	if !ok {
		logger.Error("Invalid port forward response - missing success field", "client_id", c.getClientID())
		return
	}

	if success {
		logger.Info("Port forwarding setup successful", "client_id", c.getClientID())
	} else {
		errorMsg, _ := msg["error"].(string)
		logger.Error("Port forwarding setup failed", "client_id", c.getClientID(), "err", errorMsg)
	}

	// Log specific port statuses (if available)
	if portStatuses, ok := msg["port_statuses"].([]interface{}); ok {
		for _, status := range portStatuses {
			if statusMap, ok := status.(map[string]interface{}); ok {
				port, _ := statusMap["port"].(float64)
				success, _ := statusMap["success"].(bool)
				if success {
					logger.Debug("  Port forwarding active", "port", int(port))
				} else {
					logger.Error("  Port forwarding failed", "port", int(port))
				}
			}
		}
	}
}
