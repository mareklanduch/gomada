# 0006. The interface stays plain; tables scroll sideways

## Context

gomada should be usable on anything from a wide desktop terminal to a pocket
device such as the Cardputer Zero. Layouts that rearrange themselves per
screen size are a lot of code to write and to test.

## Decision

One list on screen at a time, one line of key hints, no side-by-side panels,
no mouse. Every screen is the same table component. When the columns do not
fit, `h` and `l` (or the arrow keys) scroll one column at a time while the
name column stays in place. Details open as a full-screen page.

Every action is a single letter. Status is shown by shape (`●` `◐` `○`) as
well as by color, so the view reads without color.

## Consequences

- No view has size-specific code; sideways scrolling is built once.
- A tab is a table definition plus a list of actions, so adding one (Wi-Fi
  networks, logs) does not touch the rest.
- On a very narrow screen a wide column, such as a MAC address, can be cut
  off even when scrolled to. The details page shows every value in full.
