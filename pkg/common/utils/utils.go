// Package utils provides common utility functions and helpers for the anyproxy system.
// It includes string manipulation, data conversion, and other general-purpose utilities.
package utils

import (
	"fmt"

	"github.com/rs/xid"
)

// UserContext user context
type UserContext struct {
	Username string
	GroupID  string
}

// GroupIDToTUICUUID converts a groupID to a TUIC-compatible UUID
// Simple conversion: copy groupID bytes to [16]byte UUID, pad with zeros if needed
// This matches the server-side conversion so clients can use the same groupID
// Returns both the raw [16]byte UUID and formatted string
func GroupIDToTUICUUID(groupID string) (uuid [16]byte, formatted string) {
	// Simply copy groupID bytes to UUID array, pad with zeros if too short
	copy(uuid[:], groupID)

	// Format as standard UUID string (xxxxxxxx-xxxx-xxxx-xxxx-xxxxxxxxxxxx)
	formatted = fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
		uuid[0:4], uuid[4:6], uuid[6:8], uuid[8:10], uuid[10:16])

	return uuid, formatted
}

// GatewayProxy proxy interface (simplified version - only keeps truly used methods)
type GatewayProxy interface {
	// Start starts the proxy server
	Start() error
	// Stop stops the proxy server
	Stop() error
}

// GenerateConnID generate a unique connection ID
func GenerateConnID() string {
	// use xid to generate a unique connection ID
	// Length: 20 characters
	return xid.New().String()
}

// GetMessageFields gets all field names from a message (for debugging)
func GetMessageFields(msg map[string]interface{}) []string {
	fields := make([]string, 0, len(msg))
	for key := range msg {
		fields = append(fields, key)
	}
	return fields
}
