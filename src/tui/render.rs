//! Draws the model with ratatui: a header line, the body, and a bottom
//! line.

use ratatui::layout::{Constraint, Layout};
use ratatui::style::{Color, Modifier, Style};
use ratatui::text::{Line, Span};
use ratatui::widgets::Paragraph;
use ratatui::Frame;

use super::model::{Model, Overlay, Screen};
use super::table::Look;
use super::text::{cut, cut_left, tab_bar, tab_bar_min_width, width, wrap};

/// The looks of the parts of the screen. They work with and without color:
/// meaning is carried by words and shapes, never by color alone.
struct Styles {
    header: Style,
    faint: Style,
    error: Style,
    title: Style,
    selected: Style,
    /// Light ink on black, which has the right polarity for a QR code
    /// whatever the colors of the terminal are.
    qr: Style,
}

impl Styles {
    fn new(color: bool) -> Styles {
        let bold = Style::new().add_modifier(Modifier::BOLD);
        Styles {
            header: bold,
            faint: Style::new().add_modifier(Modifier::DIM),
            error: if color { bold.fg(Color::Red) } else { bold },
            title: bold.add_modifier(Modifier::UNDERLINED),
            selected: Style::new().add_modifier(Modifier::REVERSED),
            qr: if color { Style::new().fg(Color::White).bg(Color::Black) } else { Style::new() },
        }
    }
}

fn plain(s: impl Into<String>) -> Line<'static> {
    Line::raw(s.into())
}

fn styled(s: impl Into<String>, style: Style) -> Line<'static> {
    Line::styled(s.into(), style)
}

impl Model {
    /// Draws the whole terminal.
    pub fn draw(&mut self, frame: &mut Frame) {
        let area = frame.area();
        let (w, h) = (area.width as usize, area.height as usize);
        if w == 0 || h == 0 {
            return;
        }
        let st = Styles::new(self.color);
        let [top, middle, bottom] =
            Layout::vertical([Constraint::Length(1), Constraint::Min(0), Constraint::Length(1)]).areas(area);
        frame.render_widget(Paragraph::new(styled(cut(&self.header(w), w), st.header)), top);
        if h > 1 {
            let mut body = self.body(w, h - 2, &st);
            body.truncate(h - 2);
            frame.render_widget(Paragraph::new(body), middle);
            frame.render_widget(Paragraph::new(self.footer(w, &st)), bottom);
        }
    }

    /// Names where the person is, and on list screens where the cursor is.
    fn header(&self, w: usize) -> String {
        let left = match self.screen {
            Screen::Loading => "gomada".to_string(),
            Screen::Wizard => "gomada › add a controller".to_string(),
            Screen::Hosts => "Hosts".to_string(),
            Screen::Sites => format!("{} › sites", self.sess.as_ref().map_or("", |s| s.host())),
            Screen::Main => return self.main_header(w),
        };
        let Some(l) = self.current() else { return left };
        let right = self.position(l.table.position(), l.table.len(), l.loading);
        match w.checked_sub(width(&left) + width(&right)) {
            Some(space) if space >= 1 => format!("{left}{}{right}", " ".repeat(space)),
            _ => left,
        }
    }

    fn position(&self, at: usize, of: usize, loading: bool) -> String {
        if loading {
            format!("… {at}/{of}")
        } else {
            format!("{at}/{of}")
        }
    }

    /// The header of the tab screen: host and site, the tab bar, and the
    /// cursor position. When space is short the position goes first, then
    /// the host and site are shortened; the current tab is kept.
    fn main_header(&self, w: usize) -> String {
        const SEP: usize = 2;
        let titles: Vec<String> = self.tabs.iter().map(|t| format!("{}{}", t.active().title, t.badge())).collect();
        let l = &self.active_tab().list;
        let mut right = self.position(l.table.position(), l.table.len(), l.loading);
        let mut left = format!("{} › {}", self.sess.as_ref().map_or("", |s| s.host()), self.site.name);
        let need = tab_bar_min_width(&titles, self.tab);
        if width(&left) + SEP + need + SEP + width(&right) > w {
            right.clear();
        }
        let right_space = if right.is_empty() { 0 } else { SEP + width(&right) };
        let wanted = width(&left) + SEP + need + right_space;
        if wanted > w {
            left = cut(&left, width(&left).saturating_sub(wanted - w));
        }
        let left_space = if left.is_empty() { 0 } else { width(&left) + SEP };
        let bar = tab_bar(&titles, self.tab, w.saturating_sub(left_space + right_space));
        let head = if left.is_empty() { bar } else { format!("{left}{}{bar}", " ".repeat(SEP)) };
        if right.is_empty() {
            return head;
        }
        let space = w.saturating_sub(width(&head) + width(&right)).max(1);
        format!("{head}{}{right}", " ".repeat(space))
    }

    fn body(&mut self, w: usize, height: usize, st: &Styles) -> Vec<Line<'static>> {
        match self.overlay {
            Overlay::Help => {
                let lines = self.help_lines(w, st);
                return self.scrolled(lines, height);
            }
            Overlay::Details => {
                let lines = self.detail_lines(w, height, st);
                return self.scrolled(lines, height);
            }
            _ => {}
        }
        match self.screen {
            Screen::Loading => return std::iter::once(String::new()).chain(wrap(&self.busy, w)).map(plain).collect(),
            Screen::Wizard => return self.wizard_lines(w, st),
            _ => {}
        }

        let hosts = self.screen == Screen::Hosts;
        let Some(l) = self.current_mut() else { return Vec::new() };
        l.table.set_size(w, height);
        let mut lines: Vec<Line> = l
            .table
            .view()
            .into_iter()
            .map(|(text, look)| match look {
                Look::Header => styled(text, st.header),
                Look::Selected => styled(text, st.selected),
                Look::Dim => styled(text, st.faint),
                Look::Plain => plain(text),
            })
            .collect();
        let noun = l.noun;
        let note: Vec<Line> = match &l.err {
            Some(err) if !l.loaded => wrap(&format!("Could not load the {noun}s: {err}"), w)
                .into_iter()
                .map(|s| styled(s, st.error))
                .chain(std::iter::once(plain("Press R to try again.")))
                .collect(),
            _ => {
                let text = if !l.loaded {
                    "Loading…".to_string()
                } else if l.table.total() == 0 && hosts {
                    "No hosts yet. Press a to add one.".to_string()
                } else if l.table.total() == 0 {
                    format!("No {noun}s here.")
                } else if l.table.is_empty() {
                    format!("No {noun} matches the filter. Press esc to clear it.")
                } else {
                    String::new()
                };
                if text.is_empty() {
                    Vec::new()
                } else {
                    wrap(&text, w).into_iter().map(plain).collect()
                }
            }
        };
        if !note.is_empty() {
            lines.truncate(1);
            lines.extend(note);
        }
        lines
    }

    /// Returns the part of `lines` that starts at the scroll position of
    /// the open details or help text.
    fn scrolled(&mut self, mut lines: Vec<Line<'static>>, height: usize) -> Vec<Line<'static>> {
        self.detail_top = self.detail_top.min(lines.len().saturating_sub(height));
        lines.drain(..self.detail_top);
        lines
    }

    fn detail_lines(&self, w: usize, height: usize, st: &Styles) -> Vec<Line<'static>> {
        let mut lines = vec![styled(cut(&self.detail_title, w), st.title), plain("")];

        if self.wifi.is_some() {
            if let Some(code) = &self.wifi_code {
                // The code is only useful whole, so it gets the height that
                // is left after the title; the fields follow and can be
                // scrolled to.
                match code.fit(w, height.saturating_sub(lines.len())) {
                    Some(drawn) => lines.extend(drawn.into_iter().map(|l| styled(l, st.qr))),
                    None => {
                        let note = format!(
                            "This window is too small for the QR code, which needs {} rows. Press p to show the password.",
                            code.min_height() + lines.len() + 2
                        );
                        lines.extend(wrap(&note, w).into_iter().map(plain));
                    }
                }
            } else if !self.wifi_note.is_empty() {
                lines.extend(wrap(&self.wifi_note, w).into_iter().map(plain));
            }
            lines.push(plain(""));
        }

        let label_width = self.detail.iter().map(|f| width(&f.label)).max().unwrap_or(0);
        // A long value, such as an alert message, wraps under itself.
        let value_width = w.saturating_sub(label_width + 2).max(10);
        for f in &self.detail {
            let label = format!("{}{}", f.label, " ".repeat(label_width - width(&f.label)));
            let room = w.saturating_sub(label_width + 2);
            for (n, part) in wrap(f.value.trim_end_matches('\n'), value_width).into_iter().enumerate() {
                let lead = if n == 0 {
                    Span::styled(cut(&label, w), st.faint)
                } else {
                    Span::raw(" ".repeat(label_width.min(w)))
                };
                lines.push(Line::from(vec![lead, Span::raw("  "), Span::raw(cut(part.trim_end(), room))]));
            }
        }
        lines
    }

    fn help_lines(&self, w: usize, st: &Styles) -> Vec<Line<'static>> {
        let fixed = |k: &str, v: &str| (k.to_string(), v.to_string());
        let mut keys = vec![
            fixed("↑ ↓  k j", "move"),
            fixed("← →  h l", "scroll the table sideways"),
            fixed("g G", "first and last row"),
            fixed("enter", "open or show details"),
            fixed("/", "filter by text in any column"),
            fixed("o", "sort by the next column"),
            fixed("R", "reload"),
            fixed("tab", "next tab"),
            fixed("S", "sites of this host"),
            fixed("H", "hosts"),
            fixed("esc", "clear the filter, go back"),
            fixed("q", "quit"),
        ];
        if let Some(l) = self.current() {
            for (i, a) in l.actions.iter().enumerate() {
                let help =
                    if a.help.is_empty() { format!("{} the selected {}", a.label, l.noun) } else { a.help.to_string() };
                keys.insert(4 + i, (a.key.to_string(), help));
            }
        }
        if self.screen == Screen::Main {
            let t = &self.tabs[self.tab];
            if let Some(alt) = &t.alt {
                keys.push(("B".into(), format!("switch between {} and {}", t.title, alt.title)));
            }
            keys.push((format!("1-{}", self.tabs.len()), "jump to a tab".into()));
        }
        if self.screen == Screen::Hosts {
            keys.insert(4, fixed("a", "add a host"));
        }
        let mut lines = vec![styled("Keys", st.title), plain("")];
        lines.extend(keys.into_iter().map(|(k, v)| {
            let pad = " ".repeat(9usize.saturating_sub(k.chars().count()));
            plain(cut(&format!("{k}{pad} {v}"), w))
        }));
        lines
    }

    /// Draws the wizard: the answers so far, then the explanation of the
    /// current question, then any problem with the last answer.
    fn wizard_lines(&self, w: usize, st: &Styles) -> Vec<Line<'static>> {
        let h = &self.wiz.host;
        let mut lines = Vec::new();
        for (label, value) in [("Name", &h.name), ("Address", &h.url), ("Client ID", &h.client_id)] {
            if !value.is_empty() {
                let room = w.saturating_sub(width(label) + 2);
                lines.push(Line::from(vec![
                    Span::styled(format!("{label}:"), st.faint),
                    Span::raw(format!(" {}", cut(value, room))),
                ]));
            }
        }
        if !lines.is_empty() {
            lines.push(plain(""));
        }
        if !self.wiz.err.is_empty() {
            lines.extend(wrap(&format!("Error: {}", self.wiz.err), w).into_iter().map(|s| styled(s, st.error)));
            lines.push(plain(""));
        }
        let text = if self.wiz.working.is_empty() { &self.wiz.hint } else { &self.wiz.working };
        lines.extend(wrap(text, w).into_iter().map(plain));
        lines
    }

    /// The bottom line: an open prompt, else the latest message, else the
    /// key hints.
    fn footer(&self, w: usize, st: &Styles) -> Line<'static> {
        match self.overlay {
            Overlay::Filter => plain(cut_left(&format!("/{}", self.input.view()), w)),
            Overlay::Prompt => plain(cut_left(&format!("{}: {}", self.input_label, self.input.view()), w)),
            Overlay::Confirm => styled(cut_left(&format!("{} y/N", self.question), w), st.header),
            Overlay::Details if self.wifi.is_some() => {
                styled(cut("p password · any other key back · ↑↓ scroll", w), st.faint)
            }
            Overlay::Details | Overlay::Help => styled(cut("any key back · ↑↓ scroll", w), st.faint),
            Overlay::None if !self.status.is_empty() && self.status_err => {
                styled(cut(&format!("Error: {}", self.status), w), st.error)
            }
            Overlay::None if !self.status.is_empty() => plain(cut(&self.status, w)),
            Overlay::None => styled(cut(&self.hints(), w), st.faint),
        }
    }

    /// Lists the most useful keys first, because a narrow terminal cuts the
    /// line off.
    fn hints(&self) -> String {
        let mut hints: Vec<String> = Vec::new();
        let list = self.current();
        if let Some(filter) = list.map(|l| l.table.filter()).filter(|f| !f.is_empty()) {
            hints.push(format!("filter: {filter} (esc clears)"));
        }
        match self.screen {
            Screen::Loading => return "esc hosts · q quit".into(),
            Screen::Wizard => return "esc cancel".into(),
            Screen::Hosts => hints.extend(["enter open".to_string(), "a add".to_string()]),
            Screen::Sites => hints.push("enter open".into()),
            Screen::Main => hints.push("enter details".into()),
        }
        if let Some(l) = list {
            hints.extend(l.actions.iter().map(|a| format!("{} {}", a.key, a.label)));
        }
        hints.extend(["? help".to_string(), "/ filter".to_string()]);
        if self.screen == Screen::Main {
            let t = &self.tabs[self.tab];
            if t.alt.is_some() {
                hints.push(format!("B {}", t.other().hint));
            }
            let next = &self.tabs[(self.tab + 1) % self.tabs.len()];
            hints.push(format!("tab {}", next.active().title.to_lowercase()));
            hints.push("S sites".into());
        }
        if self.screen != Screen::Hosts {
            hints.push("H hosts".into());
        }
        hints.push("q quit".into());
        hints.join(" · ")
    }
}
