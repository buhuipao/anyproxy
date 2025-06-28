// Package message provides message handling interfaces and utilities for the anyproxy system.
// It defines the core message processing abstractions including MessageConnection and MessageHandler.
package message

import (
	"fmt"

	"github.com/buhuipao/anyproxy/pkg/common/protocol"
)

// Connection represents a connection that can handle binary messages
// (renamed from MessageConnection to avoid stuttering)
type Connection interface {
	// Write message (binary data)
	WriteMessage(data []byte) error
	// Read message
	ReadMessage() ([]byte, error)
	Close() error
}

// Handler defines the interface for handling binary protocol messages
// (renamed from MessageHandler to avoid stuttering)
type Handler interface {
	// Read next message
	ReadNextMessage() (map[string]interface{}, error)
	// Parse binary message
	ParseBinaryMessage(msgData []byte) (map[string]interface{}, error)
	// Send data message
	WriteDataMessage(connID string, data []byte) error
	// Send close message
	WriteCloseMessage(connID string) error
}

// BinaryMessageHandler common implementation of binary message handler
type BinaryMessageHandler struct {
	conn     Connection
	isClient bool // Used to distinguish between client and gateway message types
}

// NewClientMessageHandler creates client message handler
func NewClientMessageHandler(conn Connection) Handler {
	return &BinaryMessageHandler{
		conn:     conn,
		isClient: true,
	}
}

// NewGatewayMessageHandler creates gateway message handler
func NewGatewayMessageHandler(conn Connection) Handler {
	return &BinaryMessageHandler{
		conn:     conn,
		isClient: false,
	}
}

// ReadNextMessage reads the next message, using binary format completely
func (h *BinaryMessageHandler) ReadNextMessage() (map[string]interface{}, error) {
	// Read raw message data
	msgData, err := h.conn.ReadMessage()
	if err != nil {
		return nil, err
	}

	// Check if it's a binary protocol message
	if !protocol.IsBinaryMessage(msgData) {
		return nil, fmt.Errorf("received non-binary message")
	}

	return h.ParseBinaryMessage(msgData)
}

// ParseBinaryMessage parses binary message to compatible map format
func (h *BinaryMessageHandler) ParseBinaryMessage(msgData []byte) (map[string]interface{}, error) {
	version, msgType, data, err := protocol.UnpackBinaryHeader(msgData)
	if err != nil {
		return nil, err
	}

	_ = version // Version not used for now

	// Client and gateway handle different message types
	if h.isClient {
		return h.parseClientMessage(msgType, data)
	}
	return h.parseGatewayMessage(msgType, data)
}

// parseClientMessage parses messages received by client
func (h *BinaryMessageHandler) parseClientMessage(msgType byte, data []byte) (map[string]interface{}, error) {
	switch msgType {
	case protocol.BinaryMsgTypeData:
		// Data message
		connID, payload, err := protocol.UnpackDataMessage(data)
		if err != nil {
			return nil, err
		}

		return map[string]interface{}{
			"type":       protocol.MsgTypeData,
			"id":         connID,
			"data":       payload, // Use raw data directly
			"_optimized": true,
		}, nil

	case protocol.BinaryMsgTypeConnect:
		// Connection request
		connID, network, address, err := protocol.UnpackConnectMessage(data)
		if err != nil {
			return nil, err
		}

		return map[string]interface{}{
			"type":    protocol.MsgTypeConnect,
			"id":      connID,
			"network": network,
			"address": address,
		}, nil

	case protocol.BinaryMsgTypeClose:
		// Close message
		connID, err := protocol.UnpackCloseMessage(data)
		if err != nil {
			return nil, err
		}

		return map[string]interface{}{
			"type": protocol.MsgTypeClose,
			"id":   connID,
		}, nil

	case protocol.BinaryMsgTypePortForwardResp:
		// Port forward response
		success, errorMsg, statuses, err := protocol.UnpackPortForwardResponseMessage(data)
		if err != nil {
			return nil, err
		}

		// Convert status list to compatible format
		var statusMap = make(map[int]bool)
		for _, status := range statuses {
			statusMap[status.Port] = status.Success
		}

		return map[string]interface{}{
			"type":    "port_forward_response",
			"success": success,
			"error":   errorMsg,
			"ports":   statusMap,
		}, nil

	case protocol.BinaryMsgTypeError:
		// Error message
		errorMsg, err := protocol.UnpackErrorMessage(data)
		if err != nil {
			return nil, err
		}

		return map[string]interface{}{
			"type":          protocol.MsgTypeError,
			"error_message": errorMsg,
		}, nil

	default:
		return nil, fmt.Errorf("unknown binary message type for client: 0x%02x", msgType)
	}
}

// parseGatewayMessage parses messages received by gateway
func (h *BinaryMessageHandler) parseGatewayMessage(msgType byte, data []byte) (map[string]interface{}, error) {
	switch msgType {
	case protocol.BinaryMsgTypeData:
		// Data message
		connID, payload, err := protocol.UnpackDataMessage(data)
		if err != nil {
			return nil, err
		}

		return map[string]interface{}{
			"type":       protocol.MsgTypeData,
			"id":         connID,
			"data":       payload, // Use raw data directly
			"_optimized": true,
		}, nil

	case protocol.BinaryMsgTypeConnectResponse:
		// Connection response
		connID, success, errorMsg, err := protocol.UnpackConnectResponseMessage(data)
		if err != nil {
			return nil, err
		}

		return map[string]interface{}{
			"type":    protocol.MsgTypeConnectResponse,
			"id":      connID,
			"success": success,
			"error":   errorMsg,
		}, nil

	case protocol.BinaryMsgTypeClose:
		// Close message
		connID, err := protocol.UnpackCloseMessage(data)
		if err != nil {
			return nil, err
		}

		return map[string]interface{}{
			"type": protocol.MsgTypeClose,
			"id":   connID,
		}, nil

	case protocol.BinaryMsgTypePortForward:
		// Port forward request
		clientID, ports, err := protocol.UnpackPortForwardMessage(data)
		if err != nil {
			return nil, err
		}

		// Convert to compatible format
		openPorts := make([]interface{}, len(ports))
		for i, port := range ports {
			openPorts[i] = map[string]interface{}{
				"remote_port": port.RemotePort,
				"local_port":  port.LocalPort,
				"local_host":  port.LocalHost,
				"protocol":    port.Protocol,
			}
		}

		return map[string]interface{}{
			"type":       protocol.MsgTypePortForwardReq,
			"client_id":  clientID,
			"open_ports": openPorts,
		}, nil

	case protocol.BinaryMsgTypeError:
		// Error message
		errorMsg, err := protocol.UnpackErrorMessage(data)
		if err != nil {
			return nil, err
		}

		return map[string]interface{}{
			"type":          protocol.MsgTypeError,
			"error_message": errorMsg,
		}, nil

	default:
		return nil, fmt.Errorf("unknown binary message type for gateway: 0x%02x", msgType)
	}
}

// WriteDataMessage sends data message using binary format
func (h *BinaryMessageHandler) WriteDataMessage(connID string, data []byte) error {
	// Use binary format
	binaryMsg := protocol.PackDataMessage(connID, data)

	return h.conn.WriteMessage(binaryMsg)
}

// WriteCloseMessage sends close message using binary format
func (h *BinaryMessageHandler) WriteCloseMessage(connID string) error {
	// Use binary format
	binaryMsg := protocol.PackCloseMessage(connID)

	return h.conn.WriteMessage(binaryMsg)
}

// ExtendedMessageHandler extended message handler interface (for endpoint-specific additional functionality)
type ExtendedMessageHandler interface {
	Handler
	// Client-specific methods
	WriteConnectResponse(connID string, success bool, errorMsg string) error
	// Gateway-specific methods
	WriteConnectMessage(connID, network, address string) error
	// Common methods
	WriteErrorMessage(errorMsg string) error
}

// ExtendedBinaryMessageHandler extended binary message handler
type ExtendedBinaryMessageHandler struct {
	*BinaryMessageHandler
}

// NewClientExtendedMessageHandler creates client extended message handler
func NewClientExtendedMessageHandler(conn Connection) ExtendedMessageHandler {
	return &ExtendedBinaryMessageHandler{
		BinaryMessageHandler: &BinaryMessageHandler{
			conn:     conn,
			isClient: true,
		},
	}
}

// NewGatewayExtendedMessageHandler creates gateway extended message handler
func NewGatewayExtendedMessageHandler(conn Connection) ExtendedMessageHandler {
	return &ExtendedBinaryMessageHandler{
		BinaryMessageHandler: &BinaryMessageHandler{
			conn:     conn,
			isClient: false,
		},
	}
}

// WriteConnectResponse sends connection response using binary format (used by client)
func (h *ExtendedBinaryMessageHandler) WriteConnectResponse(connID string, success bool, errorMsg string) error {
	// Use binary format
	binaryMsg := protocol.PackConnectResponseMessage(connID, success, errorMsg)

	return h.conn.WriteMessage(binaryMsg)
}

// WriteConnectMessage sends connection request using binary format (used by gateway)
func (h *ExtendedBinaryMessageHandler) WriteConnectMessage(connID, network, address string) error {
	// Use binary format
	binaryMsg := protocol.PackConnectMessage(connID, network, address)

	return h.conn.WriteMessage(binaryMsg)
}

// WriteErrorMessage sends error message using binary format (used by both client and gateway)
func (h *ExtendedBinaryMessageHandler) WriteErrorMessage(errorMsg string) error {
	// Use binary format
	binaryMsg := protocol.PackErrorMessage(errorMsg)

	return h.conn.WriteMessage(binaryMsg)
}
