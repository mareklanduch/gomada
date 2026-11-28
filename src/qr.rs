//! Draws QR codes with text characters, for terminals.
//!
//! A terminal cell is taller than it is wide, so one character can stand
//! for several of the code's square modules. Two styles are offered: half
//! blocks (one character is one module wide and two high), which every
//! terminal font has, and sextants (two wide and three high), which need a
//! font with the Unicode "Symbols for Legacy Computing" block but take a
//! third fewer rows.
//!
//! The characters are drawn for the light modules. Shown as light ink on a
//! dark background the code has the usual polarity.

use qrcode::{Color, EcLevel, QrCode};

/// An encoded QR code.
#[derive(Debug, Clone, PartialEq)]
pub struct Code {
    size: usize,
    dark: Vec<bool>, // row by row
}

/// Margins, in modules. The standard asks for four; two scan reliably on a
/// screen, and one still works when space is short.
const COMFORTABLE_QUIET: usize = 2;
const MINIMAL_QUIET: usize = 1;

impl Code {
    /// Turns text into a QR code. It uses the lowest error correction
    /// level, which gives the smallest code; a screen is a clean medium.
    pub fn encode(text: &str) -> Result<Code, String> {
        let code = QrCode::with_error_correction_level(text, EcLevel::L).map_err(|e| format!("encode QR code: {e}"))?;
        let dark = code.to_colors().into_iter().map(|c| c == Color::Dark).collect();
        Ok(Code { size: code.width(), dark })
    }

    /// Builds a code from its modules, for tests.
    pub fn from_modules(size: usize, dark: Vec<bool>) -> Code {
        assert_eq!(dark.len(), size * size);
        Code { size, dark }
    }

    /// Returns the number of modules along one side, without a margin.
    pub fn size(&self) -> usize {
        self.size
    }

    /// Reports whether the module at x, y of the drawing, which has a
    /// margin of quiet modules around the code, is light. Everything
    /// outside the code is light.
    fn light(&self, x: usize, y: usize, quiet: usize) -> bool {
        if x < quiet || y < quiet {
            return true;
        }
        let (x, y) = (x - quiet, y - quiet);
        x >= self.size || y >= self.size || !self.dark[y * self.size + x]
    }

    /// Draws the code with one character per module in width and per two
    /// modules in height. `quiet` is the margin in modules.
    pub fn half_blocks(&self, quiet: usize) -> Vec<String> {
        let total = self.size + 2 * quiet;
        const GLYPHS: [char; 4] = [' ', '▀', '▄', '█']; // bit 0 = top light, bit 1 = bottom light
        (0..total)
            .step_by(2)
            .map(|y| {
                (0..total)
                    .map(|x| {
                        let top = self.light(x, y, quiet) as usize;
                        let bottom = (y + 1 >= total || self.light(x, y + 1, quiet)) as usize;
                        GLYPHS[top | bottom << 1]
                    })
                    .collect()
            })
            .collect()
    }

    /// Draws the code with one character per two modules in width and per
    /// three in height.
    pub fn sextants(&self, quiet: usize) -> Vec<String> {
        let total = self.size + 2 * quiet;
        (0..total)
            .step_by(3)
            .map(|y| {
                (0..total)
                    .step_by(2)
                    .map(|x| {
                        let mut bits = 0u32;
                        for i in 0..6 {
                            let (mx, my) = (x + i % 2, y + i / 2);
                            if mx >= total || my >= total || self.light(mx, my, quiet) {
                                bits |= 1 << i;
                            }
                        }
                        sextant(bits)
                    })
                    .collect()
            })
            .collect()
    }

    /// Draws the code as large and as plainly as the space allows: half
    /// blocks if they fit, else sextants, each with the wider margin if
    /// possible. `None` means the code does not fit at all.
    pub fn fit(&self, width: usize, height: usize) -> Option<Vec<String>> {
        let draws: [fn(&Code, usize) -> Vec<String>; 2] = [Code::half_blocks, Code::sextants];
        for draw in draws {
            for quiet in [COMFORTABLE_QUIET, MINIMAL_QUIET] {
                let lines = draw(self, quiet);
                if lines.len() <= height && lines[0].chars().count() <= width {
                    return Some(lines);
                }
            }
        }
        None
    }

    /// Returns the fewest rows the code can be drawn in.
    pub fn min_height(&self) -> usize {
        self.sextants(MINIMAL_QUIET).len()
    }
}

/// Returns the character whose six cells (bit 0 top left, bit 1 top right,
/// then the middle and bottom rows) are filled as in `bits`. Unicode has 60
/// dedicated sextant characters; the other four patterns are the space, the
/// full block, and the two vertical half blocks.
fn sextant(bits: u32) -> char {
    const LEFT_COLUMN: u32 = 0b010101;
    const RIGHT_COLUMN: u32 = 0b101010;
    const FULL: u32 = 0b111111;
    match bits {
        0 => ' ',
        LEFT_COLUMN => '▌',
        RIGHT_COLUMN => '▐',
        FULL => '█',
        _ => {
            let mut r = 0x1FB00 + bits - 1;
            if bits > LEFT_COLUMN {
                r -= 1;
            }
            if bits > RIGHT_COLUMN {
                r -= 1;
            }
            char::from_u32(r).unwrap_or(' ')
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    /// A 3x3 code with a dark diagonal.
    fn diagonal() -> Code {
        Code::from_modules(3, vec![true, false, false, false, true, false, false, false, true])
    }

    #[test]
    fn half_blocks_draw_the_light_modules() {
        // Past the bottom edge counts as light.
        assert_eq!(diagonal().half_blocks(0), ["▄▀█", "██▄"]);
        assert_eq!(diagonal().half_blocks(1), ["█▀███", "██▄▀█", "█████"]);
    }

    #[test]
    fn sextants_draw_the_light_modules() {
        // Top-left cell: modules (0,0) dark, (1,0) light, (0,1) light,
        // (1,1) dark, (0,2) light, (1,2) light -> bits 0b110110.
        let lines = diagonal().sextants(0);
        assert_eq!(lines.len(), 1);
        let first = lines[0].chars().next().unwrap();
        assert_eq!(first as u32, 0x1FB00 + 0b110110 - 1 - 2);
        assert_eq!(lines[0].chars().count(), 2);
    }

    #[test]
    fn sextant_characters() {
        assert_eq!(sextant(0), ' ');
        assert_eq!(sextant(0b111111), '█');
        assert_eq!(sextant(0b010101), '▌');
        assert_eq!(sextant(0b101010), '▐');
        assert_eq!(sextant(1) as u32, 0x1FB00);
        assert_eq!(sextant(0b111110) as u32, 0x1FB3B);
        // Every pattern has its own character.
        let all: std::collections::HashSet<char> = (0..64).map(sextant).collect();
        assert_eq!(all.len(), 64);
    }

    #[test]
    fn a_real_code_has_finder_patterns_and_fits() {
        let code = Code::encode("WIFI:T:WPA;S:Home-5G;P:correct horse battery;;").unwrap();
        assert_eq!(code.size(), 29, "version 3 at the lowest error correction level");
        // The top-left finder pattern: a dark 7x7 ring.
        for i in 0..7 {
            assert!(!code.light(i, 0, 0) && !code.light(0, i, 0) && !code.light(i, 6, 0) && !code.light(6, i, 0));
        }
        assert!(code.light(1, 1, 0) && !code.light(3, 3, 0));

        let half = code.fit(60, 24).unwrap();
        assert_eq!((half.len(), half[0].chars().count()), (17, 33), "half blocks with a margin of two");
        let tight = code.fit(60, 16).unwrap();
        assert_eq!((tight.len(), tight[0].chars().count()), (16, 31), "half blocks with a margin of one");
        let small = code.fit(60, 12).unwrap();
        assert_eq!((small.len(), small[0].chars().count()), (11, 17), "sextants");
        assert!(code.fit(60, 10).is_none(), "sextants with a margin of one need 11 rows too");
        assert_eq!(code.fit(16, 11).unwrap()[0].chars().count(), 16, "sextants with a margin of one");
        assert!(code.fit(10, 40).is_none());
        assert_eq!(code.min_height(), 11);
    }

    #[test]
    fn text_that_is_too_long_is_refused() {
        assert!(Code::encode(&"x".repeat(5000)).is_err());
    }
}
