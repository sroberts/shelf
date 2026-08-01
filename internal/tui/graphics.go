package tui

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"os"
	"strings"

	"golang.org/x/image/draw"
)

// Terminal graphics.
//
// Three renderers, in descending fidelity: the kitty graphics protocol, sixel,
// and unicode half-blocks. Half-blocks are the floor rather than an apology --
// they work in every terminal, over ssh, and inside tmux, which is where a lot
// of this will actually run.

// GraphicsMode selects a rendering backend.
type GraphicsMode string

const (
	GraphicsNone   GraphicsMode = "none"
	GraphicsBlocks GraphicsMode = "ascii"
	GraphicsSixel  GraphicsMode = "sixel"
	GraphicsKitty  GraphicsMode = "kitty"
)

// DetectGraphics chooses a backend.
//
// pref comes from config ui.graphics and wins unless it is "auto". Detection is
// by environment only: querying the terminal means writing an escape sequence
// and reading the reply, which fights with Bubble Tea for the same file
// descriptor and can hang on a terminal that never answers.
func DetectGraphics(pref string) GraphicsMode {
	switch GraphicsMode(strings.ToLower(strings.TrimSpace(pref))) {
	case GraphicsNone:
		return GraphicsNone
	case GraphicsBlocks:
		return GraphicsBlocks
	case GraphicsSixel:
		return GraphicsSixel
	case GraphicsKitty:
		return GraphicsKitty
	}

	// Multiplexers need explicit passthrough configuration for graphics
	// protocols, and get it wrong more often than right. Degrade rather than
	// spray escape codes across someone's panes.
	if os.Getenv("TMUX") != "" || strings.HasPrefix(os.Getenv("TERM"), "screen") {
		return GraphicsBlocks
	}

	term := strings.ToLower(os.Getenv("TERM"))
	termProgram := strings.ToLower(os.Getenv("TERM_PROGRAM"))

	if os.Getenv("KITTY_WINDOW_ID") != "" || strings.Contains(term, "kitty") {
		return GraphicsKitty
	}
	switch termProgram {
	case "ghostty", "wezterm":
		return GraphicsKitty
	}
	if strings.Contains(term, "sixel") || termProgram == "mlterm" || term == "foot" {
		return GraphicsSixel
	}

	return GraphicsBlocks
}

// RenderCover renders an image into a block of terminal text.
//
// cols and rows are character cells. The image is scaled to fit while keeping
// its aspect ratio, assuming a cell is roughly twice as tall as it is wide.
func RenderCover(img image.Image, mode GraphicsMode, cols, rows int) string {
	if img == nil || mode == GraphicsNone || cols < 2 || rows < 2 {
		return ""
	}

	switch mode {
	case GraphicsKitty:
		return renderKitty(img, cols, rows)
	case GraphicsSixel:
		return renderSixel(img, cols, rows)
	default:
		return renderBlocks(img, cols, rows)
	}
}

// fitToCells scales an image to fit a character-cell box.
//
// pixelsPerCol and pixelsPerRow describe how many image pixels one cell should
// carry; the half-block renderer packs two vertical pixels per row, while the
// pixel protocols use a conventional cell size.
func fitToCells(img image.Image, cols, rows, pixelsPerCol, pixelsPerRow int) image.Image {
	b := img.Bounds()
	if b.Dx() == 0 || b.Dy() == 0 {
		return img
	}

	maxW := cols * pixelsPerCol
	maxH := rows * pixelsPerRow

	// Preserve aspect ratio.
	scale := float64(maxW) / float64(b.Dx())
	if s := float64(maxH) / float64(b.Dy()); s < scale {
		scale = s
	}
	if scale >= 1 {
		// Never upscale; a small cover stays small rather than turning to mush.
		scale = 1
	}

	w := int(float64(b.Dx()) * scale)
	h := int(float64(b.Dy()) * scale)
	if w < 1 {
		w = 1
	}
	if h < 1 {
		h = 1
	}

	dst := image.NewRGBA(image.Rect(0, 0, w, h))
	draw.CatmullRom.Scale(dst, dst.Bounds(), img, b, draw.Src, nil)
	return dst
}

// renderBlocks draws with the upper-half-block character.
//
// Each cell carries two vertical pixels: the foreground colors the top half,
// the background the bottom. That doubles the effective vertical resolution and
// needs nothing beyond 24-bit color.
func renderBlocks(img image.Image, cols, rows int) string {
	scaled := fitToCells(img, cols, rows, 1, 2)
	b := scaled.Bounds()

	var sb strings.Builder
	for y := b.Min.Y; y < b.Max.Y; y += 2 {
		for x := b.Min.X; x < b.Max.X; x++ {
			top := scaled.At(x, y)
			bottom := top
			if y+1 < b.Max.Y {
				bottom = scaled.At(x, y+1)
			}

			tr, tg, tb := rgb8(top)
			br, bg, bb := rgb8(bottom)
			fmt.Fprintf(&sb, "\x1b[38;2;%d;%d;%dm\x1b[48;2;%d;%d;%dm▀", tr, tg, tb, br, bg, bb)
		}
		sb.WriteString("\x1b[0m")
		if y+2 < b.Max.Y {
			sb.WriteString("\n")
		}
	}
	return sb.String()
}

// renderKitty emits the kitty graphics protocol: a base64 PNG in 4 KB chunks.
func renderKitty(img image.Image, cols, rows int) string {
	scaled := fitToCells(img, cols, rows, 8, 16)

	var buf bytes.Buffer
	if err := png.Encode(&buf, scaled); err != nil {
		return renderBlocks(img, cols, rows)
	}
	encoded := base64.StdEncoding.EncodeToString(buf.Bytes())

	const chunkSize = 4096
	var sb strings.Builder

	for i := 0; i < len(encoded); i += chunkSize {
		end := min(i+chunkSize, len(encoded))
		more := 0
		if end < len(encoded) {
			more = 1
		}

		if i == 0 {
			// a=T transmit and display, f=100 PNG, c/r bound the cell box.
			fmt.Fprintf(&sb, "\x1b_Ga=T,f=100,c=%d,r=%d,m=%d;%s\x1b\\",
				cols, rows, more, encoded[i:end])
		} else {
			fmt.Fprintf(&sb, "\x1b_Gm=%d;%s\x1b\\", more, encoded[i:end])
		}
	}
	return sb.String()
}

// renderSixel encodes the image as sixel data.
//
// Colors are quantized to the 6x6x6 cube, which is 216 entries and plenty for
// cover art at thumbnail size. A full median-cut palette would look marginally
// better and cost considerably more code.
func renderSixel(img image.Image, cols, rows int) string {
	scaled := fitToCells(img, cols, rows, 8, 16)
	b := scaled.Bounds()
	w, h := b.Dx(), b.Dy()
	if w == 0 || h == 0 {
		return ""
	}

	// Map every pixel to a palette index up front.
	indexed := make([]uint8, w*h)
	used := make(map[uint8]bool)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			r, g, bl := rgb8(scaled.At(b.Min.X+x, b.Min.Y+y))
			idx := uint8(r/43)*36 + uint8(g/43)*6 + uint8(bl/43)
			indexed[y*w+x] = idx
			used[idx] = true
		}
	}

	var sb strings.Builder
	// Enter sixel mode: P1 pixel aspect, P2=1 transparent background.
	fmt.Fprintf(&sb, "\x1bP0;1;0q\"1;1;%d;%d", w, h)

	// Declare only the colors actually present, as percentages 0-100.
	for idx := range used {
		r := int(idx/36) * 20
		g := int((idx/6)%6) * 20
		bl := int(idx%6) * 20
		fmt.Fprintf(&sb, "#%d;2;%d;%d;%d", idx, r, g, bl)
	}

	// Sixel works in bands of six pixel rows.
	for top := 0; top < h; top += 6 {
		first := true
		for idx := range used {
			// Build this color's bitmask for the band.
			line := make([]byte, w)
			any := false
			for x := 0; x < w; x++ {
				var bits byte
				for bit := 0; bit < 6; bit++ {
					y := top + bit
					if y < h && indexed[y*w+x] == idx {
						bits |= 1 << bit
					}
				}
				if bits != 0 {
					any = true
				}
				line[x] = bits + 0x3F // sixel chars start at '?'
			}
			if !any {
				continue
			}

			if !first {
				sb.WriteString("$") // carriage return within the band
			}
			first = false
			fmt.Fprintf(&sb, "#%d", idx)
			writeSixelRLE(&sb, line)
		}
		if top+6 < h {
			sb.WriteString("-") // next band
		}
	}

	sb.WriteString("\x1b\\")
	return sb.String()
}

// writeSixelRLE writes a sixel row with run-length compression, which matters:
// cover art has large flat regions and the uncompressed form is enormous.
func writeSixelRLE(sb *strings.Builder, line []byte) {
	for i := 0; i < len(line); {
		j := i
		for j < len(line) && line[j] == line[i] {
			j++
		}
		run := j - i

		// The !<count> form only pays for itself past three repeats.
		if run > 3 {
			fmt.Fprintf(sb, "!%d%c", run, line[i])
		} else {
			for k := 0; k < run; k++ {
				sb.WriteByte(line[i])
			}
		}
		i = j
	}
}

// rgb8 converts a color to 8-bit components.
func rgb8(c color.Color) (int, int, int) {
	r, g, b, _ := c.RGBA()
	return int(r >> 8), int(g >> 8), int(b >> 8)
}
