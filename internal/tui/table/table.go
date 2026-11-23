// Package table is the list component every screen of the terminal UI is
// built on: a table with a cursor that scrolls in both directions.
//
// When the columns do not fit the terminal, the table scrolls sideways one
// column at a time while the leading "pinned" columns stay in place, so it is
// always clear which row is which. That is the only thing the UI does for
// small screens; no view has size-specific code.
package table

import (
	"sort"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// Column describes one column.
type Column struct {
	Title string
	// Numeric columns are right-aligned and sorted by Cell.Num.
	Numeric bool
	// MaxWidth caps the column width; longer text ends in an ellipsis.
	// Zero means DefaultMaxWidth.
	MaxWidth int
}

// DefaultMaxWidth is the widest a column gets unless it says otherwise.
const DefaultMaxWidth = 28

// Cell is one value of a row.
type Cell struct {
	Text string
	// Num is the value a numeric column is sorted by.
	Num float64
}

// Text returns a plain text cell.
func Text(s string) Cell { return Cell{Text: s} }

// Num returns a cell that shows text and sorts as n.
func Num(text string, n float64) Cell { return Cell{Text: text, Num: n} }

// Row is one line of the table.
type Row struct {
	// Key identifies the row across reloads, so the cursor can stay on it.
	Key   string
	Cells []Cell
	// Dim rows are drawn faint, e.g. offline devices.
	Dim bool
}

// Styles are the looks of the table parts.
type Styles struct {
	Header   lipgloss.Style
	Selected lipgloss.Style
	Dim      lipgloss.Style
}

// DefaultStyles returns looks that work with and without color.
func DefaultStyles() Styles {
	return Styles{
		Header:   lipgloss.NewStyle().Bold(true),
		Selected: lipgloss.NewStyle().Reverse(true),
		Dim:      lipgloss.NewStyle().Faint(true),
	}
}

const (
	gap        = 2 // spaces between columns
	markerCols = 2 // width of the cursor marker in front of each row
	cursorMark = "▸ "
	moreLeft   = "‹ "
	moreRight  = "›"
	sortMark   = "▲"
)

// Model is the table state. The zero value is not usable; call New.
type Model struct {
	Styles Styles

	cols   []Column
	pinned int
	rows   []Row
	widths []int // column widths, from all rows so they do not jump while filtering

	filter  string
	sortCol int   // column sorted by, -1 for the order the rows were given in
	visible []int // indexes into rows after filtering and sorting

	cursor int // index into visible
	top    int // first visible row shown
	xoff   int // how many unpinned columns are scrolled out to the left
	width  int
	height int
}

// New returns an empty table. The first pinned columns stay in place when
// the table scrolls sideways.
func New(cols []Column, pinned int) Model {
	m := Model{Styles: DefaultStyles(), cols: cols, pinned: min(pinned, len(cols)), sortCol: -1}
	m.measure()
	return m
}

// SetSize sets the space the table may use, header line included.
func (m *Model) SetSize(width, height int) {
	m.width, m.height = max(width, 0), max(height, 0)
	m.clamp()
}

// SetRows replaces the rows. The cursor stays on the row with the same key
// when it still exists.
func (m *Model) SetRows(rows []Row) {
	key, hadKey := m.SelectedKey()
	m.rows = rows
	m.measure()
	m.refresh()
	if hadKey {
		m.selectKey(key)
	}
	m.clamp()
}

// SetFilter shows only rows with the text in any cell, ignoring case.
func (m *Model) SetFilter(filter string) {
	key, hadKey := m.SelectedKey()
	m.filter = filter
	m.refresh()
	if hadKey {
		m.selectKey(key)
	}
	m.clamp()
}

// Filter returns the current filter text.
func (m *Model) Filter() string { return m.filter }

// CycleSort sorts by the next column: the given order first, then each
// column in turn, then back to the given order.
func (m *Model) CycleSort() {
	key, hadKey := m.SelectedKey()
	m.sortCol++
	for m.sortCol < len(m.cols) && m.cols[m.sortCol].Title == "" {
		m.sortCol++ // a column without a title, such as a status symbol
	}
	if m.sortCol >= len(m.cols) {
		m.sortCol = -1
	}
	m.refresh()
	if hadKey {
		m.selectKey(key)
	}
	m.clamp()
}

// SortColumn returns the title of the column sorted by, or "".
func (m *Model) SortColumn() string {
	if m.sortCol < 0 {
		return ""
	}
	return m.cols[m.sortCol].Title
}

// Len returns the number of rows shown after filtering.
func (m *Model) Len() int { return len(m.visible) }

// Total returns the number of rows before filtering.
func (m *Model) Total() int { return len(m.rows) }

// Selected returns the row under the cursor.
func (m *Model) Selected() (Row, bool) {
	if m.cursor < 0 || m.cursor >= len(m.visible) {
		return Row{}, false
	}
	return m.rows[m.visible[m.cursor]], true
}

// SelectedKey returns the key of the row under the cursor.
func (m *Model) SelectedKey() (string, bool) {
	row, ok := m.Selected()
	return row.Key, ok
}

// Select moves the cursor to the row with the given key, if it is shown.
func (m *Model) Select(key string) {
	m.selectKey(key)
	m.clamp()
}

// Position returns the cursor position counted from 1, or 0 when the table
// is empty.
func (m *Model) Position() int {
	if len(m.visible) == 0 {
		return 0
	}
	return m.cursor + 1
}

// Move moves the cursor by delta rows.
func (m *Model) Move(delta int) {
	m.cursor += delta
	m.clamp()
}

// Page moves the cursor by delta screens.
func (m *Model) Page(delta int) { m.Move(delta * max(m.bodyHeight(), 1)) }

// Top moves the cursor to the first row.
func (m *Model) Top() { m.Move(-len(m.rows)) }

// Bottom moves the cursor to the last row.
func (m *Model) Bottom() { m.Move(len(m.rows)) }

// ScrollRight scrolls one column to the right, if any column is cut off.
func (m *Model) ScrollRight() {
	if m.CanScrollRight() {
		m.xoff++
	}
}

// ScrollLeft scrolls one column back to the left.
func (m *Model) ScrollLeft() {
	if m.xoff > 0 {
		m.xoff--
	}
}

// CanScrollRight reports whether columns are cut off on the right.
func (m *Model) CanScrollRight() bool {
	return m.xoff < len(m.cols)-m.pinned-1 && m.lineWidth() > m.width
}

// CanScrollLeft reports whether columns are scrolled out on the left.
func (m *Model) CanScrollLeft() bool { return m.xoff > 0 }

// lineWidth is the width of a full line from the current scroll position.
func (m *Model) lineWidth() int {
	w := markerCols
	for _, i := range m.shownColumns() {
		w += m.widths[i] + gap
	}
	return w - gap
}

// shownColumns lists the columns drawn at the current scroll position: the
// pinned ones, then the rest starting after the scrolled-out ones.
func (m *Model) shownColumns() []int {
	cols := make([]int, 0, len(m.cols))
	for i := range m.cols {
		if i < m.pinned || i >= m.pinned+m.xoff {
			cols = append(cols, i)
		}
	}
	return cols
}

func (m *Model) bodyHeight() int { return max(m.height-1, 0) }

// measure computes the column widths from the titles and all rows.
func (m *Model) measure() {
	m.widths = make([]int, len(m.cols))
	for i, c := range m.cols {
		m.widths[i] = lipgloss.Width(c.Title) + lipgloss.Width(sortMark)
	}
	for _, row := range m.rows {
		for i := range m.cols {
			if i < len(row.Cells) {
				m.widths[i] = max(m.widths[i], lipgloss.Width(row.Cells[i].Text))
			}
		}
	}
	for i, c := range m.cols {
		limit := c.MaxWidth
		if limit <= 0 {
			limit = DefaultMaxWidth
		}
		m.widths[i] = min(m.widths[i], limit)
	}
}

// refresh recomputes which rows are visible and in what order.
func (m *Model) refresh() {
	m.visible = m.visible[:0]
	needle := strings.ToLower(m.filter)
	for i, row := range m.rows {
		if needle == "" || rowContains(row, needle) {
			m.visible = append(m.visible, i)
		}
	}
	if m.sortCol < 0 {
		return
	}
	col := m.sortCol
	numeric := m.cols[col].Numeric
	cell := func(i int) Cell {
		if cells := m.rows[i].Cells; col < len(cells) {
			return cells[col]
		}
		return Cell{}
	}
	sort.SliceStable(m.visible, func(a, b int) bool {
		ca, cb := cell(m.visible[a]), cell(m.visible[b])
		if numeric {
			return ca.Num < cb.Num
		}
		return strings.ToLower(ca.Text) < strings.ToLower(cb.Text)
	})
}

func rowContains(row Row, needle string) bool {
	for _, c := range row.Cells {
		if strings.Contains(strings.ToLower(c.Text), needle) {
			return true
		}
	}
	return false
}

func (m *Model) selectKey(key string) {
	for pos, i := range m.visible {
		if m.rows[i].Key == key {
			m.cursor = pos
			return
		}
	}
}

// clamp keeps the cursor on a row and the row on screen.
func (m *Model) clamp() {
	m.cursor = max(min(m.cursor, len(m.visible)-1), 0)
	body := m.bodyHeight()
	if m.cursor < m.top {
		m.top = m.cursor
	}
	if body > 0 && m.cursor >= m.top+body {
		m.top = m.cursor - body + 1
	}
	m.top = max(min(m.top, len(m.visible)-body), 0)
	m.xoff = max(min(m.xoff, len(m.cols)-m.pinned-1), 0)
}

// View draws the table: a header line and as many rows as fit. Every line
// is exactly as wide as the table.
func (m *Model) View() string {
	if m.width <= 0 || m.height <= 0 {
		return ""
	}
	cols := m.shownColumns()
	lines := make([]string, 0, m.height)

	// Header, with markers showing that there is more to either side.
	lead := strings.Repeat(" ", markerCols)
	if m.CanScrollLeft() {
		lead = moreLeft
	}
	titles := make([]string, len(cols))
	for n, i := range cols {
		title := m.cols[i].Title
		if i == m.sortCol {
			title += sortMark
		}
		titles[n] = m.align(i, title)
	}
	header := fit(lead+strings.Join(titles, strings.Repeat(" ", gap)), m.width)
	if m.CanScrollRight() {
		header = fit(header, m.width-lipgloss.Width(moreRight)) + moreRight
	}
	lines = append(lines, m.Styles.Header.Render(header))

	end := min(m.top+m.bodyHeight(), len(m.visible))
	for pos := m.top; pos < end; pos++ {
		row := m.rows[m.visible[pos]]
		cells := make([]string, len(cols))
		for n, i := range cols {
			text := ""
			if i < len(row.Cells) {
				text = row.Cells[i].Text
			}
			cells[n] = m.align(i, text)
		}
		lead := strings.Repeat(" ", markerCols)
		if pos == m.cursor {
			lead = cursorMark
		}
		line := fit(lead+strings.Join(cells, strings.Repeat(" ", gap)), m.width)
		switch {
		case pos == m.cursor:
			line = m.Styles.Selected.Render(line)
		case row.Dim:
			line = m.Styles.Dim.Render(line)
		}
		lines = append(lines, line)
	}
	return strings.Join(lines, "\n")
}

// align fits text into column i, right-aligned for numeric columns.
func (m *Model) align(i int, text string) string {
	w := m.widths[i]
	text = ansi.Truncate(text, w, "…")
	pad := strings.Repeat(" ", w-lipgloss.Width(text))
	if m.cols[i].Numeric {
		return pad + text
	}
	return text + pad
}

// fit cuts or pads a line to exactly width cells.
func fit(line string, width int) string {
	if width <= 0 {
		return ""
	}
	line = ansi.Truncate(line, width, "")
	return line + strings.Repeat(" ", width-lipgloss.Width(line))
}
