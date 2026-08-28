package tailscale

import "time"

// CreateKeyRequest represents a request to create an API key
type CreateKeyRequest struct {
	Capabilities KeyCapabilities `json:"capabilities"`
	ExpirySeconds int64          `json:"expirySeconds,omitempty"`
	Description  string          `json:"description,omitempty"`
}

// KeyCapabilities defines what the key can do
type KeyCapabilities struct {
	Devices DeviceCapabilities `json:"devices"`
}

// DeviceCapabilities defines device creation options
type DeviceCapabilities struct {
	Create DeviceCreateOptions `json:"create"`
}

// DeviceCreateOptions defines how devices can be created
type DeviceCreateOptions struct {
	Reusable      bool     `json:"reusable"`
	Ephemeral     bool     `json:"ephemeral"`
	Tags          []string `json:"tags"`
	Preauthorized bool     `json:"preauthorized"`
}

// Key represents a Tailscale API key
type Key struct {
	ID           string          `json:"id"`
	Key          string          `json:"key"`           // Only returned on creation
	KeyType      string          `json:"keyType"`
	Description  string          `json:"description"`
	Created      time.Time       `json:"created"`
	Expires      time.Time       `json:"expires"`
	Revoked      bool            `json:"revoked"`
	Invalid      bool            `json:"invalid"`
	Capabilities KeyCapabilities  `json:"capabilities"`
}

// ListKeysResponse represents a response from listing keys
type ListKeysResponse struct {
	Keys []Key `json:"keys"`
}

// DeleteKeyResponse represents a response from deleting a key
type DeleteKeyResponse struct {
	Success bool   `json:"success"`
	Message string `json:"message,omitempty"`
}
