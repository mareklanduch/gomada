package qr_test

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/mareklanduch/gomada/internal/qr"
)

const payload = "WIFI:T:WPA;S:Home-5G;P:correct horse battery;;"

func encode(t *testing.T) *qr.Code {
	t.Helper()
	c, err := qr.Encode(payload)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// decodeHalf turns a half-block drawing back into a grid of light modules.
func decodeHalf(lines []string) [][]bool {
	var grid [][]bool
	for _, line := range lines {
		top, bottom := []bool{}, []bool{}
		for _, r := range line {
			top = append(top, r == '▀' || r == '█')
			bottom = append(bottom, r == '▄' || r == '█')
		}
		grid = append(grid, top, bottom)
	}
	return grid
}

// decodeSextant turns a sextant drawing back into a grid of light modules.
func decodeSextant(t *testing.T, lines []string) [][]bool {
	t.Helper()
	bitsOf := map[rune]int{' ': 0, '▌': 0b010101, '▐': 0b101010, '█': 0b111111}
	n := 0
	for bits := 1; bits < 63; bits++ {
		if bits == 0b010101 || bits == 0b101010 {
			continue
		}
		bitsOf[rune(0x1FB00+n)] = bits
		n++
	}
	if n != 60 {
		t.Fatalf("built %d sextant characters, want 60", n)
	}
	var grid [][]bool
	for _, line := range lines {
		rows := [3][]bool{}
		for _, r := range line {
			bits, ok := bitsOf[r]
			if !ok {
				t.Fatalf("unexpected character %q (%U)", r, r)
			}
			for i := range 6 {
				rows[i/2] = append(rows[i/2], bits&(1<<i) != 0)
			}
		}
		grid = append(grid, rows[0], rows[1], rows[2])
	}
	return grid
}

// modules extracts the code itself from a decoded drawing with a margin.
func modules(grid [][]bool, quiet, size int) string {
	var b strings.Builder
	for y := quiet; y < quiet+size; y++ {
		for x := quiet; x < quiet+size; x++ {
			if grid[y][x] {
				b.WriteByte('.')
			} else {
				b.WriteByte('#')
			}
		}
		b.WriteByte('\n')
	}
	return b.String()
}

// Both drawings must show exactly the same modules; a single wrong module
// can make a code unreadable.
func TestHalfBlocksAndSextantsShowTheSameCode(t *testing.T) {
	c := encode(t)
	for _, quiet := range []int{1, 2, 4} {
		half := modules(decodeHalf(c.HalfBlocks(quiet)), quiet, c.Size())
		sext := modules(decodeSextant(t, c.Sextants(quiet)), quiet, c.Size())
		if half != sext {
			t.Fatalf("quiet %d: the two drawings differ\nhalf blocks:\n%s\nsextants:\n%s", quiet, half, sext)
		}
		if !strings.Contains(half, "#") || !strings.Contains(half, ".") {
			t.Fatal("the code must have dark and light modules")
		}
	}
}

func TestFinderPatternAndQuietZone(t *testing.T) {
	c := encode(t)
	got := modules(decodeHalf(c.HalfBlocks(2)), 2, c.Size())
	rows := strings.Split(got, "\n")
	// Every QR code starts with a 7x7 finder pattern in the top left corner.
	want := []string{"#######", "#.....#", "#.###.#", "#.###.#", "#.###.#", "#.....#", "#######"}
	for i, w := range want {
		if rows[i][:7] != w {
			t.Errorf("finder pattern row %d = %q, want %q", i, rows[i][:7], w)
		}
	}
	// The margin is all light, which is drawn as full ink.
	lines := c.HalfBlocks(2)
	if strings.Trim(lines[0], "█") != "" {
		t.Errorf("first line = %q, want only full blocks (the quiet zone)", lines[0])
	}
	for i, line := range lines {
		r := []rune(line)
		if r[0] != '█' || r[1] != '█' || r[len(r)-1] != '█' {
			t.Errorf("line %d does not start and end with the quiet zone: %q", i, line)
		}
	}
}

func TestDimensions(t *testing.T) {
	c := encode(t)
	size := c.Size()
	if size < 21 || size%4 != 1 {
		t.Fatalf("size = %d, not a valid QR code size", size)
	}
	half := c.HalfBlocks(1)
	if len(half) != (size+2+1)/2 || len([]rune(half[0])) != size+2 {
		t.Errorf("half blocks: %d lines of %d, want %d of %d", len(half), len([]rune(half[0])), (size+2+1)/2, size+2)
	}
	sext := c.Sextants(1)
	if len(sext) != (size+2+2)/3 || len([]rune(sext[0])) != (size+2+1)/2 {
		t.Errorf("sextants: %d lines of %d", len(sext), len([]rune(sext[0])))
	}
	if c.MinHeight() != len(sext) {
		t.Errorf("MinHeight = %d, want %d", c.MinHeight(), len(sext))
	}
	// Each character must take exactly one terminal cell.
	for _, line := range append(half, sext...) {
		if w, n := ansi.StringWidth(line), len([]rune(line)); w != n {
			t.Fatalf("line of %d characters is %d cells wide: %q", n, w, line)
		}
	}
}

func TestFit(t *testing.T) {
	c := encode(t)
	halfRoomy, halfTight := c.HalfBlocks(2), c.HalfBlocks(1)
	sextRoomy, sextTight := c.Sextants(2), c.Sextants(1)
	width := len([]rune(halfRoomy[0]))

	tests := []struct {
		name          string
		width, height int
		want          []string
	}{
		{"plenty of room", 200, 100, halfRoomy},
		{"exactly half blocks with the wide margin", width, len(halfRoomy), halfRoomy},
		{"one row short: narrow margin", width, len(halfTight), halfTight},
		{"too short for half blocks", width, len(halfTight) - 1, sextRoomy},
		{"too narrow for sextants with the wide margin", len([]rune(sextTight[0])), 100, sextTight},
		{"too narrow for half blocks", len([]rune(halfTight[0])) - 1, 100, sextRoomy},
	}
	for _, tt := range tests {
		got, ok := c.Fit(tt.width, tt.height)
		if !ok || strings.Join(got, "\n") != strings.Join(tt.want, "\n") {
			t.Errorf("%s: got %d lines (fits %v), want %d lines", tt.name, len(got), ok, len(tt.want))
		}
	}
	if _, ok := c.Fit(width, len(sextTight)-1); ok {
		t.Error("a code must not be reported as fitting when it is one row too tall")
	}
	if _, ok := c.Fit(len([]rune(sextTight[0]))-1, 100); ok {
		t.Error("a code must not be reported as fitting when it is one column too wide")
	}
}

func TestEncodeTooLong(t *testing.T) {
	if _, err := qr.Encode(strings.Repeat("x", 5000)); err == nil {
		t.Error("want an error for text that does not fit in a QR code")
	}
}
