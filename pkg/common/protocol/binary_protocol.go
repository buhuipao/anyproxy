// Package protocol defines the binary protocol specifications and utilities for the anyproxy system.
// It provides message packing/unpacking, protocol constants, and communication protocol implementations.
package protocol

import (
	"encoding/binary"
	"fmt"
)

// Binary protocol version
const (
	BinaryProtocolVersion byte = 1
)

// Binary message type definitions
const (
	// Control message types (0x00 - 0x0F)
	BinaryMsgTypeConnect         byte = 0x01 // Connection request
	BinaryMsgTypeConnectResponse byte = 0x02 // Connection response
	BinaryMsgTypeClose           byte = 0x03 // Close connection
	BinaryMsgTypePortForward     byte = 0x04 // Port forwarding request
	BinaryMsgTypePortForwardResp byte = 0x05 // Port forwarding response

	// Authentication message types (0x06 - 0x0F)
	BinaryMsgTypeAuth         byte = 0x06 // Authentication request
	BinaryMsgTypeAuthResponse byte = 0x07 // Authentication response
	BinaryMsgTypeError        byte = 0x08 // Error message
	BinaryMsgTypeHeartbeat    byte = 0x09 // Heartbeat message (client -> gateway)
	BinaryMsgTypeHeartbeatAck byte = 0x0A // Heartbeat acknowledgment (gateway -> client)

	// Data message types (0x10 - 0x1F)
	BinaryMsgTypeData    byte = 0x10 // Data transfer (TCP stream)
	BinaryMsgTypeUDPData byte = 0x11 // UDP data transfer

	// Reserved types (0x20 - 0xFF)
)

// Message header sizes
const (
	BinaryHeaderSize = 2  // [version:1byte][type:1byte]
	ConnIDSize       = 20 // xid string length
)

// BinaryMessage binary message base structure
type BinaryMessage struct {
	Version byte   // Protocol version
	Type    byte   // Message type
	ConnID  string // Connection ID (20 characters)
	Data    []byte // Message data
}

// PackBinaryMessage packs any type of binary message
// Format: [version:1][type:1][data:N]
// Data part has different formats based on message type
func PackBinaryMessage(msgType byte, data []byte) []byte {
	msg := make([]byte, BinaryHeaderSize+len(data))
	msg[0] = BinaryProtocolVersion
	msg[1] = msgType
	copy(msg[2:], data)
	return msg
}

// UnpackBinaryHeader unpacks message header, returns version, type and data part
func UnpackBinaryHeader(msg []byte) (version, msgType byte, data []byte, err error) {
	if len(msg) < BinaryHeaderSize {
		return 0, 0, nil, fmt.Errorf("message too short: %d bytes", len(msg))
	}

	version = msg[0]
	msgType = msg[1]
	data = msg[2:]

	if version != BinaryProtocolVersion {
		return 0, 0, nil, fmt.Errorf("unsupported version: %d", version)
	}

	return version, msgType, data, nil
}

// IsBinaryMessage checks if this is a binary protocol message
func IsBinaryMessage(data []byte) bool {
	if len(data) < BinaryHeaderSize {
		return false
	}
	return data[0] == BinaryProtocolVersion
}

// --- Data messages (highest frequency) ---
// Format: [version:1][type:1][connID:20][data:N]

// PackDataMessage packs data message
func PackDataMessage(connID string, data []byte) []byte {
	if len(connID) > ConnIDSize {
		connID = connID[:ConnIDSize]
	}

	payload := make([]byte, ConnIDSize+len(data))

	// Copy connID, pad with zeros if insufficient
	connIDBytes := []byte(connID)
	copy(payload[:ConnIDSize], connIDBytes)

	// Copy data
	copy(payload[ConnIDSize:], data)

	return PackBinaryMessage(BinaryMsgTypeData, payload)
}

// UnpackDataMessage unpacks data message
func UnpackDataMessage(data []byte) (connID string, payload []byte, err error) {
	if len(data) < ConnIDSize {
		return "", nil, fmt.Errorf("data message too short: %d bytes", len(data))
	}

	// Extract connID (remove trailing zero bytes)
	connIDBytes := data[:ConnIDSize]
	for i, b := range connIDBytes {
		if b == 0 {
			connID = string(connIDBytes[:i])
			break
		}
	}
	if connID == "" {
		connID = string(connIDBytes)
	}

	// 🔧 CRITICAL FIX: Copy payload to avoid buffer reuse issues
	// data[ConnIDSize:] is a slice reference to the original buffer
	// which may be reused by the QUIC layer, causing data corruption
	payload = make([]byte, len(data)-ConnIDSize)
	copy(payload, data[ConnIDSize:])
	return connID, payload, nil
}

// --- Connection request messages ---
// Format: [version:1][type:1][connID:20][network_length:2][network:N][address_length:2][address:N]

// PackConnectMessage packs connection request
func PackConnectMessage(connID, network, address string) []byte {
	if len(connID) > ConnIDSize {
		connID = connID[:ConnIDSize]
	}

	networkBytes := []byte(network)
	addressBytes := []byte(address)

	// Calculate total length
	totalLen := ConnIDSize + 2 + len(networkBytes) + 2 + len(addressBytes)
	payload := make([]byte, totalLen)

	offset := 0

	// connID (fixed 20 bytes)
	copy(payload[offset:offset+ConnIDSize], []byte(connID))
	offset += ConnIDSize

	// network length (2 bytes)
	binary.BigEndian.PutUint16(payload[offset:], uint16(len(networkBytes))) //nolint:gosec // network is always short
	offset += 2

	// network content
	copy(payload[offset:], networkBytes)
	offset += len(networkBytes)

	// address length (2 bytes)
	binary.BigEndian.PutUint16(payload[offset:], uint16(len(addressBytes))) //nolint:gosec // address is always short
	offset += 2

	// address content
	copy(payload[offset:], addressBytes)

	return PackBinaryMessage(BinaryMsgTypeConnect, payload)
}

// UnpackConnectMessage unpacks connection request
func UnpackConnectMessage(data []byte) (connID, network, address string, err error) {
	if len(data) < ConnIDSize+4 {
		return "", "", "", fmt.Errorf("connect message too short: %d bytes", len(data))
	}

	offset := 0

	// Extract connID
	connIDBytes := data[offset : offset+ConnIDSize]
	for i, b := range connIDBytes {
		if b == 0 {
			connID = string(connIDBytes[:i])
			break
		}
	}
	if connID == "" {
		connID = string(connIDBytes)
	}
	offset += ConnIDSize

	// Extract network
	networkLen := binary.BigEndian.Uint16(data[offset:])
	offset += 2
	if offset+int(networkLen) > len(data) {
		return "", "", "", fmt.Errorf("invalid network length")
	}
	network = string(data[offset : offset+int(networkLen)])
	offset += int(networkLen)

	// Extract address
	if offset+2 > len(data) {
		return "", "", "", fmt.Errorf("missing address length")
	}
	addressLen := binary.BigEndian.Uint16(data[offset:])
	offset += 2
	if offset+int(addressLen) > len(data) {
		return "", "", "", fmt.Errorf("invalid address length")
	}
	address = string(data[offset : offset+int(addressLen)])

	return connID, network, address, nil
}

// --- Connection response messages ---
// Format: [version:1][type:1][connID:20][success:1][error_length:2][error:N]

// PackConnectResponseMessage packs connection response
func PackConnectResponseMessage(connID string, success bool, errorMsg string) []byte {
	if len(connID) > ConnIDSize {
		connID = connID[:ConnIDSize]
	}

	errorBytes := []byte(errorMsg)

	// Calculate total length
	totalLen := ConnIDSize + 1 + 2 + len(errorBytes)
	payload := make([]byte, totalLen)

	offset := 0

	// connID (fixed 20 bytes)
	copy(payload[offset:offset+ConnIDSize], []byte(connID))
	offset += ConnIDSize

	// success (1 byte)
	if success {
		payload[offset] = 1
	} else {
		payload[offset] = 0
	}
	offset++

	// error length (2 bytes)
	binary.BigEndian.PutUint16(payload[offset:], uint16(len(errorBytes))) //nolint:gosec // error msg is always short
	offset += 2

	// error content
	copy(payload[offset:], errorBytes)

	return PackBinaryMessage(BinaryMsgTypeConnectResponse, payload)
}

// UnpackConnectResponseMessage unpacks connection response
func UnpackConnectResponseMessage(data []byte) (connID string, success bool, errorMsg string, err error) {
	if len(data) < ConnIDSize+3 {
		return "", false, "", fmt.Errorf("connect response too short: %d bytes", len(data))
	}

	offset := 0

	// Extract connID
	connIDBytes := data[offset : offset+ConnIDSize]
	for i, b := range connIDBytes {
		if b == 0 {
			connID = string(connIDBytes[:i])
			break
		}
	}
	if connID == "" {
		connID = string(connIDBytes)
	}
	offset += ConnIDSize

	// Extract success
	success = data[offset] == 1
	offset++

	// Extract error
	errorLen := binary.BigEndian.Uint16(data[offset:])
	offset += 2
	if errorLen > 0 {
		if offset+int(errorLen) > len(data) {
			return "", false, "", fmt.Errorf("invalid error length")
		}
		errorMsg = string(data[offset : offset+int(errorLen)])
	}

	return connID, success, errorMsg, nil
}

// --- Close messages ---
// Format: [version:1][type:1][connID:20]

// PackCloseMessage packs close message
func PackCloseMessage(connID string) []byte {
	if len(connID) > ConnIDSize {
		connID = connID[:ConnIDSize]
	}

	payload := make([]byte, ConnIDSize)
	copy(payload, []byte(connID))

	return PackBinaryMessage(BinaryMsgTypeClose, payload)
}

// UnpackCloseMessage unpacks close message
func UnpackCloseMessage(data []byte) (connID string, err error) {
	if len(data) < ConnIDSize {
		return "", fmt.Errorf("close message too short: %d bytes", len(data))
	}

	// Extract connID
	connIDBytes := data[:ConnIDSize]
	for i, b := range connIDBytes {
		if b == 0 {
			connID = string(connIDBytes[:i])
			break
		}
	}
	if connID == "" {
		connID = string(connIDBytes)
	}

	return connID, nil
}

// --- Port forwarding request ---
// Format: [version:1][type:1][clientID_length:2][clientID:N][port_count:2][port_config1][port_config2]...
// Port config format: [remotePort:2][localPort:2][localHost_length:2][localHost:N][protocol_length:1][protocol:N]

// PortConfig port forwarding configuration
type PortConfig struct {
	RemotePort int
	LocalPort  int
	LocalHost  string
	Protocol   string
}

// PackPortForwardMessage packs port forwarding request
func PackPortForwardMessage(clientID string, ports []PortConfig) []byte {
	clientIDBytes := []byte(clientID)

	// Calculate total length
	totalLen := 2 + len(clientIDBytes) + 2 // clientID length + clientID + port count
	for _, port := range ports {
		totalLen += 2 + 2 + 2 + len(port.LocalHost) + 1 + len(port.Protocol)
	}

	payload := make([]byte, totalLen)

	offset := 0

	// clientID length (2 bytes)
	binary.BigEndian.PutUint16(payload[offset:], uint16(len(clientIDBytes))) //nolint:gosec // clientID is always short
	offset += 2

	// clientID content
	copy(payload[offset:], clientIDBytes)
	offset += len(clientIDBytes)

	// port count (2 bytes)
	binary.BigEndian.PutUint16(payload[offset:], uint16(len(ports))) //nolint:gosec // port count is limited
	offset += 2

	// port configuration list
	for _, port := range ports {
		// remotePort (2 bytes)
		binary.BigEndian.PutUint16(payload[offset:], uint16(port.RemotePort)) //nolint:gosec // port is always valid
		offset += 2

		// localPort (2 bytes)
		binary.BigEndian.PutUint16(payload[offset:], uint16(port.LocalPort)) //nolint:gosec // port is always valid
		offset += 2

		// localHost length (2 bytes)
		localHostBytes := []byte(port.LocalHost)
		binary.BigEndian.PutUint16(payload[offset:], uint16(len(localHostBytes))) //nolint:gosec // host is always short
		offset += 2

		// localHost content
		copy(payload[offset:], localHostBytes)
		offset += len(localHostBytes)

		// protocol length (1 byte)
		protocolBytes := []byte(port.Protocol)
		payload[offset] = byte(len(protocolBytes)) //nolint:gosec // protocol is always short
		offset++

		// protocol content
		copy(payload[offset:], protocolBytes)
		offset += len(protocolBytes)
	}

	return PackBinaryMessage(BinaryMsgTypePortForward, payload)
}

// UnpackPortForwardMessage unpacks port forwarding request
func UnpackPortForwardMessage(data []byte) (clientID string, ports []PortConfig, err error) {
	if len(data) < 4 {
		return "", nil, fmt.Errorf("port forward message too short: %d bytes", len(data))
	}

	offset := 0

	// Extract clientID
	clientIDLen := binary.BigEndian.Uint16(data[offset:])
	offset += 2
	if offset+int(clientIDLen) > len(data) {
		return "", nil, fmt.Errorf("invalid clientID length")
	}
	clientID = string(data[offset : offset+int(clientIDLen)])
	offset += int(clientIDLen)

	// Extract port count
	if offset+2 > len(data) {
		return "", nil, fmt.Errorf("missing port count")
	}
	portCount := binary.BigEndian.Uint16(data[offset:])
	offset += 2

	// Extract port configuration list
	ports = make([]PortConfig, portCount)
	for i := 0; i < int(portCount); i++ {
		// remotePort
		if offset+2 > len(data) {
			return "", nil, fmt.Errorf("missing remote port")
		}
		ports[i].RemotePort = int(binary.BigEndian.Uint16(data[offset:]))
		offset += 2

		// localPort
		if offset+2 > len(data) {
			return "", nil, fmt.Errorf("missing local port")
		}
		ports[i].LocalPort = int(binary.BigEndian.Uint16(data[offset:]))
		offset += 2

		// localHost
		if offset+2 > len(data) {
			return "", nil, fmt.Errorf("missing local host length")
		}
		localHostLen := binary.BigEndian.Uint16(data[offset:])
		offset += 2
		if offset+int(localHostLen) > len(data) {
			return "", nil, fmt.Errorf("invalid local host length")
		}
		ports[i].LocalHost = string(data[offset : offset+int(localHostLen)])
		offset += int(localHostLen)

		// protocol
		if offset+1 > len(data) {
			return "", nil, fmt.Errorf("missing protocol length")
		}
		protocolLen := data[offset]
		offset++
		if offset+int(protocolLen) > len(data) {
			return "", nil, fmt.Errorf("invalid protocol length")
		}
		ports[i].Protocol = string(data[offset : offset+int(protocolLen)])
		offset += int(protocolLen)
	}

	return clientID, ports, nil
}

// --- Port forwarding response ---
// Format: [version:1][type:1][success:1][error_length:2][error:N][forward_count:2][port1:2][status1:1]...

// PortForwardStatus port forwarding status
type PortForwardStatus struct {
	Port    int
	Success bool
}

// PackPortForwardResponseMessage packs port forwarding response
func PackPortForwardResponseMessage(success bool, errorMsg string, statuses []PortForwardStatus) []byte {
	errorBytes := []byte(errorMsg)

	// Calculate total length
	totalLen := 1 + 2 + len(errorBytes) + 2 + len(statuses)*3
	payload := make([]byte, totalLen)

	offset := 0

	// success (1 byte)
	if success {
		payload[offset] = 1
	} else {
		payload[offset] = 0
	}
	offset++

	// error length (2 bytes)
	binary.BigEndian.PutUint16(payload[offset:], uint16(len(errorBytes))) //nolint:gosec // error msg is always short
	offset += 2

	// error content
	copy(payload[offset:], errorBytes)
	offset += len(errorBytes)

	// forward count (2 bytes)
	binary.BigEndian.PutUint16(payload[offset:], uint16(len(statuses))) //nolint:gosec // status count is limited
	offset += 2

	// status list
	for _, status := range statuses {
		binary.BigEndian.PutUint16(payload[offset:], uint16(status.Port)) //nolint:gosec // port is always valid
		offset += 2
		if status.Success {
			payload[offset] = 1
		} else {
			payload[offset] = 0
		}
		offset++
	}

	return PackBinaryMessage(BinaryMsgTypePortForwardResp, payload)
}

// UnpackPortForwardResponseMessage unpacks port forwarding response
func UnpackPortForwardResponseMessage(data []byte) (success bool, errorMsg string, statuses []PortForwardStatus, err error) {
	if len(data) < 5 {
		return false, "", nil, fmt.Errorf("port forward response too short: %d bytes", len(data))
	}

	offset := 0

	// Extract success
	success = data[offset] == 1
	offset++

	// Extract error
	errorLen := binary.BigEndian.Uint16(data[offset:])
	offset += 2
	if errorLen > 0 {
		if offset+int(errorLen) > len(data) {
			return false, "", nil, fmt.Errorf("invalid error length")
		}
		errorMsg = string(data[offset : offset+int(errorLen)])
		offset += int(errorLen)
	}

	// Extract status count
	if offset+2 > len(data) {
		return false, "", nil, fmt.Errorf("missing status count")
	}
	statusCount := binary.BigEndian.Uint16(data[offset:])
	offset += 2

	// Extract status list
	statuses = make([]PortForwardStatus, statusCount)
	for i := 0; i < int(statusCount); i++ {
		if offset+3 > len(data) {
			return false, "", nil, fmt.Errorf("invalid status data")
		}
		statuses[i].Port = int(binary.BigEndian.Uint16(data[offset:]))
		offset += 2
		statuses[i].Success = data[offset] == 1
		offset++
	}

	return success, errorMsg, statuses, nil
}

// --- Authentication request messages ---
// Format: [version:1][type:1][clientID_length:2][clientID:N][groupID_length:2][groupID:N][username_length:2][username:N][password_length:2][password:N][groupPassword_length:2][groupPassword:N]

// PackAuthMessage packs authentication request
func PackAuthMessage(clientID, groupID, username, password, groupPassword string) []byte {
	clientIDBytes := []byte(clientID)
	groupIDBytes := []byte(groupID)
	usernameBytes := []byte(username)
	passwordBytes := []byte(password)
	groupPasswordBytes := []byte(groupPassword)

	// Calculate total length
	totalLen := 2 + len(clientIDBytes) + 2 + len(groupIDBytes) + 2 + len(usernameBytes) + 2 + len(passwordBytes) + 2 + len(groupPasswordBytes)
	payload := make([]byte, totalLen)

	offset := 0

	// clientID length (2 bytes)
	binary.BigEndian.PutUint16(payload[offset:], uint16(len(clientIDBytes))) //nolint:gosec // clientID is always short
	offset += 2

	// clientID content
	copy(payload[offset:], clientIDBytes)
	offset += len(clientIDBytes)

	// groupID length (2 bytes)
	binary.BigEndian.PutUint16(payload[offset:], uint16(len(groupIDBytes))) //nolint:gosec // groupID is always short
	offset += 2

	// groupID content
	copy(payload[offset:], groupIDBytes)
	offset += len(groupIDBytes)

	// username length (2 bytes)
	binary.BigEndian.PutUint16(payload[offset:], uint16(len(usernameBytes))) //nolint:gosec // username is always short
	offset += 2

	// username content
	copy(payload[offset:], usernameBytes)
	offset += len(usernameBytes)

	// password length (2 bytes)
	binary.BigEndian.PutUint16(payload[offset:], uint16(len(passwordBytes))) //nolint:gosec // password is always short
	offset += 2

	// password content
	copy(payload[offset:], passwordBytes)
	offset += len(passwordBytes)

	// groupPassword length (2 bytes)
	binary.BigEndian.PutUint16(payload[offset:], uint16(len(groupPasswordBytes))) //nolint:gosec // groupPassword is always short
	offset += 2

	// groupPassword content
	copy(payload[offset:], groupPasswordBytes)

	return PackBinaryMessage(BinaryMsgTypeAuth, payload)
}

// UnpackAuthMessage unpacks authentication request
func UnpackAuthMessage(data []byte) (clientID, groupID, username, password, groupPassword string, err error) {
	if len(data) < 10 {
		return "", "", "", "", "", fmt.Errorf("auth message too short: %d bytes", len(data))
	}

	offset := 0

	// Extract clientID
	clientIDLen := binary.BigEndian.Uint16(data[offset:])
	offset += 2
	if offset+int(clientIDLen) > len(data) {
		return "", "", "", "", "", fmt.Errorf("invalid clientID length")
	}
	clientID = string(data[offset : offset+int(clientIDLen)])
	offset += int(clientIDLen)

	// Extract groupID
	if offset+2 > len(data) {
		return "", "", "", "", "", fmt.Errorf("missing groupID length")
	}
	groupIDLen := binary.BigEndian.Uint16(data[offset:])
	offset += 2
	if offset+int(groupIDLen) > len(data) {
		return "", "", "", "", "", fmt.Errorf("invalid groupID length")
	}
	groupID = string(data[offset : offset+int(groupIDLen)])
	offset += int(groupIDLen)

	// Extract username
	if offset+2 > len(data) {
		return "", "", "", "", "", fmt.Errorf("missing username length")
	}
	usernameLen := binary.BigEndian.Uint16(data[offset:])
	offset += 2
	if offset+int(usernameLen) > len(data) {
		return "", "", "", "", "", fmt.Errorf("invalid username length")
	}
	username = string(data[offset : offset+int(usernameLen)])
	offset += int(usernameLen)

	// Extract password
	if offset+2 > len(data) {
		return "", "", "", "", "", fmt.Errorf("missing password length")
	}
	passwordLen := binary.BigEndian.Uint16(data[offset:])
	offset += 2
	if offset+int(passwordLen) > len(data) {
		return "", "", "", "", "", fmt.Errorf("invalid password length")
	}
	password = string(data[offset : offset+int(passwordLen)])
	offset += int(passwordLen)

	// Extract groupPassword
	if offset+2 > len(data) {
		return "", "", "", "", "", fmt.Errorf("missing groupPassword length")
	}
	groupPasswordLen := binary.BigEndian.Uint16(data[offset:])
	offset += 2
	if offset+int(groupPasswordLen) > len(data) {
		return "", "", "", "", "", fmt.Errorf("invalid groupPassword length")
	}
	groupPassword = string(data[offset : offset+int(groupPasswordLen)])

	return clientID, groupID, username, password, groupPassword, nil
}

// --- Authentication response messages ---
// Format: [version:1][type:1][status_length:2][status:N][reason_length:2][reason:N]

// PackAuthResponseMessage packs authentication response
func PackAuthResponseMessage(status, reason string) []byte {
	statusBytes := []byte(status)
	reasonBytes := []byte(reason)

	// Calculate total length
	totalLen := 2 + len(statusBytes) + 2 + len(reasonBytes)
	payload := make([]byte, totalLen)

	offset := 0

	// status length (2 bytes)
	binary.BigEndian.PutUint16(payload[offset:], uint16(len(statusBytes))) //nolint:gosec // status is always short
	offset += 2

	// status content
	copy(payload[offset:], statusBytes)
	offset += len(statusBytes)

	// reason length (2 bytes)
	binary.BigEndian.PutUint16(payload[offset:], uint16(len(reasonBytes))) //nolint:gosec // reason is always short
	offset += 2

	// reason content
	copy(payload[offset:], reasonBytes)

	return PackBinaryMessage(BinaryMsgTypeAuthResponse, payload)
}

// UnpackAuthResponseMessage unpacks authentication response
func UnpackAuthResponseMessage(data []byte) (status, reason string, err error) {
	if len(data) < 4 {
		return "", "", fmt.Errorf("auth response too short: %d bytes", len(data))
	}

	offset := 0

	// Extract status
	statusLen := binary.BigEndian.Uint16(data[offset:])
	offset += 2
	if offset+int(statusLen) > len(data) {
		return "", "", fmt.Errorf("invalid status length")
	}
	status = string(data[offset : offset+int(statusLen)])
	offset += int(statusLen)

	// Extract reason
	if offset+2 > len(data) {
		return "", "", fmt.Errorf("missing reason length")
	}
	reasonLen := binary.BigEndian.Uint16(data[offset:])
	offset += 2
	if offset+int(reasonLen) > len(data) {
		return "", "", fmt.Errorf("invalid reason length")
	}
	reason = string(data[offset : offset+int(reasonLen)])

	return status, reason, nil
}

// --- Error messages ---
// Format: [version:1][type:1][error_message_length:2][error_message:N]

// PackErrorMessage packs error message
func PackErrorMessage(errorMsg string) []byte {
	errorBytes := []byte(errorMsg)

	// Calculate total length
	totalLen := 2 + len(errorBytes)
	payload := make([]byte, totalLen)

	offset := 0

	// error message length (2 bytes)
	binary.BigEndian.PutUint16(payload[offset:], uint16(len(errorBytes))) //nolint:gosec // error msg is always short
	offset += 2

	// error message content
	copy(payload[offset:], errorBytes)

	return PackBinaryMessage(BinaryMsgTypeError, payload)
}

// UnpackErrorMessage unpacks error message
func UnpackErrorMessage(data []byte) (errorMsg string, err error) {
	if len(data) < 2 {
		return "", fmt.Errorf("error message too short: %d bytes", len(data))
	}

	offset := 0

	// Extract error message
	errorLen := binary.BigEndian.Uint16(data[offset:])
	offset += 2
	if offset+int(errorLen) > len(data) {
		return "", fmt.Errorf("invalid error message length")
	}
	errorMsg = string(data[offset : offset+int(errorLen)])

	return errorMsg, nil
}

// --- UDP Data messages ---
// Format: [version:1][type:1][connID:20][addrLen:2][targetAddr:N][data:M]
// This preserves UDP datagram boundaries by including the target address with each datagram

// PackUDPDataMessage packs UDP data message
func PackUDPDataMessage(connID, targetAddr string, data []byte) []byte {
	if len(connID) > ConnIDSize {
		connID = connID[:ConnIDSize]
	}

	targetAddrBytes := []byte(targetAddr)

	// Calculate total payload length: connID + addrLen + targetAddr + data
	totalLen := ConnIDSize + 2 + len(targetAddrBytes) + len(data)
	payload := make([]byte, totalLen)

	offset := 0

	// Copy connID (fixed 20 bytes)
	connIDBytes := []byte(connID)
	copy(payload[offset:offset+ConnIDSize], connIDBytes)
	offset += ConnIDSize

	// Target address length (2 bytes)
	binary.BigEndian.PutUint16(payload[offset:], uint16(len(targetAddrBytes))) //nolint:gosec // address is always short
	offset += 2

	// Target address content
	copy(payload[offset:], targetAddrBytes)
	offset += len(targetAddrBytes)

	// UDP data
	copy(payload[offset:], data)

	return PackBinaryMessage(BinaryMsgTypeUDPData, payload)
}

// BuildUDPDataHeader builds the UDP data message header without the payload
// Returns the header bytes that should be prepended to the data
func BuildUDPDataHeader(connID, targetAddr string) []byte {
	if len(connID) > ConnIDSize {
		connID = connID[:ConnIDSize]
	}

	targetAddrBytes := []byte(targetAddr)

	// Header size: version(1) + type(1) + connID(20) + addrLen(2) + targetAddr(N)
	headerLen := BinaryHeaderSize + ConnIDSize + 2 + len(targetAddrBytes)
	header := make([]byte, headerLen)

	offset := 0

	// Protocol header
	header[offset] = BinaryProtocolVersion
	offset++
	header[offset] = BinaryMsgTypeUDPData
	offset++

	// ConnID (fixed 20 bytes, pad with zeros)
	copy(header[offset:offset+ConnIDSize], []byte(connID))
	offset += ConnIDSize

	// Target address length (2 bytes)
	binary.BigEndian.PutUint16(header[offset:], uint16(len(targetAddrBytes))) //nolint:gosec // address is always short
	offset += 2

	// Target address content
	copy(header[offset:], targetAddrBytes)

	return header
}

// UnpackUDPDataMessage unpacks UDP data message
func UnpackUDPDataMessage(data []byte) (connID, targetAddr string, payload []byte, err error) {
	if len(data) < ConnIDSize+2 {
		return "", "", nil, fmt.Errorf("UDP data message too short: %d bytes", len(data))
	}

	offset := 0

	// Extract connID (remove trailing zero bytes)
	connIDBytes := data[offset : offset+ConnIDSize]
	for i, b := range connIDBytes {
		if b == 0 {
			connID = string(connIDBytes[:i])
			break
		}
	}
	if connID == "" {
		connID = string(connIDBytes)
	}
	offset += ConnIDSize

	// Extract target address length
	addrLen := binary.BigEndian.Uint16(data[offset:])
	offset += 2

	// Validate address length
	if offset+int(addrLen) > len(data) {
		return "", "", nil, fmt.Errorf("invalid target address length: %d", addrLen)
	}

	// Extract target address
	targetAddr = string(data[offset : offset+int(addrLen)])
	offset += int(addrLen)

	// Remaining data is the UDP payload
	payload = make([]byte, len(data)-offset)
	copy(payload, data[offset:])

	return connID, targetAddr, payload, nil
}

// ============================================================================
// Zero-Copy StreamBuffer Support
// ============================================================================

// StreamBufferPrepender is an interface for buffers that support header prepending.
// This allows the protocol package to work with StreamBuffer without importing it.
type StreamBufferPrepender interface {
	PrependHeader(header []byte) bool
	AvailableHeaderSpace() int
}

// ErrHeaderTooLarge is returned when the header exceeds available space.
var ErrHeaderTooLarge = fmt.Errorf("header too large for available space")

// BuildDataHeader builds a TCP data message header.
// Format: [version:1][type:1][connID:20]
// Returns the header bytes to prepend.
func BuildDataHeader(connID string) []byte {
	header := make([]byte, BinaryHeaderSize+ConnIDSize)
	header[0] = BinaryProtocolVersion
	header[1] = BinaryMsgTypeData

	// Copy connID, pad with zeros if shorter
	connIDBytes := []byte(connID)
	if len(connIDBytes) > ConnIDSize {
		connIDBytes = connIDBytes[:ConnIDSize]
	}
	copy(header[BinaryHeaderSize:], connIDBytes)

	return header
}

// PrependDataHeader prepends a TCP data header to a StreamBuffer.
// This is a zero-copy operation that uses the buffer's reserved header space.
func PrependDataHeader(sb StreamBufferPrepender, connID string) error {
	header := BuildDataHeader(connID)
	if len(header) > sb.AvailableHeaderSpace() {
		return ErrHeaderTooLarge
	}
	if !sb.PrependHeader(header) {
		return ErrHeaderTooLarge
	}
	return nil
}

// PrependUDPDataHeaderToBuffer prepends a UDP data header to a StreamBuffer.
// This is a zero-copy operation that uses the buffer's reserved header space.
// Note: Uses BuildUDPDataHeader which is already defined above.
func PrependUDPDataHeaderToBuffer(sb StreamBufferPrepender, connID, targetAddr string) error {
	header := BuildUDPDataHeader(connID, targetAddr)
	if len(header) > sb.AvailableHeaderSpace() {
		return ErrHeaderTooLarge
	}
	if !sb.PrependHeader(header) {
		return ErrHeaderTooLarge
	}
	return nil
}

// DataHeaderSize returns the size of a TCP data header.
func DataHeaderSize() int {
	return BinaryHeaderSize + ConnIDSize
}

// UDPDataHeaderSize returns the size of a UDP data header for the given target address.
func UDPDataHeaderSize(targetAddr string) int {
	return BinaryHeaderSize + ConnIDSize + 2 + len(targetAddr)
}

// ==================== Heartbeat Messages ====================

// PackHeartbeatMessage packs a heartbeat message from client to gateway
// Format: [clientID_len:2bytes][clientID:variable][timestamp:8bytes]
func PackHeartbeatMessage(clientID string, timestamp int64) []byte {
	clientIDBytes := []byte(clientID)
	clientIDLen := len(clientIDBytes)

	// Calculate payload size: 2 (clientID len) + clientID + 8 (timestamp)
	payloadSize := 2 + clientIDLen + 8
	payload := make([]byte, payloadSize)

	offset := 0

	// Write clientID length (2 bytes)
	binary.BigEndian.PutUint16(payload[offset:], uint16(clientIDLen)) //nolint:gosec // clientID is always short
	offset += 2

	// Write clientID
	copy(payload[offset:], clientIDBytes)
	offset += clientIDLen

	// Write timestamp (8 bytes, big-endian)
	binary.BigEndian.PutUint64(payload[offset:], uint64(timestamp)) //nolint:gosec // same bits, read back as int64

	return PackBinaryMessage(BinaryMsgTypeHeartbeat, payload)
}

// UnpackHeartbeatMessage unpacks a heartbeat message
// Returns clientID and timestamp
func UnpackHeartbeatMessage(data []byte) (clientID string, timestamp int64, err error) {
	if len(data) < 10 { // Minimum: 2 (len) + 0 (empty clientID) + 8 (timestamp)
		return "", 0, fmt.Errorf("heartbeat message too short: %d bytes", len(data))
	}

	offset := 0

	// Read clientID length
	clientIDLen := int(data[offset])<<8 | int(data[offset+1])
	offset += 2

	if len(data) < offset+clientIDLen+8 {
		return "", 0, fmt.Errorf("heartbeat message incomplete: expected %d bytes, got %d", offset+clientIDLen+8, len(data))
	}

	// Read clientID
	clientID = string(data[offset : offset+clientIDLen])
	offset += clientIDLen

	// Read timestamp (big-endian)
	timestamp = int64(data[offset])<<56 | int64(data[offset+1])<<48 |
		int64(data[offset+2])<<40 | int64(data[offset+3])<<32 |
		int64(data[offset+4])<<24 | int64(data[offset+5])<<16 |
		int64(data[offset+6])<<8 | int64(data[offset+7])

	return clientID, timestamp, nil
}

// PackHeartbeatAckMessage packs a heartbeat acknowledgment message from gateway to client
// Format: [timestamp:8bytes]
func PackHeartbeatAckMessage(timestamp int64) []byte {
	payload := make([]byte, 8)

	// Write timestamp (8 bytes, big-endian)
	binary.BigEndian.PutUint64(payload, uint64(timestamp)) //nolint:gosec // same bits, read back as int64

	return PackBinaryMessage(BinaryMsgTypeHeartbeatAck, payload)
}

// UnpackHeartbeatAckMessage unpacks a heartbeat acknowledgment message
// Returns timestamp
func UnpackHeartbeatAckMessage(data []byte) (timestamp int64, err error) {
	if len(data) < 8 {
		return 0, fmt.Errorf("heartbeat ack message too short: %d bytes", len(data))
	}

	// Read timestamp (big-endian)
	timestamp = int64(data[0])<<56 | int64(data[1])<<48 |
		int64(data[2])<<40 | int64(data[3])<<32 |
		int64(data[4])<<24 | int64(data[5])<<16 |
		int64(data[6])<<8 | int64(data[7])

	return timestamp, nil
}
