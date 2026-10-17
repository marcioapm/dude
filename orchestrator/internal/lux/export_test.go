package lux

import "time"

// WithExecTimeout sets the client's Exec deadline for callers that set none.
func (c *HTTPClient) WithExecTimeout(d time.Duration) *HTTPClient {
	c.execTimeout = d
	return c
}
