package table_test

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/mareklanduch/gomada/internal/tui/table"
)

var columns = []table.Column{
	{Title: "NAME"},
	{Title: "TYPE"},
	{Title: "IP"},
	{Title: "CPU", Numeric: true},
}

func rows() []table.Row {
	return []table.Row{
		{Key: "1", Cells: []table.Cell{table.Text("Core router"), table.Text("gateway"), table.Text("192.168.0.1"), table.Num("8%", 8)}},
		{Key: "2", Cells: []table.Cell{table.Text("Rack switch"), table.Text("switch"), table.Text("192.168.0.2"), table.Num("12%", 12)}},
		{Key: "3", Cells: []table.Cell{table.Text("AP office"), table.Text("ap"), table.Text("192.168.0.21"), table.Num("5%", 5)}},
		{Key: "4", Cells: []table.Cell{table.Text("AP garage"), table.Text("ap"), table.Text("192.168.0.23"), table.Num("-", -1)}, Dim: true},
	}
}

func newTable(width, height int) table.Model {
	m := table.New(columns, 1)
	m.SetRows(rows())
	m.SetSize(width, height)
	return m
}

// lines returns the table as plain text lines, without styling.
func lines(m table.Model) []string {
	return strings.Split(ansi.Strip(m.View()), "\n")
}

func wantLines(t *testing.T, m table.Model, want ...string) {
	t.Helper()
	got := lines(m)
	if len(got) != len(want) {
		t.Fatalf("got %d lines, want %d:\n%s", len(got), len(want), strings.Join(got, "\n"))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("line %d:\n got %q\nwant %q", i, got[i], want[i])
		}
	}
}

func TestViewWide(t *testing.T) {
	m := newTable(44, 5)
	wantLines(t, m,
		"  NAME         TYPE     IP             CPU  ",
		"▸ Core router  gateway  192.168.0.1     8%  ",
		"  Rack switch  switch   192.168.0.2    12%  ",
		"  AP office    ap       192.168.0.21    5%  ",
		"  AP garage    ap       192.168.0.23     -  ",
	)
	if m.CanScrollRight() || m.CanScrollLeft() {
		t.Error("a table that fits must not scroll sideways")
	}
}

func TestEveryLineIsExactlyAsWideAsTheTable(t *testing.T) {
	for _, width := range []int{1, 2, 5, 13, 20, 34, 44, 80} {
		m := newTable(width, 5)
		for range 4 {
			for i, line := range lines(m) {
				if got := ansi.StringWidth(line); got != width {
					t.Errorf("width %d, line %d is %d wide: %q", width, i, got, line)
				}
			}
			m.ScrollRight()
		}
	}
}

func TestSidewaysScrollingKeepsPinnedColumn(t *testing.T) {
	m := newTable(24, 3)
	wantLines(t, m,
		"  NAME         TYPE    ›",
		"▸ Core router  gateway  ",
		"  Rack switch  switch   ",
	)
	if !m.CanScrollRight() || m.CanScrollLeft() {
		t.Fatal("want more columns to the right only")
	}

	m.ScrollRight()
	wantLines(t, m,
		"‹ NAME         IP      ›",
		"▸ Core router  192.168.0",
		"  Rack switch  192.168.0",
	)

	m.ScrollRight()
	wantLines(t, m,
		"‹ NAME          CPU     ",
		"▸ Core router    8%     ",
		"  Rack switch   12%     ",
	)
	if m.CanScrollRight() {
		t.Error("the last column is visible; there is nothing more to the right")
	}
	m.ScrollRight() // must not scroll past the last column
	wantLines(t, m,
		"‹ NAME          CPU     ",
		"▸ Core router    8%     ",
		"  Rack switch   12%     ",
	)

	m.ScrollLeft()
	m.ScrollLeft()
	m.ScrollLeft() // must not scroll before the first column
	if m.CanScrollLeft() {
		t.Error("want the table back at the first column")
	}
}

func TestWideningTheTableUndoesTheScroll(t *testing.T) {
	m := newTable(24, 3)
	m.ScrollRight()
	m.ScrollRight()
	m.SetSize(80, 3)
	if m.CanScrollRight() {
		t.Error("nothing is cut off at width 80")
	}
	m.ScrollLeft()
	m.ScrollLeft()
	wantLines(t, m,
		"  NAME         TYPE     IP             CPU"+strings.Repeat(" ", 38),
		"▸ Core router  gateway  192.168.0.1     8%"+strings.Repeat(" ", 38),
		"  Rack switch  switch   192.168.0.2    12%"+strings.Repeat(" ", 38),
	)
}

func TestVerticalScrolling(t *testing.T) {
	m := newTable(44, 3) // header + 2 rows
	selected := func() string { k, _ := m.SelectedKey(); return k }
	first := func() string { return strings.TrimSpace(string([]rune(lines(m)[1])[2:15])) }

	m.Move(1)
	if selected() != "2" || first() != "Core router" {
		t.Errorf("after one step: selected %s, first row %q", selected(), first())
	}
	m.Move(1)
	if selected() != "3" || first() != "Rack switch" {
		t.Errorf("after two steps: selected %s, first row %q", selected(), first())
	}
	m.Move(10)
	if selected() != "4" || m.Position() != 4 {
		t.Errorf("past the end: selected %s at %d", selected(), m.Position())
	}
	m.Top()
	if selected() != "1" || first() != "Core router" {
		t.Errorf("Top: selected %s, first row %q", selected(), first())
	}
	m.Bottom()
	if selected() != "4" {
		t.Errorf("Bottom: selected %s", selected())
	}
	m.Page(-1)
	if selected() != "2" {
		t.Errorf("Page(-1): selected %s", selected())
	}
	m.Move(-10)
	if selected() != "1" {
		t.Errorf("before the start: selected %s", selected())
	}
}

func TestFilter(t *testing.T) {
	m := newTable(44, 5)
	m.Move(2) // AP office
	m.SetFilter("AP")
	if m.Len() != 2 || m.Total() != 4 {
		t.Fatalf("Len = %d, Total = %d", m.Len(), m.Total())
	}
	if key, _ := m.SelectedKey(); key != "3" {
		t.Errorf("the cursor must stay on the same row; selected %s", key)
	}
	m.SetFilter("192.168.0.1")
	if key, _ := m.SelectedKey(); m.Len() != 1 || key != "1" {
		t.Errorf("filter by IP: Len = %d, selected %s", m.Len(), key)
	}
	m.SetFilter("nothing matches")
	if _, ok := m.Selected(); ok || m.Len() != 0 || m.Position() != 0 {
		t.Error("an empty result must have no selection")
	}
	wantLines(t, m, "  NAME         TYPE     IP             CPU  ")
	m.SetFilter("")
	if m.Len() != 4 || m.Filter() != "" {
		t.Errorf("clearing the filter: Len = %d", m.Len())
	}
}

func TestSort(t *testing.T) {
	m := newTable(44, 5)
	order := func() string {
		var keys []string
		m.Top()
		for range m.Len() {
			k, _ := m.SelectedKey()
			keys = append(keys, k)
			m.Move(1)
		}
		m.Top()
		return strings.Join(keys, "")
	}
	if got := order(); got != "1234" || m.SortColumn() != "" {
		t.Errorf("given order: %s", got)
	}
	m.CycleSort() // NAME
	if got := order(); got != "4312" || m.SortColumn() != "NAME" {
		t.Errorf("by name: %s", got)
	}
	if !strings.Contains(lines(m)[0], "NAME▲") {
		t.Errorf("the header must mark the sorted column: %q", lines(m)[0])
	}
	m.CycleSort() // TYPE: stable, so the two APs keep their given order
	if got := order(); got != "3412" {
		t.Errorf("by type: %s", got)
	}
	m.CycleSort() // IP
	m.CycleSort() // CPU, numeric: -1, 5, 8, 12
	if got := order(); got != "4312" || m.SortColumn() != "CPU" {
		t.Errorf("by CPU: %s (a text sort would put 12%% before 5%%)", got)
	}
	m.CycleSort() // back to the given order
	if got := order(); got != "1234" || m.SortColumn() != "" {
		t.Errorf("after a full cycle: %s", got)
	}
}

func TestSortSkipsColumnsWithoutTitle(t *testing.T) {
	m := table.New([]table.Column{{Title: ""}, {Title: "NAME"}}, 2)
	m.SetRows([]table.Row{{Key: "1", Cells: []table.Cell{table.Text("●"), table.Text("b")}}})
	m.CycleSort()
	if got := m.SortColumn(); got != "NAME" {
		t.Errorf("first sort column = %q, want NAME", got)
	}
	m.CycleSort()
	if got := m.SortColumn(); got != "" {
		t.Errorf("after the last column: %q, want the given order", got)
	}
}

func TestSetRowsKeepsCursorOnSameKey(t *testing.T) {
	m := newTable(44, 5)
	m.Move(2) // key 3
	reordered := rows()
	reordered[0], reordered[2] = reordered[2], reordered[0]
	m.SetRows(reordered)
	if key, _ := m.SelectedKey(); key != "3" || m.Position() != 1 {
		t.Errorf("selected %s at %d, want key 3 at its new position 1", key, m.Position())
	}
	m.SetRows(rows()[:1]) // the selected row is gone
	if key, ok := m.SelectedKey(); !ok || key != "1" {
		t.Errorf("selected %q, %v; want the remaining row", key, ok)
	}
	m.SetRows(nil)
	if _, ok := m.Selected(); ok {
		t.Error("an empty table has no selection")
	}
	m.Move(1) // must not panic
}

func TestLongTextIsCutWithEllipsis(t *testing.T) {
	m := table.New([]table.Column{{Title: "NAME", MaxWidth: 8}, {Title: "X"}}, 1)
	m.SetRows([]table.Row{{Key: "1", Cells: []table.Cell{table.Text("A very long device name"), table.Text("y")}}})
	m.SetSize(20, 2)
	wantLines(t, m,
		"  NAME      X       ",
		"▸ A very …  y       ",
	)
}

func TestShortRowsAndWideCharacters(t *testing.T) {
	m := table.New([]table.Column{{Title: "NAME"}, {Title: "NOTE"}}, 1)
	m.SetRows([]table.Row{
		{Key: "1", Cells: []table.Cell{table.Text("Büro 日本")}}, // one cell short
		{Key: "2", Cells: []table.Cell{table.Text("b"), table.Text("ok")}},
	})
	m.SetSize(18, 3)
	wantLines(t, m,
		"  NAME       NOTE ",
		"▸ Büro 日本       ",
		"  b          ok   ",
	)
}

func TestNoSpace(t *testing.T) {
	m := newTable(0, 0)
	if m.View() != "" {
		t.Errorf("View with no space = %q", m.View())
	}
	m.SetSize(30, 1) // room for the header only
	if got := lines(m); len(got) != 1 {
		t.Errorf("got %d lines, want just the header", len(got))
	}
	m.Move(3) // must not panic without a body
}
