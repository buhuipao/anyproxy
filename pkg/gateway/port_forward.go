package gateway

import (
	"context"
	"fmt"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	commonctx "github.com/buhuipao/anyproxy/pkg/common/context"
	"github.com/buhuipao/anyproxy/pkg/common/monitoring"
	"github.com/buhuipao/anyproxy/pkg/common/protocol"
	"github.com/buhuipao/anyproxy/pkg/common/utils"
	"github.com/buhuipao/anyproxy/pkg/config"
	"github.com/buhuipao/anyproxy/pkg/logger"
)

// PortKey represents a port with protocol information for unique identification
type PortKey struct {
	Port     int
	Protocol string
}

// String returns a string representation of the PortKey
func (pk PortKey) String() string {
	return fmt.Sprintf("%s:%d", pk.Protocol, pk.Port)
}

// PortForwardManager port forwarding manager
type PortForwardManager struct {
	// Map of client ID to their forwarded ports (port -> PortListener)
	clientPorts map[string]map[PortKey]*PortListener
	// Map of (port, protocol) to client ID (for conflict detection)
	portOwners map[PortKey]string
	mutex      sync.RWMutex
	ctx        context.Context
	cancel     context.CancelFunc
	wg         sync.WaitGroup
}

// PortListener port listener
type PortListener struct {
	Port       int
	Protocol   string
	ClientID   string
	LocalHost  string
	LocalPort  int
	Listener   net.Listener   // For TCP
	PacketConn net.PacketConn // For UDP
	Client     *ClientConn
	ctx        context.Context
	cancel     context.CancelFunc
}

// NewPortForwardManager creates a new port forward manager.
func NewPortForwardManager() *PortForwardManager {
	logger.Info("Creating new port forwarding manager")

	ctx, cancel := context.WithCancel(context.Background())
	manager := &PortForwardManager{
		clientPorts: make(map[string]map[PortKey]*PortListener),
		portOwners:  make(map[PortKey]string),
		ctx:         ctx,
		cancel:      cancel,
	}

	logger.Debug("Port forwarding manager initialized successfully", "client_ports_capacity", len(manager.clientPorts), "port_owners_capacity", len(manager.portOwners))

	return manager
}

// OpenPorts opens port forwarding for client
func (pm *PortForwardManager) OpenPorts(client *ClientConn, openPorts []config.OpenPort) error {
	if client == nil {
		logger.Error("Port opening failed: client cannot be nil")
		return fmt.Errorf("client cannot be nil")
	}

	logger.Info("Opening ports for client", "client_id", client.ID, "port_count", len(openPorts))

	pm.mutex.Lock()
	defer pm.mutex.Unlock()

	// Check if manager is shutting down
	select {
	case <-pm.ctx.Done():
		logger.Warn("Port opening rejected: manager is shutting down", "client_id", client.ID)
		return fmt.Errorf("port forward manager is shutting down")
	default:
	}

	// Initialize client ports map if it doesn't exist
	if pm.clientPorts[client.ID] == nil {
		pm.clientPorts[client.ID] = make(map[PortKey]*PortListener)
		logger.Debug("Initialized port map for new client", "client_id", client.ID)
	}

	var errors []error
	successfulPorts := []*PortListener{}
	conflictPorts := []PortKey{}
	duplicatePorts := []PortKey{}

	// Log details of each port request
	for i, openPort := range openPorts {
		logger.Debug("Processing port request", "client_id", client.ID, "port_index", i, "remote_port", openPort.RemotePort, "local_host", openPort.LocalHost, "local_port", openPort.LocalPort, "protocol", openPort.Protocol)
	}

	for _, openPort := range openPorts {
		// Create port key with protocol information
		portKey := PortKey{
			Port:     openPort.RemotePort,
			Protocol: openPort.Protocol,
		}

		// Check if port+protocol combination is already in use
		if existingClientID, exists := pm.portOwners[portKey]; exists {
			if existingClientID != client.ID {
				conflictPorts = append(conflictPorts, portKey)
				logger.Warn("Port conflict detected", "client_id", client.ID, "port_key", portKey.String(), "existing_owner", existingClientID)
				errors = append(errors, fmt.Errorf("port %d (%s) already in use by client %s", openPort.RemotePort, openPort.Protocol, existingClientID))
				continue
			}
			// Same client requesting same port+protocol combination - skip
			duplicatePorts = append(duplicatePorts, portKey)
			logger.Info("Port already opened by same client", "port_key", portKey.String(), "client_id", client.ID)
			continue
		}

		// Create port listener
		logger.Debug("Creating port listener", "client_id", client.ID, "port_key", portKey.String())

		portListener, err := pm.createPortListener(client, openPort)
		if err != nil {
			logger.Error("Failed to create port listener", "client_id", client.ID, "port_key", portKey.String(), "err", err)
			errors = append(errors, fmt.Errorf("failed to open port %d (%s): %v", openPort.RemotePort, openPort.Protocol, err))
			continue
		}

		// Register the port with protocol information
		pm.clientPorts[client.ID][portKey] = portListener
		pm.portOwners[portKey] = client.ID
		successfulPorts = append(successfulPorts, portListener)

		logger.Info("Port forwarding created successfully", "client_id", client.ID, "remote_port", openPort.RemotePort, "local_host", openPort.LocalHost, "local_port", openPort.LocalPort, "protocol", openPort.Protocol)
	}

	// Start listening on successful ports
	logger.Debug("Starting listeners for successful ports", "client_id", client.ID, "successful_count", len(successfulPorts))

	for i, portListener := range successfulPorts {
		logger.Debug("Starting port listener", "client_id", client.ID, "port", portListener.Port, "protocol", portListener.Protocol, "listener_index", i)

		pm.wg.Add(1)
		go func(pl *PortListener) {
			defer pm.wg.Done()
			pm.handlePortListener(pl)
		}(portListener)
	}

	// If we have any errors, return them
	if len(errors) > 0 {
		logger.Error("Port opening completed with errors", "client_id", client.ID, "requested_ports", len(openPorts), "successful_ports", len(successfulPorts), "error_count", len(errors), "conflict_ports", conflictPorts, "duplicate_ports", duplicatePorts)
		return fmt.Errorf("failed to open some ports: %v", errors)
	}

	logger.Info("All ports opened successfully", "client_id", client.ID, "successful_ports", len(successfulPorts), "duplicate_ports", len(duplicatePorts), "total_requested", len(openPorts))

	return nil
}

// createPortListener creates port listener
func (pm *PortForwardManager) createPortListener(client *ClientConn, openPort config.OpenPort) (*PortListener, error) {
	logger.Debug("Creating port listener", "client_id", client.ID, "port", openPort.RemotePort, "protocol", openPort.Protocol, "local_target", fmt.Sprintf("%s:%d", openPort.LocalHost, openPort.LocalPort))

	// Support both TCP and UDP
	if openPort.Protocol != protocol.ProtocolTCP && openPort.Protocol != protocol.ProtocolUDP {
		logger.Error("Unsupported protocol for port forwarding", "client_id", client.ID, "port", openPort.RemotePort, "protocol", openPort.Protocol, "supported_protocols", []string{protocol.ProtocolTCP, protocol.ProtocolUDP})
		return nil, fmt.Errorf("protocol %s not supported, only TCP and UDP are supported", openPort.Protocol)
	}

	ctx, cancel := context.WithCancel(pm.ctx)
	addr := fmt.Sprintf(":%d", openPort.RemotePort)
	portListener := &PortListener{
		Port:      openPort.RemotePort,
		Protocol:  openPort.Protocol,
		ClientID:  client.ID,
		LocalHost: openPort.LocalHost,
		LocalPort: openPort.LocalPort,
		Client:    client,
		ctx:       ctx,
		cancel:    cancel,
	}

	logger.Debug("Port listener structure created", "client_id", client.ID, "port", openPort.RemotePort, "bind_addr", addr)

	if openPort.Protocol == protocol.ProtocolTCP {
		// Create TCP listener
		logger.Debug("Creating TCP listener", "client_id", client.ID, "port", openPort.RemotePort, "bind_addr", addr)

		listener, err := net.Listen(protocol.ProtocolTCP, addr)
		if err != nil {
			logger.Error("Failed to create TCP listener", "client_id", client.ID, "port", openPort.RemotePort, "bind_addr", addr, "err", err)
			cancel()
			return nil, fmt.Errorf("failed to listen on TCP port %d: %v", openPort.RemotePort, err)
		}
		portListener.Listener = listener

		logger.Debug("TCP listener created successfully", "client_id", client.ID, "port", openPort.RemotePort, "local_addr", listener.Addr())
	} else { // UDP
		// Create UDP listener
		logger.Debug("Creating UDP packet connection", "client_id", client.ID, "port", openPort.RemotePort, "bind_addr", addr)

		packetConn, err := net.ListenPacket("udp", addr)
		if err != nil {
			logger.Error("Failed to create UDP packet connection", "client_id", client.ID, "port", openPort.RemotePort, "bind_addr", addr, "err", err)
			cancel()
			return nil, fmt.Errorf("failed to listen on UDP port %d: %v", openPort.RemotePort, err)
		}
		portListener.PacketConn = packetConn

		logger.Debug("UDP packet connection created successfully", "client_id", client.ID, "port", openPort.RemotePort, "local_addr", packetConn.LocalAddr())
	}

	logger.Debug("Port listener created successfully", "client_id", client.ID, "port", openPort.RemotePort, "protocol", openPort.Protocol, "local_target", fmt.Sprintf("%s:%d", openPort.LocalHost, openPort.LocalPort))

	return portListener, nil
}

// handlePortListener handles port listener
func (pm *PortForwardManager) handlePortListener(portListener *PortListener) {
	defer func() {
		// Cancel the port listener context
		portListener.cancel()

		// Close the appropriate connection based on protocol
		if portListener.Protocol == protocol.ProtocolTCP && portListener.Listener != nil {
			if err := portListener.Listener.Close(); err != nil {
				logger.Warn("Error closing TCP listener", "port", portListener.Port, "err", err)
			}
		} else if portListener.PacketConn != nil {
			if err := portListener.PacketConn.Close(); err != nil {
				logger.Warn("Error closing UDP packet connection", "port", portListener.Port, "err", err)
			}
		}

		logger.Info("Port listener stopped", "port", portListener.Port, "client_id", portListener.ClientID)
	}()

	logger.Info("Started listening for port forwarding", "port", portListener.Port, "protocol", portListener.Protocol, "client_id", portListener.ClientID, "local_target", net.JoinHostPort(portListener.LocalHost, strconv.Itoa(portListener.LocalPort)))

	if portListener.Protocol == protocol.ProtocolTCP {
		pm.handleTCPPortListener(portListener)
	} else {
		pm.handleUDPPortListener(portListener)
	}
}

// handleTCPPortListener handles TCP port listening
func (pm *PortForwardManager) handleTCPPortListener(portListener *PortListener) {
	// Create channels for async operations
	connCh := make(chan net.Conn, 1)
	errCh := make(chan error, 1)

	// Start accepting connections in a separate goroutine
	go func() {
		defer close(connCh)
		defer close(errCh)

		for {
			conn, err := portListener.Listener.Accept()
			if err != nil {
				select {
				case errCh <- err:
				case <-portListener.ctx.Done():
				}
				return
			}

			select {
			case connCh <- conn:
			case <-portListener.ctx.Done():
				if err := conn.Close(); err != nil {
					logger.Warn("Error closing connection on context cancellation", "err", err)
				}
				return
			}
		}
	}()

	for {
		select {
		case <-portListener.ctx.Done():
			return
		case conn, ok := <-connCh:
			if !ok {
				return
			}
			// Handle the connection asynchronously
			pm.wg.Add(1)
			go func(incomingConn net.Conn) {
				defer pm.wg.Done()
				pm.handleForwardedConnection(portListener, incomingConn)
			}(conn)
		case err, ok := <-errCh:
			if !ok {
				return
			}
			// Check if the error is due to listener being closed (normal shutdown)
			if strings.Contains(err.Error(), "use of closed network connection") {
				logger.Debug("Port listener closed", "port", portListener.Port)
				return
			}
			logger.Error("Error accepting connection on forwarded port", "port", portListener.Port, "err", err)
			return
		}
	}
}

// handleUDPPortListener handles UDP port listening
func (pm *PortForwardManager) handleUDPPortListener(portListener *PortListener) {
	buffer := make([]byte, 65536) // Maximum UDP packet size

	// Create channels for async operations
	type udpPacket struct {
		data []byte
		addr net.Addr
	}
	packetCh := make(chan udpPacket, 10)
	errCh := make(chan error, 1)

	// Start reading packets in a separate goroutine
	go func() {
		defer close(packetCh)
		defer close(errCh)

		for {
			n, addr, err := portListener.PacketConn.ReadFrom(buffer)
			if err != nil {
				select {
				case errCh <- err:
				case <-portListener.ctx.Done():
				}
				return
			}

			// Make a copy of the data
			data := make([]byte, n)
			copy(data, buffer[:n])

			select {
			case packetCh <- udpPacket{data: data, addr: addr}:
			case <-portListener.ctx.Done():
				return
			}
		}
	}()

	for {
		select {
		case <-portListener.ctx.Done():
			return
		case packet, ok := <-packetCh:
			if !ok {
				return
			}
			// Handle the UDP packet asynchronously
			pm.wg.Add(1)
			go func(data []byte, clientAddr net.Addr) {
				defer pm.wg.Done()
				pm.handleUDPPacket(portListener, data, clientAddr)
			}(packet.data, packet.addr)
		case err, ok := <-errCh:
			if !ok {
				return
			}
			// Check if the error is due to connection being closed (normal shutdown)
			if strings.Contains(err.Error(), "use of closed network connection") {
				logger.Debug("UDP port listener closed", "port", portListener.Port)
				return
			}
			logger.Error("Error reading UDP packet on forwarded port", "port", portListener.Port, "err", err)
			return
		}
	}
}

// handleUDPPacket handles single UDP packet
func (pm *PortForwardManager) handleUDPPacket(portListener *PortListener, data []byte, clientAddr net.Addr) {
	// Generate connection ID
	connID := utils.GenerateConnID()
	ctx := commonctx.WithConnID(context.Background(), connID)

	// Create target address
	targetAddr := net.JoinHostPort(portListener.LocalHost, strconv.Itoa(portListener.LocalPort))

	logger.Debug("New UDP packet to forwarded port", "port", portListener.Port, "client_id", portListener.ClientID, "conn_id", connID, "target", targetAddr, "client_addr", clientAddr, "data_size", len(data))

	// Connect to target (using client's dial function)
	targetConn, err := portListener.Client.dialNetwork(ctx, protocol.ProtocolUDP, targetAddr)
	if err != nil {
		logger.Error("Failed to create UDP connection to target through client tunnel", "port", portListener.Port, "client_id", portListener.ClientID, "conn_id", connID, "target", targetAddr, "err", err)
		return
	}
	defer func() {
		if err := targetConn.Close(); err != nil {
			logger.Warn("Error closing UDP target connection", "err", err)
		}
	}()

	// Send data to target
	_, err = targetConn.Write(data)
	if err != nil {
		logger.Error("Failed to send UDP data to target", "port", portListener.Port, "client_id", portListener.ClientID, "target", targetAddr, "err", err)
		return
	}

	// Create connection record for UDP port forwarding (outbound data)
	monitoring.CreateConnection(connID, portListener.ClientID, fmt.Sprintf("udp-port-forward:%d->%s", portListener.Port, targetAddr))
	monitoring.UpdateConnectionBytes(connID, portListener.ClientID, int64(len(data)), 0)

	// Fix: Handle UDP response asynchronously to avoid unnecessary waiting
	// Create a goroutine to wait for response, main function returns immediately
	go func() {
		// Use shorter timeout that is configurable
		timeout := 1 * time.Second
		if err := targetConn.SetReadDeadline(time.Now().Add(timeout)); err != nil {
			logger.Warn("Failed to set read deadline for UDP response", "err", err)
		}

		// Read response from target
		responseBuffer := make([]byte, 65536)
		n, err := targetConn.Read(responseBuffer)
		if err != nil {
			// UDP often doesn't send responses, so this is not necessarily an error
			if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
				logger.Debug("UDP response timeout (expected for many UDP protocols)", "port", portListener.Port, "timeout", timeout)
			} else {
				logger.Warn("Error reading UDP response", "port", portListener.Port, "err", err)
			}
			return
		}

		// Send response back to client
		_, err = portListener.PacketConn.WriteTo(responseBuffer[:n], clientAddr)
		if err != nil {
			logger.Error("Failed to send UDP response to client", "port", portListener.Port, "client_addr", clientAddr, "err", err)
			return
		}

		// Update monitoring statistics for UDP port forwarding (inbound response data)
		monitoring.UpdateConnectionBytes(connID, portListener.ClientID, 0, int64(n))

		logger.Debug("UDP response forwarded successfully", "port", portListener.Port, "client_addr", clientAddr, "target", targetAddr, "response_size", n, "response_time", timeout)
	}()

	logger.Debug("UDP request forwarded", "port", portListener.Port, "client_addr", clientAddr, "target", targetAddr, "request_size", len(data))
}

// handleForwardedConnection handles forwarded connection
func (pm *PortForwardManager) handleForwardedConnection(portListener *PortListener, incomingConn net.Conn) {
	defer func() {
		if err := incomingConn.Close(); err != nil {
			logger.Warn("Error closing incoming connection", "err", err)
		}
	}()

	// Generate connection ID
	connID := utils.GenerateConnID()
	ctx := commonctx.WithConnID(context.Background(), connID)

	// Create target address
	targetAddr := net.JoinHostPort(portListener.LocalHost, strconv.Itoa(portListener.LocalPort))

	logger.Info("New port forwarding connection", "port", portListener.Port, "client_id", portListener.ClientID, "conn_id", connID, "target", targetAddr, "remote_addr", incomingConn.RemoteAddr())

	// Create connection record for port forwarding
	monitoring.CreateConnection(connID, portListener.ClientID, fmt.Sprintf("port-forward:%d->%s", portListener.Port, targetAddr))

	defer func() {
		// Close connection when port forwarding ends
		monitoring.CloseConnection(connID)
	}()

	// Connect to target (using client's dial function)
	clientConn, err := portListener.Client.dialNetwork(ctx, protocol.ProtocolTCP, targetAddr)
	if err != nil {
		logger.Error("Port forwarding connection failed", "port", portListener.Port, "client_id", portListener.ClientID, "conn_id", connID, "target", targetAddr, "remote_addr", incomingConn.RemoteAddr(), "err", err)
		return
	}

	logger.Info("Port forwarding connection established", "port", portListener.Port, "client_id", portListener.ClientID, "conn_id", connID, "target", targetAddr, "remote_addr", incomingConn.RemoteAddr())
	defer func() {
		if err := clientConn.Close(); err != nil {
			logger.Warn("Error closing client connection", "err", err)
		}
	}()

	// Create context for the connection with timeout
	ctx, cancel := context.WithTimeout(portListener.ctx, 30*time.Minute)
	defer cancel()

	// Start bidirectional data transfer
	pm.transferData(ctx, incomingConn, clientConn, portListener.Port, connID, portListener.ClientID)
}

// transferData handles bidirectional data transfer
func (pm *PortForwardManager) transferData(ctx context.Context, conn1, conn2 net.Conn, port int, connID, clientID string) {
	var wg sync.WaitGroup

	// Copy from conn1 to conn2
	wg.Add(1)
	go func() {
		defer wg.Done()
		pm.copyDataWithContext(ctx, conn1, conn2, "incoming->client", port, connID, clientID)
	}()

	// Copy from conn2 to conn1
	wg.Add(1)
	go func() {
		defer wg.Done()
		pm.copyDataWithContext(ctx, conn2, conn1, "client->incoming", port, connID, clientID)
	}()

	// Wait for completion or context cancellation
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		logger.Debug("Data transfer completed for port forwarding", "port", port)
	case <-ctx.Done():
		logger.Debug("Data transfer cancelled due to context", "port", port)
	}
}

// copyDataWithContext copies data between connections
func (pm *PortForwardManager) copyDataWithContext(ctx context.Context, dst, src net.Conn, direction string, port int, connID, clientID string) {
	buffer := make([]byte, 32*1024) // 32KB buffer to match other components
	totalBytes := int64(0)

	for {
		// Check context before each operation
		select {
		case <-ctx.Done():
			logger.Debug("Data copy cancelled by context", "direction", direction, "port", port, "transferred_bytes", totalBytes)
			return
		default:
		}

		// Set read timeout based on context
		if deadline, ok := ctx.Deadline(); ok {
			if err := src.SetReadDeadline(deadline); err != nil {
				logger.Warn("Failed to set read deadline", "err", err)
			}
		} else {
			if err := src.SetReadDeadline(time.Now().Add(30 * time.Second)); err != nil {
				logger.Warn("Failed to set read deadline", "err", err)
			}
		}

		n, err := src.Read(buffer)
		if n > 0 {
			totalBytes += int64(n)

			// Set write timeout based on context
			if deadline, ok := ctx.Deadline(); ok {
				if err := dst.SetWriteDeadline(deadline); err != nil {
					logger.Warn("Failed to set write deadline", "err", err)
				}
			} else {
				if err := dst.SetWriteDeadline(time.Now().Add(30 * time.Second)); err != nil {
					logger.Warn("Failed to set write deadline", "err", err)
				}
			}

			_, writeErr := dst.Write(buffer[:n])
			if writeErr != nil {
				logger.Error("Port forward write error", "direction", direction, "port", port, "err", writeErr, "transferred_bytes", totalBytes)
				return
			}

			// Update monitoring statistics for port forwarding traffic
			if strings.Contains(direction, "incoming->client") {
				// Data from external client to internal service (bytes received by the proxy)
				monitoring.UpdateConnectionBytes(connID, clientID, 0, int64(n))
			} else {
				// Data from internal service to external client (bytes sent by the proxy)
				monitoring.UpdateConnectionBytes(connID, clientID, int64(n), 0)
			}
		}

		if err != nil {
			if err != net.ErrClosed {
				logger.Debug("Port forward connection closed", "direction", direction, "port", port, "err", err, "transferred_bytes", totalBytes)
			}
			return
		}
	}
}

// CloseClientPorts closes all ports for client
func (pm *PortForwardManager) CloseClientPorts(clientID string) {
	pm.mutex.Lock()
	defer pm.mutex.Unlock()

	clientPortMap, exists := pm.clientPorts[clientID]
	if !exists {
		return
	}

	logger.Info("Closing all ports for client", "client_id", clientID, "port_count", len(clientPortMap))

	// Close all port listeners for this client
	for portKey, portListener := range clientPortMap {
		// Remove from port owners
		delete(pm.portOwners, portKey)

		// Cancel the port listener context - this will gracefully stop all operations
		portListener.cancel()

		logger.Info("Closed port forwarding", "client_id", clientID, "port_key", portKey.String())
	}

	// Remove the client from clientPorts
	delete(pm.clientPorts, clientID)
}

// Stop stops the port forward manager and cleans up all resources.
func (pm *PortForwardManager) Stop() {
	logger.Info("Stopping port forwarding manager")

	// Cancel the context to stop all port listeners
	pm.cancel()

	// Get count of active ports for logging
	pm.mutex.RLock()
	totalPorts := len(pm.portOwners)
	totalClients := len(pm.clientPorts)
	pm.mutex.RUnlock()

	logger.Debug("Waiting for all port forwarding operations to complete", "total_ports", totalPorts, "total_clients", totalClients)

	// Wait for all goroutines to finish
	done := make(chan struct{})
	go func() {
		pm.wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		logger.Debug("All port forwarding goroutines finished gracefully")
	case <-time.After(5 * time.Second):
		logger.Warn("Timeout waiting for port forwarding goroutines to finish")
	}

	// Clear all data structures
	pm.mutex.Lock()
	pm.clientPorts = make(map[string]map[PortKey]*PortListener)
	pm.portOwners = make(map[PortKey]string)
	pm.mutex.Unlock()

	logger.Info("Port forwarding manager stopped", "ports_closed", totalPorts, "clients_affected", totalClients)
}

// GetClientPorts gets client port list
func (pm *PortForwardManager) GetClientPorts(clientID string) []PortKey {
	pm.mutex.RLock()
	defer pm.mutex.RUnlock()

	clientPortMap, exists := pm.clientPorts[clientID]
	if !exists {
		logger.Debug("No ports found for client", "client_id", clientID)
		return nil
	}

	ports := make([]PortKey, 0, len(clientPortMap))
	for portKey := range clientPortMap {
		ports = append(ports, portKey)
	}

	logger.Debug("Retrieved client ports", "client_id", clientID, "port_count", len(ports), "ports", ports)

	return ports
}
