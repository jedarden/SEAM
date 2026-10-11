package limits

import "time"

// Defaults bound route-authored buffering and timeout overrides. Deployments
// set these ceilings explicitly so fragments cannot increase them.
const (
	DefaultMaxRequestBytesCeiling  int64 = 8 * 1024 * 1024
	DefaultMaxResponseBytesCeiling int64 = 8 * 1024 * 1024
	DefaultMaxTimeoutCeiling             = 120 * time.Second
)

type Ceilings struct {
	RequestBytes  int64
	ResponseBytes int64
	Timeout       time.Duration
}

func (c Ceilings) WithDefaults() Ceilings {
	if c.RequestBytes <= 0 {
		c.RequestBytes = DefaultMaxRequestBytesCeiling
	}
	if c.ResponseBytes <= 0 {
		c.ResponseBytes = DefaultMaxResponseBytesCeiling
	}
	if c.Timeout <= 0 {
		c.Timeout = DefaultMaxTimeoutCeiling
	}
	return c
}
