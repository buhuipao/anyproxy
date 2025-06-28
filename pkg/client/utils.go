package client

import (
	"fmt"

	"github.com/rs/xid"
)

// getClientID gets client ID for logging use
func (c *Client) getClientID() string {
	if c.actualID != "" {
		return c.actualID
	}
	return c.config.ClientID
}

// generateClientID generates a unique client ID
func generateClientID(clientID string, replicaIdx int) string {
	// Include replica index in generated ID to ensure uniqueness
	generatedID := fmt.Sprintf("%s-r%d-%s", clientID, replicaIdx, xid.New().String())
	return generatedID
}
