# 0008. Wi-Fi and alerts are tabs; firmware belongs to the device

## Context

Version 0.2 added Wi-Fi networks with a QR code, alerts, firmware updates,
and unblocking clients. Each needed a place in the interface, and some of
the data is slower or riskier to handle than a device list.

## Decision

**Tabs are for things you look at and act on.** Wi-Fi and Alerts are tabs
next to Devices and Clients. A settings area is reserved for changing how
the network is configured, which gomada does not do yet. Firmware is not a
tab: it is a column and an action of the device it belongs to. Blocked
clients are a second view of the Clients tab, switched with `B`, because a
blocked client is not connected and so has no row among the connected ones.

**The tab bar scrolls like the tables.** When the tab names do not fit, the
bar shows the current tab with its neighbors and markers for the rest. The
host and site names are shortened before the current tab is.

**Slow data arrives after the rows.** Firmware information costs one request
per device. The device rows are shown first; the firmware information is
fetched afterwards, kept for ten minutes, and the rows are refreshed once it
is there. The alert count in the tab bar is kept current in the background.

**What the controller does not offer is worked around in one place.**
- It has no list of blocked clients, so the list of known clients is read
  and filtered.
- An alert does not say whether it is resolved, so resolved and unresolved
  alerts are asked for separately.

**Care scales with the risk.**
- A firmware update asks for the device name to be typed, not just `y`. It
  is one device at a time, and never starts by itself.
- A Wi-Fi password is fetched only when asked for, is never logged, and is
  dropped from memory when its screen closes.
- Alerts are read-only.

**The QR code is drawn as light ink on black.** That has the right polarity
whatever the terminal's colors are. It uses half-block characters when the
window is tall enough and the more compact sextant characters when it is
not. Below about 15 rows it does not fit, and the password is shown instead.

## Consequences

- On a very small screen, such as a Cardputer Zero, there is no QR code.
  Painting pixels directly to the screen would make it possible, at the
  price of Linux-only code that CI cannot test; it was left out.
- The Open API application needs more permissions than before for the new
  features. Each feature fails by itself with the controller's message when
  a permission is missing; the others keep working.
