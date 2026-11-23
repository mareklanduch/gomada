package omada

import "context"

// Site is one site managed by a controller.
type Site struct {
	ID       string `json:"siteId"`
	Name     string `json:"name"`
	Region   string `json:"region,omitempty"`
	Scenario string `json:"scenario,omitempty"`
}

// Sites lists the sites the application may access.
func (c *Client) Sites(ctx context.Context) ([]Site, error) {
	return list[Site](ctx, c, "/sites")
}
