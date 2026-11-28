//! The list component every screen of the terminal UI is built on: a table
//! with a cursor that scrolls in both directions.
//!
//! When the columns do not fit the terminal, the table scrolls sideways one
//! column at a time while the leading "pinned" columns stay in place, so it
//! is always clear which row is which. That is the only thing the UI does
//! for small screens; no view has size-specific code.

use super::text::{fit, truncate, width};

/// Describes one column.
#[derive(Debug, Clone, Copy)]
pub struct Column {
    pub title: &'static str,
    /// Numeric columns are right-aligned and sorted by [`Cell::num`].
    pub numeric: bool,
    /// Caps the column width; longer text ends in an ellipsis. Zero means
    /// [`DEFAULT_MAX_WIDTH`].
    pub max_width: usize,
}

/// A text column.
pub const fn col(title: &'static str) -> Column {
    Column { title, numeric: false, max_width: 0 }
}

/// A right-aligned column sorted by number.
pub const fn num_col(title: &'static str) -> Column {
    Column { title, numeric: true, max_width: 0 }
}

/// A text column with its own width limit.
pub const fn wide_col(title: &'static str, max_width: usize) -> Column {
    Column { title, numeric: false, max_width }
}

/// The widest a column gets unless it says otherwise.
pub const DEFAULT_MAX_WIDTH: usize = 28;

/// One value of a row.
#[derive(Debug, Clone, Default, PartialEq)]
pub struct Cell {
    pub text: String,
    /// The value a numeric column is sorted by.
    pub num: f64,
}

/// A plain text cell.
pub fn text(s: impl Into<String>) -> Cell {
    Cell { text: s.into(), num: 0.0 }
}

/// A cell that shows text and sorts as `n`.
pub fn num(s: impl Into<String>, n: f64) -> Cell {
    Cell { text: s.into(), num: n }
}

/// One line of the table.
#[derive(Debug, Clone, Default, PartialEq)]
pub struct Row {
    /// Identifies the row across reloads, so the cursor can stay on it.
    pub key: String,
    pub cells: Vec<Cell>,
    /// Dim rows are drawn faint, e.g. offline devices.
    pub dim: bool,
}

/// How a drawn line is to be styled.
#[derive(Debug, Clone, Copy, PartialEq)]
pub enum Look {
    Header,
    Selected,
    Dim,
    Plain,
}

const GAP: usize = 2; // spaces between columns
const MARKER_COLS: usize = 2; // width of the cursor marker in front of each row
const CURSOR_MARK: &str = "▸ ";
const MORE_LEFT: &str = "‹ ";
const MORE_RIGHT: &str = "›";
const SORT_MARK: &str = "▲";

/// The table state.
#[derive(Debug, Clone)]
pub struct Table {
    cols: Vec<Column>,
    pinned: usize,
    rows: Vec<Row>,
    /// Column widths, from all rows so they do not jump while filtering.
    widths: Vec<usize>,

    filter: String,
    /// The column sorted by; `None` for the order the rows were given in.
    sort_col: Option<usize>,
    /// Indexes into rows after filtering and sorting.
    visible: Vec<usize>,

    cursor: usize, // index into visible
    top: usize,    // first visible row shown
    xoff: usize,   // how many unpinned columns are scrolled out to the left
    width: usize,
    height: usize,
}

impl Table {
    /// Returns an empty table. The first `pinned` columns stay in place
    /// when the table scrolls sideways.
    pub fn new(cols: Vec<Column>, pinned: usize) -> Table {
        let mut t = Table {
            pinned: pinned.min(cols.len()),
            cols,
            rows: Vec::new(),
            widths: Vec::new(),
            filter: String::new(),
            sort_col: None,
            visible: Vec::new(),
            cursor: 0,
            top: 0,
            xoff: 0,
            width: 0,
            height: 0,
        };
        t.measure();
        t
    }

    /// Sets the space the table may use, header line included.
    pub fn set_size(&mut self, width: usize, height: usize) {
        self.width = width;
        self.height = height;
        self.clamp();
    }

    /// Runs a change and then puts the cursor back on the row it was on.
    fn keeping_selection(&mut self, change: impl FnOnce(&mut Table)) {
        let key = self.selected_key().map(str::to_string);
        change(self);
        self.refresh();
        if let Some(key) = key {
            self.select_key(&key);
        }
        self.clamp();
    }

    /// Replaces the rows. The cursor stays on the row with the same key
    /// when it still exists.
    pub fn set_rows(&mut self, rows: Vec<Row>) {
        self.keeping_selection(|t| {
            t.rows = rows;
            t.measure();
        });
    }

    /// Shows only rows with the text in any cell, ignoring case.
    pub fn set_filter(&mut self, filter: &str) {
        self.keeping_selection(|t| t.filter = filter.to_string());
    }

    /// Returns the current filter text.
    pub fn filter(&self) -> &str {
        &self.filter
    }

    /// Sorts by the next column: the given order first, then each column in
    /// turn, then back to the given order.
    pub fn cycle_sort(&mut self) {
        self.keeping_selection(|t| {
            let mut next = t.sort_col.map_or(0, |c| c + 1);
            while next < t.cols.len() && t.cols[next].title.is_empty() {
                next += 1; // a column without a title, such as a status symbol
            }
            t.sort_col = (next < t.cols.len()).then_some(next);
        });
    }

    /// Returns the title of the column sorted by, or "".
    pub fn sort_column(&self) -> &'static str {
        self.sort_col.map_or("", |c| self.cols[c].title)
    }

    /// Returns the number of rows shown after filtering.
    pub fn len(&self) -> usize {
        self.visible.len()
    }

    pub fn is_empty(&self) -> bool {
        self.visible.is_empty()
    }

    /// Returns the number of rows before filtering.
    pub fn total(&self) -> usize {
        self.rows.len()
    }

    /// Returns the row under the cursor.
    pub fn selected(&self) -> Option<&Row> {
        self.visible.get(self.cursor).map(|&i| &self.rows[i])
    }

    /// Returns the key of the row under the cursor.
    pub fn selected_key(&self) -> Option<&str> {
        self.selected().map(|r| r.key.as_str())
    }

    /// Moves the cursor to the row with the given key, if it is shown.
    pub fn select(&mut self, key: &str) {
        self.select_key(key);
        self.clamp();
    }

    /// Returns the cursor position counted from 1, or 0 when the table is
    /// empty.
    pub fn position(&self) -> usize {
        if self.visible.is_empty() {
            0
        } else {
            self.cursor + 1
        }
    }

    /// Moves the cursor by `delta` rows.
    pub fn move_by(&mut self, delta: isize) {
        self.cursor = self.cursor.saturating_add_signed(delta);
        self.clamp();
    }

    /// Moves the cursor by `delta` screens.
    pub fn page(&mut self, delta: isize) {
        self.move_by(delta * self.body_height().max(1) as isize);
    }

    /// Moves the cursor to the first row.
    pub fn top(&mut self) {
        self.cursor = 0;
        self.clamp();
    }

    /// Moves the cursor to the last row.
    pub fn bottom(&mut self) {
        self.cursor = usize::MAX;
        self.clamp();
    }

    /// Scrolls one column to the right, if any column is cut off.
    pub fn scroll_right(&mut self) {
        if self.can_scroll_right() {
            self.xoff += 1;
        }
    }

    /// Scrolls one column back to the left.
    pub fn scroll_left(&mut self) {
        self.xoff = self.xoff.saturating_sub(1);
    }

    /// Reports whether columns are cut off on the right.
    pub fn can_scroll_right(&self) -> bool {
        self.xoff + self.pinned + 1 < self.cols.len() && self.line_width() > self.width
    }

    /// Reports whether columns are scrolled out on the left.
    pub fn can_scroll_left(&self) -> bool {
        self.xoff > 0
    }

    /// The width of a full line from the current scroll position.
    fn line_width(&self) -> usize {
        let cols = self.shown_columns();
        MARKER_COLS + cols.iter().map(|&i| self.widths[i]).sum::<usize>() + GAP * cols.len().saturating_sub(1)
    }

    /// Lists the columns drawn at the current scroll position: the pinned
    /// ones, then the rest starting after the scrolled-out ones.
    fn shown_columns(&self) -> Vec<usize> {
        (0..self.cols.len()).filter(|&i| i < self.pinned || i >= self.pinned + self.xoff).collect()
    }

    fn body_height(&self) -> usize {
        self.height.saturating_sub(1)
    }

    /// Computes the column widths from the titles and all rows.
    fn measure(&mut self) {
        self.widths = self
            .cols
            .iter()
            .enumerate()
            .map(|(i, c)| {
                let widest = self
                    .rows
                    .iter()
                    .filter_map(|r| r.cells.get(i))
                    .map(|cell| width(&cell.text))
                    .max()
                    .unwrap_or(0)
                    .max(width(c.title) + width(SORT_MARK));
                let limit = if c.max_width == 0 { DEFAULT_MAX_WIDTH } else { c.max_width };
                widest.min(limit)
            })
            .collect();
    }

    /// Recomputes which rows are visible and in what order.
    fn refresh(&mut self) {
        let needle = self.filter.to_lowercase();
        self.visible = (0..self.rows.len())
            .filter(|&i| {
                needle.is_empty() || self.rows[i].cells.iter().any(|c| c.text.to_lowercase().contains(&needle))
            })
            .collect();
        let Some(col) = self.sort_col else { return };
        let rows = &self.rows;
        if self.cols[col].numeric {
            let value = |i: &usize| rows[*i].cells.get(col).map_or(0.0, |c| c.num);
            self.visible.sort_by(|a, b| value(a).total_cmp(&value(b)));
        } else {
            self.visible
                .sort_by_cached_key(|&i| rows[i].cells.get(col).map(|c| c.text.to_lowercase()).unwrap_or_default());
        }
    }

    fn select_key(&mut self, key: &str) {
        if let Some(pos) = self.visible.iter().position(|&i| self.rows[i].key == key) {
            self.cursor = pos;
        }
    }

    /// Keeps the cursor on a row and the row on screen.
    fn clamp(&mut self) {
        self.cursor = self.cursor.min(self.visible.len().saturating_sub(1));
        let body = self.body_height();
        if self.cursor < self.top {
            self.top = self.cursor;
        }
        if body > 0 && self.cursor >= self.top + body {
            self.top = self.cursor + 1 - body;
        }
        self.top = self.top.min(self.visible.len().saturating_sub(body));
        self.xoff = self.xoff.min(self.cols.len().saturating_sub(self.pinned + 1));
    }

    /// Draws the table: a header line and as many rows as fit. Every line
    /// is exactly as wide as the table.
    pub fn view(&self) -> Vec<(String, Look)> {
        if self.width == 0 || self.height == 0 {
            return Vec::new();
        }
        let cols = self.shown_columns();
        let gap = " ".repeat(GAP);
        let blank = " ".repeat(MARKER_COLS);
        let mut lines = Vec::with_capacity(self.height);

        // Header, with markers showing that there is more to either side.
        let lead = if self.can_scroll_left() { MORE_LEFT } else { &blank };
        let titles: Vec<String> = cols
            .iter()
            .map(|&i| {
                let mut title = self.cols[i].title.to_string();
                if self.sort_col == Some(i) {
                    title.push_str(SORT_MARK);
                }
                self.align(i, &title)
            })
            .collect();
        let mut header = fit(&format!("{lead}{}", titles.join(&gap)), self.width);
        if self.can_scroll_right() {
            header = fit(&header, self.width - width(MORE_RIGHT)) + MORE_RIGHT;
        }
        lines.push((header, Look::Header));

        let end = (self.top + self.body_height()).min(self.visible.len());
        for pos in self.top..end {
            let row = &self.rows[self.visible[pos]];
            let cells: Vec<String> =
                cols.iter().map(|&i| self.align(i, row.cells.get(i).map_or("", |c| c.text.as_str()))).collect();
            let lead = if pos == self.cursor { CURSOR_MARK } else { &blank };
            let line = fit(&format!("{lead}{}", cells.join(&gap)), self.width);
            let look = if pos == self.cursor {
                Look::Selected
            } else if row.dim {
                Look::Dim
            } else {
                Look::Plain
            };
            lines.push((line, look));
        }
        lines
    }

    /// Fits text into column `i`, right-aligned for numeric columns.
    fn align(&self, i: usize, s: &str) -> String {
        let w = self.widths[i];
        let s = truncate(s, w, "…");
        let pad = " ".repeat(w - width(&s));
        if self.cols[i].numeric {
            pad + &s
        } else {
            s + &pad
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn sample() -> Table {
        let mut t = Table::new(vec![col("NAME"), col("TYPE"), num_col("CPU"), col("MAC")], 1);
        t.set_rows(vec![
            Row {
                key: "1".into(),
                cells: vec![text("Core router"), text("gateway"), num("8%", 8.0), text("AA-01")],
                dim: false,
            },
            Row {
                key: "2".into(),
                cells: vec![text("Rack switch"), text("switch"), num("12%", 12.0), text("AA-02")],
                dim: false,
            },
            Row {
                key: "3".into(),
                cells: vec![text("AP garage"), text("ap"), num("-", -1.0), text("AA-04")],
                dim: true,
            },
            Row {
                key: "4".into(),
                cells: vec![text("AP office"), text("ap"), num("5%", 5.0), text("AA-03")],
                dim: false,
            },
        ]);
        t
    }

    fn lines(t: &Table) -> Vec<String> {
        t.view().into_iter().map(|(s, _)| s.trim_end().to_string()).collect()
    }

    #[test]
    fn draws_header_rows_and_cursor() {
        let mut t = sample();
        t.set_size(40, 10);
        assert_eq!(
            lines(&t),
            [
                "  NAME         TYPE      CPU  MAC",
                "▸ Core router  gateway    8%  AA-01",
                "  Rack switch  switch    12%  AA-02",
                "  AP garage    ap          -  AA-04",
                "  AP office    ap         5%  AA-03",
            ]
        );
        let looks: Vec<Look> = t.view().into_iter().map(|(_, l)| l).collect();
        assert_eq!(looks, [Look::Header, Look::Selected, Look::Plain, Look::Dim, Look::Plain]);
        assert!(t.view().iter().all(|(s, _)| width(s) == 40), "every line is exactly as wide as the table");
    }

    #[test]
    fn empty_and_tiny_tables() {
        let mut t = Table::new(vec![col("NAME")], 1);
        t.set_size(20, 5);
        assert_eq!(lines(&t), ["  NAME"]);
        assert_eq!((t.position(), t.len(), t.total()), (0, 0, 0));
        assert!(t.selected().is_none());
        t.move_by(3);
        t.page(-2);
        t.bottom();
        t.set_size(0, 0);
        assert!(t.view().is_empty());
        t.set_size(1, 1);
        assert_eq!(t.view().len(), 1);
    }

    #[test]
    fn moving_and_vertical_scrolling() {
        let mut t = sample();
        t.set_size(40, 3); // header + two rows
        t.move_by(1);
        assert_eq!(t.selected_key(), Some("2"));
        t.move_by(1);
        assert_eq!(lines(&t)[1..], ["  Rack switch  switch    12%  AA-02", "▸ AP garage    ap          -  AA-04"]);
        t.move_by(100);
        assert_eq!((t.position(), t.selected_key()), (4, Some("4")));
        t.top();
        assert_eq!(t.position(), 1);
        t.page(1);
        assert_eq!(t.position(), 3);
        t.bottom();
        assert_eq!(t.position(), 4);
        t.move_by(-100);
        assert_eq!(t.position(), 1);
    }

    #[test]
    fn sideways_scrolling_keeps_pinned_columns() {
        let mut t = sample();
        t.set_size(24, 5);
        assert!(t.can_scroll_right() && !t.can_scroll_left());
        assert_eq!(lines(&t)[..2], ["  NAME         TYPE    ›", "▸ Core router  gateway"]);
        t.scroll_right();
        assert!(t.can_scroll_left());
        // The CPU column leaves room for the sort mark.
        assert_eq!(lines(&t)[..2], ["‹ NAME          CPU  MA›", "▸ Core router    8%  AA-"]);
        t.scroll_right();
        t.scroll_right();
        t.scroll_right(); // stops at the last column
        assert_eq!(lines(&t)[..2], ["‹ NAME         MAC", "▸ Core router  AA-01"]);
        assert!(!t.can_scroll_right());
        t.scroll_left();
        t.scroll_left();
        t.scroll_left();
        assert!(!t.can_scroll_left());
        // A wider terminal brings everything back.
        t.scroll_right();
        t.set_size(80, 5);
        assert!(!t.can_scroll_right());
    }

    #[test]
    fn filtering_keeps_the_cursor_on_its_row() {
        let mut t = sample();
        t.set_size(40, 10);
        t.select("4");
        t.set_filter("AP");
        assert_eq!((t.len(), t.total(), t.selected_key()), (2, 4, Some("4")));
        t.set_filter("zzz");
        assert_eq!((t.len(), t.position()), (0, 0));
        t.set_filter("");
        assert_eq!(t.len(), 4);
        assert_eq!(t.filter(), "");
        // Column widths do not jump while filtering.
        t.set_filter("ap");
        assert!(lines(&t)[0].starts_with("  NAME         TYPE"));
    }

    #[test]
    fn sorting_cycles_through_columns() {
        let mut t = sample();
        t.set_size(40, 10);
        t.select("2");
        let order = |t: &Table| t.visible.iter().map(|&i| t.rows[i].key.clone()).collect::<String>();
        t.cycle_sort();
        assert_eq!((t.sort_column(), order(&t).as_str()), ("NAME", "3412"));
        assert_eq!(t.selected_key(), Some("2"), "the cursor follows its row");
        assert!(lines(&t)[0].starts_with("  NAME▲"));
        t.cycle_sort();
        assert_eq!(t.sort_column(), "TYPE");
        t.cycle_sort();
        assert_eq!((t.sort_column(), order(&t).as_str()), ("CPU", "3412"));
        t.cycle_sort();
        t.cycle_sort();
        assert_eq!((t.sort_column(), order(&t).as_str()), ("", "1234"));
    }

    #[test]
    fn untitled_columns_are_skipped_when_sorting() {
        let mut t = Table::new(vec![col(""), col("NAME")], 2);
        t.cycle_sort();
        assert_eq!(t.sort_column(), "NAME");
        t.cycle_sort();
        assert_eq!(t.sort_column(), "");
    }

    #[test]
    fn reloading_keeps_the_cursor_or_clamps_it() {
        let mut t = sample();
        t.set_size(40, 10);
        t.select("3");
        let mut rows = t.rows.clone();
        rows.reverse();
        t.set_rows(rows.clone());
        assert_eq!(t.selected_key(), Some("3"));
        rows.retain(|r| r.key != "3");
        t.bottom();
        t.set_rows(rows[..1].to_vec());
        assert_eq!(t.position(), 1);
    }

    #[test]
    fn long_cells_end_in_an_ellipsis() {
        let mut t = Table::new(vec![col("NAME"), wide_col("NOTE", 6)], 1);
        t.set_rows(vec![Row { key: "k".into(), cells: vec![text("x".repeat(40)), text("abcdefghij")], dim: false }]);
        t.set_size(60, 3);
        assert_eq!(lines(&t)[1], format!("▸ {}…  abcde…", "x".repeat(27)));
    }

    #[test]
    fn rows_with_missing_cells_are_drawn_blank() {
        let mut t = Table::new(vec![col("A"), num_col("B")], 1);
        t.set_rows(vec![Row { key: "k".into(), cells: vec![text("only")], dim: false }]);
        t.set_size(20, 3);
        assert_eq!(lines(&t)[1], "▸ only");
        t.cycle_sort();
        t.cycle_sort();
        assert_eq!(t.len(), 1);
    }
}
