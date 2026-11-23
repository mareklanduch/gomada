package omada

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"time"
)

// Alert is something the controller raised for a site.
type Alert struct {
	ID      Text   `json:"id"` // text on current controllers; a number is accepted too
	Key     string `json:"key"`
	Module  string `json:"module"`
	Level   string `json:"level"`
	Content string `json:"content"`
	// Millis is when the alert was raised, in milliseconds since 1970.
	Millis Num `json:"time"`
	// Resolved is not sent with each alert; Alerts fills it in from which
	// of its two queries returned the alert.
	Resolved bool `json:"-"`
}

// Time returns when the alert was raised.
func (a Alert) Time() time.Time { return time.UnixMilli(int64(a.Millis)) }

// AlertsPerState is how many of the newest alerts Alerts returns for each
// state, resolved and unresolved.
const AlertsPerState = 100

// Alerts lists the newest alerts of a site raised since the given time,
// newest first: up to AlertsPerState unresolved and as many resolved ones.
// The controller does not say with each alert whether it is resolved, so the
// two states are asked for separately.
func (c *Client) Alerts(ctx context.Context, siteID string, since time.Time) ([]Alert, error) {
	all := []Alert{}
	for _, resolved := range []bool{false, true} {
		var res struct {
			Data []Alert `json:"data"`
		}
		path := fmt.Sprintf("%s?page=1&pageSize=%d&filters.timeStart=%d&filters.timeEnd=%d&filters.resolved=%t",
			sitePath(siteID, "/logs/alerts"), AlertsPerState, since.UnixMilli(), c.now().UnixMilli(), resolved)
		if err := c.call(ctx, http.MethodGet, path, nil, &res); err != nil {
			return nil, err
		}
		for _, a := range res.Data {
			a.Resolved = resolved
			all = append(all, a)
		}
	}
	sort.SliceStable(all, func(i, j int) bool { return all[i].Millis > all[j].Millis })
	return all, nil
}
