// Package qr draws QR codes with text characters, for terminals.
//
// A terminal cell is taller than it is wide, so one character can stand for
// several of the code's square modules. Two styles are offered: half blocks
// (one character is one module wide and two high), which every terminal
// font has, and sextants (two wide and three high), which need a font with
// the Unicode "Symbols for Legacy Computing" block but take a third fewer
// rows.
//
// The characters are drawn for the light modules. Shown as light ink on a
// dark background, which is what Style gives and what a dark terminal does
// by itself, the code has the usual polarity.
package qr

import (
	"fmt"
	"strings"

	rsc "rsc.io/qr"
)

// Code is an encoded QR code.
type Code struct {
	size  int
	black func(x, y int) bool
}

// Encode turns text into a QR code. It uses the lowest error correction
// level, which gives the smallest code; a screen is a clean medium.
func Encode(text string) (*Code, error) {
	c, err := rsc.Encode(text, rsc.L)
	if err != nil {
		return nil, fmt.Errorf("encode QR code: %w", err)
	}
	return &Code{size: c.Size, black: c.Black}, nil
}

// Size returns the number of modules along one side, without a margin.
func (c *Code) Size() int { return c.size }

// light reports whether the module at x, y of the drawing, which has a
// margin of quiet modules around the code, is light. Everything outside the
// code is light.
func (c *Code) light(x, y, quiet int) bool {
	x, y = x-quiet, y-quiet
	if x < 0 || y < 0 || x >= c.size || y >= c.size {
		return true
	}
	return !c.black(x, y)
}

// HalfBlocks draws the code with one character per module in width and per
// two modules in height. quiet is the margin in modules.
func (c *Code) HalfBlocks(quiet int) []string {
	total := c.size + 2*quiet
	glyphs := [4]rune{' ', '▀', '▄', '█'} // index: bit 0 = top light, bit 1 = bottom light
	var lines []string
	for y := 0; y < total; y += 2 {
		var b strings.Builder
		for x := range total {
			i := 0
			if c.light(x, y, quiet) {
				i |= 1
			}
			if y+1 >= total || c.light(x, y+1, quiet) {
				i |= 2
			}
			b.WriteRune(glyphs[i])
		}
		lines = append(lines, b.String())
	}
	return lines
}

// Sextants draws the code with one character per two modules in width and
// per three in height.
func (c *Code) Sextants(quiet int) []string {
	total := c.size + 2*quiet
	var lines []string
	for y := 0; y < total; y += 3 {
		var b strings.Builder
		for x := 0; x < total; x += 2 {
			var bits rune
			for i := range 6 {
				mx, my := x+i%2, y+i/2
				if mx >= total || my >= total || c.light(mx, my, quiet) {
					bits |= 1 << i
				}
			}
			b.WriteRune(sextant(bits))
		}
		lines = append(lines, b.String())
	}
	return lines
}

// sextant returns the character whose six cells (bit 0 top left, bit 1 top
// right, then the middle and bottom rows) are filled as in bits. Unicode has
// 60 dedicated sextant characters; the other four patterns are the space,
// the full block, and the two vertical half blocks.
func sextant(bits rune) rune {
	const (
		leftColumn  = 0b010101
		rightColumn = 0b101010
		full        = 0b111111
	)
	switch bits {
	case 0:
		return ' '
	case leftColumn:
		return '▌'
	case rightColumn:
		return '▐'
	case full:
		return '█'
	}
	r := 0x1FB00 + bits - 1
	if bits > leftColumn {
		r--
	}
	if bits > rightColumn {
		r--
	}
	return r
}

// Margins, in modules. The standard asks for four; two scan reliably on a
// screen, and one still works when space is short.
const (
	comfortableQuiet = 2
	minimalQuiet     = 1
)

// Fit draws the code as large and as plainly as the space allows: half
// blocks if they fit, else sextants, each with the wider margin if possible.
// It reports false when the code does not fit at all.
func (c *Code) Fit(width, height int) ([]string, bool) {
	for _, draw := range []func(int) []string{c.HalfBlocks, c.Sextants} {
		for _, quiet := range []int{comfortableQuiet, minimalQuiet} {
			lines := draw(quiet)
			if len(lines) <= height && len([]rune(lines[0])) <= width {
				return lines, true
			}
		}
	}
	return nil, false
}

// MinHeight returns the fewest rows the code can be drawn in.
func (c *Code) MinHeight() int { return len(c.Sextants(minimalQuiet)) }
