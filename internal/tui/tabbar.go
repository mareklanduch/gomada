package tui

import (
	"strings"

	"charm.land/lipgloss/v2"
)

// Markers showing that there are more tabs to either side.
const (
	tabsBefore = "‹ "
	tabsAfter  = " ›"
)

// tabBar draws the tab titles in at most width cells. The current tab is in
// brackets. When they do not all fit, the bar shows a run of neighboring
// tabs that includes the current one, preferring the tabs after it, with
// markers for the tabs left out on either side.
func tabBar(titles []string, current, width int) string {
	segment := func(i int) string {
		if i == current {
			return "[" + titles[i] + "]"
		}
		return " " + titles[i] + " "
	}
	draw := func(lo, hi int) string {
		parts := make([]string, 0, hi-lo+1)
		for i := lo; i <= hi; i++ {
			parts = append(parts, segment(i))
		}
		s := strings.Join(parts, " ")
		if lo > 0 {
			s = tabsBefore + s
		}
		if hi < len(titles)-1 {
			s += tabsAfter
		}
		return s
	}
	for size := len(titles); size >= 1; size-- {
		for lo := min(current, len(titles)-size); lo >= max(current-size+1, 0); lo-- {
			if s := draw(lo, lo+size-1); lipgloss.Width(s) <= width {
				return s
			}
		}
	}
	return cut(segment(current), width)
}

// tabBarMinWidth is the space the bar needs to show the current tab whole.
func tabBarMinWidth(titles []string, current int) int {
	w := lipgloss.Width(titles[current]) + 2
	if current > 0 {
		w += lipgloss.Width(tabsBefore)
	}
	if current < len(titles)-1 {
		w += lipgloss.Width(tabsAfter)
	}
	return w
}
