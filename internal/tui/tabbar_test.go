package tui

import (
	"testing"

	"charm.land/lipgloss/v2"
)

func TestTabBar(t *testing.T) {
	titles := []string{"Devices", "Clients", "Wi-Fi", "Alerts (2)"}
	tests := []struct {
		current, width int
		want           string
	}{
		{0, 80, "[Devices]  Clients   Wi-Fi   Alerts (2) "},
		{2, 80, " Devices   Clients  [Wi-Fi]  Alerts (2) "},
		{2, 40, " Devices   Clients  [Wi-Fi]  Alerts (2) "}, // exactly fits
		{0, 39, "[Devices]  Clients   Wi-Fi  ›"},
		{1, 39, "‹ [Clients]  Wi-Fi   Alerts (2) "},
		{2, 39, "‹  Clients  [Wi-Fi]  Alerts (2) "},
		{3, 39, "‹  Clients   Wi-Fi  [Alerts (2)]"},
		{2, 24, "‹ [Wi-Fi]  Alerts (2) "}, // the next tab is preferred
		{2, 21, "‹  Clients  [Wi-Fi] ›"},  // when it does not fit, the previous one
		{2, 11, "‹ [Wi-Fi] ›"},
		{0, 11, "[Devices] ›"},
		{3, 14, "‹ [Alerts (2)]"},
		{2, 10, "[Wi-Fi]"}, // no room for the markers: the current tab alone
		{3, 8, "[Alerts…"},
		{0, 0, ""},
	}
	for _, tt := range tests {
		got := tabBar(titles, tt.current, tt.width)
		if got != tt.want {
			t.Errorf("tabBar(current %d, width %d) = %q, want %q", tt.current, tt.width, got, tt.want)
		}
		if w := lipgloss.Width(got); w > tt.width {
			t.Errorf("tabBar(current %d, width %d) is %d wide", tt.current, tt.width, w)
		}
	}
}

func TestTabBarAlwaysShowsTheCurrentTabWhenThereIsRoom(t *testing.T) {
	titles := []string{"Devices", "Clients", "Wi-Fi", "Alerts"}
	for current := range titles {
		need := tabBarMinWidth(titles, current)
		for width := need; width < 60; width++ {
			got := tabBar(titles, current, width)
			if want := "[" + titles[current] + "]"; !contains(got, want) {
				t.Errorf("current %d, width %d: %q does not show %s", current, width, got, want)
			}
		}
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
