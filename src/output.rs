//! Turns devices, clients, and other results into text: the tables and JSON
//! the command line prints, and the formatted values the terminal UI shows.

use std::io::Write;
use std::sync::atomic::{AtomicBool, Ordering};

use chrono::{DateTime, Local, Utc};
use serde::Serialize;

use crate::omada::{Alert, Device, Firmware, NetClient, Ssid, SECURITY_PERSONAL};

/// Stands in for a value that is missing.
pub const NONE: &str = "-";

static SHOW_UTC: AtomicBool = AtomicBool::new(false);

/// Makes [`timestamp`] show UTC instead of local time, so that expected
/// output in tests does not depend on where they run.
pub fn show_times_in_utc() {
    SHOW_UTC.store(true, Ordering::SeqCst);
}

/// Formats a length of time, given in seconds, with its two largest units:
/// "41d 6h", "3h 12m", "5m", "42s".
pub fn seconds(s: f64) -> String {
    if s.is_nan() || s <= 0.0 {
        return NONE.into();
    }
    let total = s as u64;
    let (days, hours, minutes) = (total / 86400, total / 3600 % 24, total / 60 % 60);
    if days > 0 {
        format!("{days}d {hours}h")
    } else if hours > 0 {
        format!("{hours}h {minutes}m")
    } else if minutes > 0 {
        format!("{minutes}m")
    } else {
        format!("{total}s")
    }
}

/// Formats a device uptime. Controllers send it either as a count of
/// seconds or as text such as "41day(s) 6h 2m 1s"; both come out as
/// "41d 6h".
pub fn uptime(raw: &str) -> String {
    let raw = raw.trim();
    if raw.is_empty() {
        return NONE.into();
    }
    if let Ok(secs) = raw.parse::<f64>() {
        return seconds(secs);
    }
    let text = raw.replace("day(s)", "d");
    text.split_whitespace().take(2).collect::<Vec<_>>().join(" ")
}

/// Formats a byte count with a binary unit: "1.2 GB".
pub fn bytes(mut n: f64) -> String {
    if n.is_nan() || n <= 0.0 {
        return "0 B".into();
    }
    const UNITS: [&str; 6] = ["B", "KB", "MB", "GB", "TB", "PB"];
    let mut i = 0;
    while n >= 1024.0 && i < UNITS.len() - 1 {
        n /= 1024.0;
        i += 1;
    }
    if i == 0 || n >= 100.0 {
        format!("{n:.0} {}", UNITS[i])
    } else {
        format!("{n:.1} {}", UNITS[i])
    }
}

/// Formats a load value: "12%".
pub fn percent(v: f64) -> String {
    format!("{v:.0}%")
}

/// Returns `s`, or the placeholder for missing values when `s` is empty.
pub fn or_none(s: &str) -> String {
    if s.is_empty() {
        NONE.into()
    } else {
        s.into()
    }
}

/// Formats a moment in local time: "2026-11-23 01:10".
pub fn timestamp(t: Option<DateTime<Utc>>) -> String {
    const FORMAT: &str = "%Y-%m-%d %H:%M";
    match t {
        None => NONE.into(),
        Some(t) if SHOW_UTC.load(Ordering::SeqCst) => t.format(FORMAT).to_string(),
        Some(t) => t.with_timezone(&Local).format(FORMAT).to_string(),
    }
}

/// Formats a moment for JSON: RFC 3339, UTC, to the second.
pub fn rfc3339(t: DateTime<Utc>) -> String {
    t.format("%Y-%m-%dT%H:%M:%SZ").to_string()
}

/// Formats a flag.
pub fn yes_no(b: bool) -> String {
    if b {
        "yes".into()
    } else {
        "no".into()
    }
}

/// One labeled value of a details view.
#[derive(Debug, Clone, PartialEq)]
pub struct Field {
    pub label: String,
    pub value: String,
}

fn field(label: &str, value: impl Into<String>) -> Field {
    Field { label: label.into(), value: value.into() }
}

/// Lists everything worth showing about one device.
pub fn device_fields(d: &Device) -> Vec<Field> {
    let mut fields = vec![
        field("Name", or_none(&d.name)),
        field("Status", d.status_text()),
        field("Type", or_none(&d.kind)),
        field("Model", or_none(&d.model)),
        field("IP address", or_none(&d.ip)),
        field("MAC address", or_none(&d.mac)),
        field("Firmware", or_none(&d.firmware)),
        field("Serial", or_none(&d.serial)),
    ];
    if d.online() {
        fields.push(field("Uptime", uptime(&d.uptime)));
        fields.push(field("CPU", percent(d.cpu)));
        fields.push(field("Memory", percent(d.mem)));
    }
    fields
}

/// Formats a wireless signal strength, or "wired" for wired clients.
pub fn signal(c: &NetClient) -> String {
    if !c.wireless {
        "wired".into()
    } else if c.rssi == 0.0 {
        NONE.into()
    } else {
        format!("{:.0} dBm", c.rssi)
    }
}

/// Lists everything worth showing about one client.
pub fn client_fields(c: &NetClient) -> Vec<Field> {
    let mut fields = vec![
        field("Name", or_none(&c.name)),
        field("Host name", or_none(&c.host_name)),
        field("IP address", or_none(&c.ip)),
        field("MAC address", or_none(&c.mac)),
        field("Vendor", or_none(&c.vendor)),
        field("Connected to", or_none(&c.connected_to())),
        field("Signal", signal(c)),
    ];
    if !c.network_name.is_empty() {
        fields.push(field("Network", c.network_name.clone()));
    }
    fields.push(field("Downloaded", bytes(c.traffic_down)));
    fields.push(field("Uploaded", bytes(c.traffic_up)));
    fields.push(field("Uptime", seconds(c.uptime)));
    fields
}

/// Lists everything worth showing about one Wi-Fi network. `password` is
/// what to show as the password: the password itself, or a note that it is
/// hidden. It is left out for networks that have none.
pub fn ssid_fields(s: &Ssid, password: &str) -> Vec<Field> {
    let mut fields = vec![
        field("Name", or_none(&s.name)),
        field("Security", s.security_text()),
        field("Band", or_none(&s.band_text())),
        field("Guest network", yes_no(s.guest)),
        field("Hidden", yes_no(s.hidden())),
    ];
    if s.vlan_on {
        fields.push(field("VLAN", (s.vlan_id as i64).to_string()));
    }
    if s.security == SECURITY_PERSONAL {
        fields.push(field("Password", password));
    }
    fields
}

/// Says whether an alert is resolved.
pub fn alert_state(a: &Alert) -> &'static str {
    if a.resolved {
        "resolved"
    } else {
        "unresolved"
    }
}

/// Lists everything worth showing about one alert.
pub fn alert_fields(a: &Alert) -> Vec<Field> {
    vec![
        field("Time", timestamp(Some(a.time()))),
        field("Level", or_none(&a.level)),
        field("Module", or_none(&a.module)),
        field("State", alert_state(a)),
        field("Message", or_none(&a.content)),
    ]
}

/// Describes the newest known firmware next to the installed one: the
/// version with a note when it is an update, or that the device is up to
/// date.
pub fn latest_firmware(f: &Firmware) -> String {
    if f.update_available() {
        format!("{} (update available)", f.latest)
    } else if f.latest.is_empty() {
        "unknown".into()
    } else {
        "up to date".into()
    }
}

/// Lists the firmware state of one device.
pub fn firmware_fields(d: &Device, f: &Firmware) -> Vec<Field> {
    let installed = if f.current.is_empty() { &d.firmware } else { &f.current };
    let mut fields =
        vec![field("Device", d.label()), field("Installed", or_none(installed)), field("Latest", latest_firmware(f))];
    if f.update_available() && !f.release_notes.is_empty() {
        fields.push(field("Release notes", f.release_notes.clone()));
    }
    fields
}

/// How the command line prints results.
#[derive(Debug, Clone, Copy, PartialEq)]
pub enum Format {
    Table,
    Json,
}

/// Checks the value of the `--output` flag.
pub fn parse_format(s: &str) -> Result<Format, String> {
    match s.to_lowercase().as_str() {
        "table" => Ok(Format::Table),
        "json" => Ok(Format::Json),
        _ => Err(format!("unknown output format {s:?}; use table or json")),
    }
}

/// Writes rows as aligned columns under an upper-case header.
pub fn table(w: &mut dyn Write, header: &[&str], rows: &[Vec<String>]) -> std::io::Result<()> {
    let head: Vec<String> = header.iter().map(|h| h.to_uppercase()).collect();
    let lines: Vec<&Vec<String>> = std::iter::once(&head).chain(rows).collect();
    let mut widths = vec![0usize; header.len()];
    for line in &lines {
        for (i, cell) in line.iter().enumerate().take(widths.len()) {
            widths[i] = widths[i].max(cell.chars().count());
        }
    }
    for line in lines {
        let mut out = String::new();
        for (i, cell) in line.iter().enumerate() {
            out.push_str(cell);
            if i + 1 < line.len() {
                let pad = widths.get(i).copied().unwrap_or(0) + 2 - cell.chars().count();
                out.push_str(&" ".repeat(pad));
            }
        }
        writeln!(w, "{out}")?;
    }
    Ok(())
}

/// Writes labeled values, one per line, with the values aligned. The
/// further lines of a value that has several are indented to match.
pub fn fields(w: &mut dyn Write, fields: &[Field]) -> std::io::Result<()> {
    let width = fields.iter().map(|f| f.label.chars().count() + 1).max().unwrap_or(0);
    for f in fields {
        let label = format!("{}:", f.label);
        let pad = " ".repeat(width - label.chars().count() + 2);
        let value = f.value.trim_end_matches('\n').replace('\n', &format!("\n{}", " ".repeat(width + 2)));
        writeln!(w, "{label}{pad}{value}")?;
    }
    Ok(())
}

/// Writes `v` as indented JSON.
pub fn json<T: Serialize>(w: &mut dyn Write, v: &T) -> std::io::Result<()> {
    serde_json::to_writer_pretty(&mut *w, v)?;
    writeln!(w)
}

/// Serializes a number the way scripts expect it: whole numbers without a
/// fraction (8, not 8.0).
pub fn ser_num<S: serde::Serializer>(v: &f64, s: S) -> Result<S::Ok, S::Error> {
    if v.fract() == 0.0 && v.abs() < 9e15 {
        s.serialize_i64(*v as i64)
    } else {
        s.serialize_f64(*v)
    }
}

/// Reports whether any cell contains the filter text, ignoring case. An
/// empty filter matches everything.
pub fn matches(cells: &[String], filter: &str) -> bool {
    if filter.is_empty() {
        return true;
    }
    let filter = filter.to_lowercase();
    cells.iter().any(|c| c.to_lowercase().contains(&filter))
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::omada::*;
    use chrono::TimeZone;

    fn value(fields: &[Field], label: &str) -> String {
        fields.iter().find(|f| f.label == label).map(|f| f.value.clone()).unwrap_or_default()
    }

    #[test]
    fn durations() {
        for (secs, want) in [
            (0.0, "-"),
            (-1.0, "-"),
            (42.0, "42s"),
            (303.0, "5m"),
            (11520.0, "3h 12m"),
            (41.0 * 86400.0 + 6.0 * 3600.0, "41d 6h"),
            (86400.0, "1d 0h"),
            (3600.0, "1h 0m"),
            (0.5, "0s"),
            (3599.0, "59m"),
            (f64::NAN, "-"),
        ] {
            assert_eq!(seconds(secs), want, "{secs}");
        }
    }

    #[test]
    fn uptimes() {
        for (raw, want) in [
            ("", "-"),
            ("  ", "-"),
            ("3600", "1h 0m"),
            ("1044000", "12d 2h"),
            ("41day(s) 6h 2m 1s", "41d 6h"),
            ("6h 2m 1s", "6h 2m"),
            ("2m", "2m"),
        ] {
            assert_eq!(uptime(raw), want, "{raw:?}");
        }
    }

    #[test]
    fn byte_counts() {
        for (n, want) in [
            (0.0, "0 B"),
            (-5.0, "0 B"),
            (512.0, "512 B"),
            (1024.0, "1.0 KB"),
            (1536.0, "1.5 KB"),
            (4194304.0, "4.0 MB"),
            (1288490188.0, "1.2 GB"),
            (94489280512.0, "88.0 GB"),
            (107374182400.0, "100 GB"),
        ] {
            assert_eq!(bytes(n), want, "{n}");
        }
    }

    #[test]
    fn signals() {
        assert_eq!(signal(&NetClient { wireless: true, rssi: -52.0, ..Default::default() }), "-52 dBm");
        assert_eq!(signal(&NetClient::default()), "wired");
        assert_eq!(signal(&NetClient { wireless: true, ..Default::default() }), "-");
    }

    #[test]
    fn device_details() {
        let online = device_fields(&Device {
            name: "AP".into(),
            status: STATUS_CONNECTED,
            uptime: "3600".into(),
            cpu: 5.0,
            ..Default::default()
        });
        assert_eq!((value(&online, "Uptime").as_str(), value(&online, "CPU").as_str()), ("1h 0m", "5%"));
        let offline = device_fields(&Device { name: "AP".into(), ..Default::default() });
        assert_eq!(value(&offline, "Uptime"), "", "no load fields for an offline device");
        assert_eq!((value(&offline, "Status").as_str(), value(&offline, "Model").as_str()), ("offline", "-"));
    }

    #[test]
    fn client_details() {
        let c = NetClient {
            name: "NAS".into(),
            switch_name: "Rack switch".into(),
            port: 4.0,
            traffic_down: 1024.0,
            uptime: 60.0,
            network_name: "LAN".into(),
            ..Default::default()
        };
        let f = client_fields(&c);
        assert_eq!(value(&f, "Connected to"), "Rack switch · port 4");
        assert_eq!(value(&f, "Signal"), "wired");
        assert_eq!(value(&f, "Downloaded"), "1.0 KB");
        assert_eq!(value(&f, "Uptime"), "1m");
        assert_eq!(value(&f, "Network"), "LAN");
    }

    #[test]
    fn ssid_details() {
        let personal = ssid_fields(
            &Ssid {
                name: "IoT".into(),
                security: SECURITY_PERSONAL,
                band: BAND_2G,
                broadcast: Some(false),
                vlan_on: true,
                vlan_id: 30.0,
                ..Default::default()
            },
            "s3cret",
        );
        assert_eq!(value(&personal, "Password"), "s3cret");
        assert_eq!(value(&personal, "Hidden"), "yes");
        assert_eq!(value(&personal, "VLAN"), "30");
        assert_eq!(value(&personal, "Band"), "2.4 GHz");
        let open = ssid_fields(&Ssid { name: "Guests".into(), guest: true, ..Default::default() }, "ignored");
        assert_eq!(value(&open, "Password"), "", "no password field for an open network");
        assert_eq!(value(&open, "VLAN"), "");
        assert_eq!(value(&open, "Guest network"), "yes");
    }

    #[test]
    fn firmware_details() {
        let d = Device { name: "AP office".into(), firmware: "1.0.14".into(), ..Default::default() };
        let fw = |c: &str, l: &str, n: &str| Firmware { current: c.into(), latest: l.into(), release_notes: n.into() };
        let update = firmware_fields(&d, &fw("1.0.14", "1.1.2", "Faster."));
        assert_eq!(value(&update, "Latest"), "1.1.2 (update available)");
        assert_eq!(value(&update, "Release notes"), "Faster.");
        let same = firmware_fields(&d, &fw("1.0.14", "1.0.14", "old"));
        assert_eq!((value(&same, "Latest").as_str(), value(&same, "Release notes").as_str()), ("up to date", ""));
        let unknown = firmware_fields(&d, &Firmware::default());
        assert_eq!(value(&unknown, "Latest"), "unknown");
        assert_eq!(value(&unknown, "Installed"), "1.0.14", "the version from the device list is the fallback");
    }

    #[test]
    fn alert_details() {
        let mut a = Alert { level: "Error".into(), content: "port 7 down".into(), millis: 1.0, ..Default::default() };
        let f = alert_fields(&a);
        assert_eq!((value(&f, "State").as_str(), value(&f, "Message").as_str()), ("unresolved", "port 7 down"));
        a.resolved = true;
        assert_eq!(alert_state(&a), "resolved");
    }

    #[test]
    fn tables_align() {
        let mut buf = Vec::new();
        table(
            &mut buf,
            &["Name", "ip"],
            &[vec!["Core router".into(), "192.168.0.1".into()], vec!["AP".into(), "192.168.0.21".into()]],
        )
        .unwrap();
        assert_eq!(
            String::from_utf8(buf).unwrap(),
            "NAME         IP\nCore router  192.168.0.1\nAP           192.168.0.21\n"
        );
    }

    #[test]
    fn fields_align_and_indent_further_lines() {
        let mut buf = Vec::new();
        fields(
            &mut buf,
            &[field("Latest", "1.1.2"), field("Release notes", "1. Faster.\n2. Safer.\n"), field("Büro", "x")],
        )
        .unwrap();
        assert_eq!(
            String::from_utf8(buf).unwrap(),
            "Latest:         1.1.2\nRelease notes:  1. Faster.\n                2. Safer.\nBüro:           x\n"
        );
    }

    #[test]
    fn json_is_indented_and_not_html_escaped() {
        #[derive(Serialize)]
        struct V {
            name: &'static str,
            #[serde(serialize_with = "ser_num")]
            whole: f64,
            #[serde(serialize_with = "ser_num")]
            part: f64,
        }
        let mut buf = Vec::new();
        json(&mut buf, &V { name: "A & B <x>", whole: 8.0, part: 12.5 }).unwrap();
        assert_eq!(
            String::from_utf8(buf).unwrap(),
            "{\n  \"name\": \"A & B <x>\",\n  \"whole\": 8,\n  \"part\": 12.5\n}\n"
        );
    }

    #[test]
    fn formats() {
        assert_eq!(parse_format("table"), Ok(Format::Table));
        assert_eq!(parse_format("JSON"), Ok(Format::Json));
        assert!(parse_format("yaml").is_err());
    }

    #[test]
    fn filter_matching() {
        let cells = vec!["AP office".to_string(), "192.168.0.21".into(), "ap".into()];
        for (filter, want) in [("", true), ("OFFICE", true), ("0.21", true), ("switch", false)] {
            assert_eq!(matches(&cells, filter), want, "{filter}");
        }
    }

    #[test]
    fn timestamps() {
        show_times_in_utc();
        assert_eq!(timestamp(None), "-");
        let at = Utc.with_ymd_and_hms(2026, 11, 23, 1, 10, 59).unwrap();
        assert_eq!(timestamp(Some(at)), "2026-11-23 01:10");
        assert_eq!(rfc3339(at), "2026-11-23T01:10:59Z");
    }
}
