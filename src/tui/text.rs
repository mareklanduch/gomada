//! Measuring, cutting, and wrapping text by terminal cells.

use unicode_width::{UnicodeWidthChar, UnicodeWidthStr};

/// The number of terminal cells a string takes.
pub fn width(s: &str) -> usize {
    UnicodeWidthStr::width(s)
}

/// Shortens a string to at most `max` cells, ending in `tail` when
/// something was cut off.
pub fn truncate(s: &str, max: usize, tail: &str) -> String {
    if width(s) <= max {
        return s.to_string();
    }
    let Some(room) = max.checked_sub(width(tail)) else {
        return String::new();
    };
    let mut out = String::new();
    let mut used = 0;
    for c in s.chars() {
        let w = c.width().unwrap_or(0);
        if used + w > room {
            break;
        }
        used += w;
        out.push(c);
    }
    out + tail
}

/// Shortens a line to the width of the terminal.
pub fn cut(s: &str, max: usize) -> String {
    truncate(s, max, "…")
}

/// Shortens a line from the left, so the end, where the person is typing,
/// stays visible.
pub fn cut_left(s: &str, max: usize) -> String {
    let total = width(s);
    if total <= max {
        return s.to_string();
    }
    if max == 0 {
        return String::new();
    }
    let drop = total - max + 1;
    let mut dropped = 0;
    let rest: String = s
        .chars()
        .skip_while(|c| {
            let skip = dropped < drop;
            if skip {
                dropped += c.width().unwrap_or(0);
            }
            skip
        })
        .collect();
    format!("…{rest}")
}

/// Cuts or pads a line to exactly `w` cells.
pub fn fit(s: &str, w: usize) -> String {
    let mut line = truncate(s, w, "");
    line.push_str(&" ".repeat(w - width(&line)));
    line
}

/// Wraps text to `max` cells per line, breaking at spaces. Line breaks in
/// the text are kept. A word longer than a line is split.
pub fn wrap(text: &str, max: usize) -> Vec<String> {
    let max = max.max(1);
    let mut lines = Vec::new();
    for paragraph in text.split('\n') {
        let mut line = String::new();
        for word in paragraph.split(' ') {
            let mut word = word.to_string();
            let sep = usize::from(!line.is_empty());
            if width(&line) + sep + width(&word) <= max {
                if sep == 1 {
                    line.push(' ');
                }
                line.push_str(&word);
                continue;
            }
            if !line.is_empty() {
                lines.push(std::mem::take(&mut line));
            }
            while width(&word) > max {
                let head = truncate(&word, max, "");
                if head.is_empty() {
                    break; // a character wider than the line
                }
                word = word[head.len()..].to_string();
                lines.push(head);
            }
            line = word;
        }
        lines.push(line);
    }
    lines
}

/// Markers showing that there are more tabs to either side.
const TABS_BEFORE: &str = "‹ ";
const TABS_AFTER: &str = " ›";

/// Draws the tab titles in at most `max` cells. The current tab is in
/// brackets. When they do not all fit, the bar shows a run of neighboring
/// tabs that includes the current one, preferring the tabs after it, with
/// markers for the tabs left out on either side.
pub fn tab_bar(titles: &[String], current: usize, max: usize) -> String {
    let segment = |i: usize| {
        if i == current {
            format!("[{}]", titles[i])
        } else {
            format!(" {} ", titles[i])
        }
    };
    let draw = |lo: usize, hi: usize| {
        let mut s = (lo..=hi).map(segment).collect::<Vec<_>>().join(" ");
        if lo > 0 {
            s = format!("{TABS_BEFORE}{s}");
        }
        if hi < titles.len() - 1 {
            s.push_str(TABS_AFTER);
        }
        s
    };
    for size in (1..=titles.len()).rev() {
        let first = current.min(titles.len() - size);
        let last = (current + 1).saturating_sub(size);
        for lo in (last..=first).rev() {
            let s = draw(lo, lo + size - 1);
            if width(&s) <= max {
                return s;
            }
        }
    }
    cut(&segment(current), max)
}

/// The space the bar needs to show the current tab whole.
pub fn tab_bar_min_width(titles: &[String], current: usize) -> usize {
    let mut w = width(&titles[current]) + 2;
    if current > 0 {
        w += width(TABS_BEFORE);
    }
    if current < titles.len() - 1 {
        w += width(TABS_AFTER);
    }
    w
}

/// Makes the first letter a capital.
pub fn upper_first(s: &str) -> String {
    let mut chars = s.chars();
    match chars.next() {
        Some(first) => first.to_uppercase().chain(chars).collect(),
        None => String::new(),
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn truncating() {
        assert_eq!(truncate("hello", 10, "…"), "hello");
        assert_eq!(truncate("hello", 5, "…"), "hello");
        assert_eq!(truncate("hello", 4, "…"), "hel…");
        assert_eq!(truncate("hello", 1, "…"), "…");
        assert_eq!(truncate("hello", 0, "…"), "");
        assert_eq!(truncate("hello", 3, ""), "hel");
        // Wide characters are never split.
        assert_eq!(truncate("日本語", 5, "…"), "日本…");
        assert_eq!(truncate("日本語", 4, ""), "日本");
        assert_eq!(truncate("日本語", 3, ""), "日");
    }

    #[test]
    fn cutting_from_the_left_keeps_the_end() {
        assert_eq!(cut_left("Name: abcdef█", 20), "Name: abcdef█");
        assert_eq!(cut_left("Name: abcdef█", 6), "…cdef█");
        assert_eq!(width(&cut_left("Name: abcdef█", 6)), 6);
        assert_eq!(cut_left("abc", 0), "");
    }

    #[test]
    fn fitting() {
        assert_eq!(fit("ab", 4), "ab  ");
        assert_eq!(fit("abcdef", 4), "abcd");
        assert_eq!(fit("", 0), "");
    }

    #[test]
    fn wrapping() {
        assert_eq!(wrap("one two three", 7), ["one two", "three"]);
        assert_eq!(wrap("one two three", 100), ["one two three"]);
        assert_eq!(wrap("a\n\nb", 10), ["a", "", "b"]);
        assert_eq!(wrap("abcdefghij", 4), ["abcd", "efgh", "ij"]);
        assert_eq!(wrap("x abcdefghij y", 4), ["x", "abcd", "efgh", "ij y"]);
        assert_eq!(wrap("", 10), [""]);
        assert_eq!(wrap("sha256:00ff", 0).len(), 11, "a width of zero is treated as one");
    }

    fn titles() -> Vec<String> {
        ["Devices", "Clients", "Wi-Fi", "Alerts (2)"].map(String::from).to_vec()
    }

    #[test]
    fn tab_bar_shows_all_tabs_when_they_fit() {
        assert_eq!(tab_bar(&titles(), 0, 80), "[Devices]  Clients   Wi-Fi   Alerts (2) ");
        assert_eq!(tab_bar(&titles(), 2, 80), " Devices   Clients  [Wi-Fi]  Alerts (2) ");
    }

    #[test]
    fn tab_bar_scrolls_when_space_is_short() {
        assert_eq!(tab_bar(&titles(), 0, 31), "[Devices]  Clients   Wi-Fi  ›");
        assert_eq!(tab_bar(&titles(), 0, 12), "[Devices] ›");
        assert_eq!(tab_bar(&titles(), 2, 34), "‹  Clients  [Wi-Fi]  Alerts (2) ");
        assert_eq!(tab_bar(&titles(), 3, 14), "‹ [Alerts (2)]");
        assert_eq!(tab_bar(&titles(), 1, 13), "‹ [Clients] ›");
        // Not even the current tab fits: it is cut.
        assert_eq!(tab_bar(&titles(), 3, 8), "[Alerts…");
        for (i, _) in titles().iter().enumerate() {
            for w in 0..60 {
                assert!(width(&tab_bar(&titles(), i, w)) <= w, "tab {i} at width {w}");
            }
        }
    }

    #[test]
    fn tab_bar_minimum() {
        assert_eq!(tab_bar_min_width(&titles(), 0), 11);
        assert_eq!(tab_bar_min_width(&titles(), 1), 13);
        assert_eq!(tab_bar_min_width(&titles(), 3), 14);
    }

    #[test]
    fn first_letter() {
        assert_eq!(upper_first("already up to date"), "Already up to date");
        assert_eq!(upper_first(""), "");
    }
}
