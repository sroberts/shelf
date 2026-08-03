package convert

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/gif"
	"image/jpeg"
	"image/png"
	"os"
	"path"
	"strings"

	xdraw "golang.org/x/image/draw" // higher-quality resampling kernels

	"github.com/sroberts/shelf/internal/epub"
)

// Device-targeted EPUB optimization.
//
// This mirrors what the device's built-in EPUB Optimizer does, but locally
// where there is CPU to spare (spec.md 9.2). The device is an ESP32-C3 with
// ~380 KB of usable RAM: a 3000px full-colour JPEG is not just slow to
// transfer, it is memory the firmware does not have.
//
// Two rules keep this safe to run over a library:
//
//  1. An image's container format never changes. A PNG stays a PNG and a JPEG
//     stays a JPEG, so the OPF manifest's media-type stays true and no package
//     document surgery is needed for the image path.
//  2. An entry is only replaced when the new bytes are actually smaller.
//     Re-encoding can grow a file — a well-optimized PNG round-tripped through
//     a generic encoder often does — and shipping a larger file to a
//     memory-constrained device to "optimize" it would be absurd.

// OptimizeResult reports what optimizing one book accomplished.
type OptimizeResult struct {
	Source     string   `json:"source"`
	Output     string   `json:"output"`
	Profile    string   `json:"profile"`
	SourceSize int64    `json:"source_size"`
	OutputSize int64    `json:"output_size"`
	Images     int      `json:"images"`
	Rewritten  int      `json:"images_rewritten"`
	Skipped    []string `json:"skipped,omitempty"`
}

// Saved returns the bytes the optimization removed. It can be zero, and never
// goes negative, because entries only get replaced when they shrink.
func (r OptimizeResult) Saved() int64 {
	if r.OutputSize >= r.SourceSize {
		return 0
	}
	return r.SourceSize - r.OutputSize
}

// imageExts maps the extensions shelf will re-encode to their format name.
//
// Keyed by extension rather than sniffed content because the extension is what
// the OPF media-type agrees with, and keeping those two consistent is the
// point. An entry whose bytes do not match its extension fails to decode and
// is passed through untouched, which is the correct outcome for the
// deliberately mislabelled covers real EPUB 2 files contain.
var imageExts = map[string]string{
	".png":  "png",
	".jpg":  "jpeg",
	".jpeg": "jpeg",
	".gif":  "gif",
}

// Optimize writes a device-targeted copy of the EPUB at src to dst.
//
// src and dst may be the same path; the underlying repack is atomic either way.
//
// Font stripping (spec.md 9.2) is deliberately not implemented here. Dropping a
// font file means removing its OPF manifest item and every @font-face rule that
// references it, and a partial job produces an EPUB that epubcheck rejects.
// Images are where the bytes are; fonts can come later, done properly.
func Optimize(src, dst string, p Profile) (*OptimizeResult, error) {
	srcSize, err := fileSize(src)
	if err != nil {
		return nil, err
	}

	res := &OptimizeResult{
		Source:     src,
		Output:     dst,
		Profile:    p.Name,
		SourceSize: srcSize,
	}

	err = epub.Repack(src, dst, func(e epub.Entry) ([]byte, error) {
		ext := strings.ToLower(path.Ext(e.Name))

		format, isImage := imageExts[ext]
		if !isImage {
			return nil, nil // copy through untouched
		}
		res.Images++

		body, err := e.Open()
		if err != nil {
			return nil, err
		}

		out, err := optimizeImage(body, format, p)
		if err != nil {
			// A single undecodable image must not fail the whole book. Pass it
			// through and report it, so the user learns which file was odd.
			res.Skipped = append(res.Skipped, fmt.Sprintf("%s: %v", e.Name, err))
			return nil, nil
		}
		if len(out) >= len(body) {
			// Rule 2: never grow an entry.
			return nil, nil
		}

		res.Rewritten++
		return out, nil
	})
	if err != nil {
		return nil, err
	}

	if res.OutputSize, err = fileSize(dst); err != nil {
		return nil, err
	}
	return res, nil
}

// optimizeImage decodes, transforms, and re-encodes a single image in place,
// returning bytes in the same container format it was given.
func optimizeImage(body []byte, format string, p Profile) ([]byte, error) {
	img, err := decodeImage(body, format)
	if err != nil {
		return nil, err
	}

	img = downscale(img, p)
	if p.Grayscale {
		img = toGray(img, p.Dither)
	}
	return encodeImage(img, format, p)
}

// decodeImage decodes using the codec the extension promises, rather than
// sniffing. A mismatch is reported, not silently corrected, because writing
// PNG bytes back into a file the OPF calls a JPEG would break the manifest.
func decodeImage(body []byte, format string) (image.Image, error) {
	r := bytes.NewReader(body)
	switch format {
	case "png":
		return png.Decode(r)
	case "jpeg":
		return jpeg.Decode(r)
	case "gif":
		return gif.Decode(r)
	}
	return nil, fmt.Errorf("unsupported image format %q", format)
}

// encodeImage re-encodes, dropping whatever metadata the source carried:
// neither encoder copies EXIF, ICC profiles, or comments, which is exactly the
// "drop metadata blobs" step and often the larger part of the saving on
// photographs.
func encodeImage(img image.Image, format string, p Profile) ([]byte, error) {
	var buf bytes.Buffer
	switch format {
	case "png":
		enc := png.Encoder{CompressionLevel: png.BestCompression}
		if err := enc.Encode(&buf, img); err != nil {
			return nil, err
		}
	case "jpeg":
		q := p.JPEGQuality
		if q <= 0 || q > 100 {
			q = 80
		}
		if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: q}); err != nil {
			return nil, err
		}
	case "gif":
		// image/gif writes paletted output; a grayscale ramp keeps the file
		// small without introducing colours the panel cannot show.
		if err := gif.Encode(&buf, img, &gif.Options{NumColors: 256}); err != nil {
			return nil, err
		}
	default:
		return nil, fmt.Errorf("unsupported image format %q", format)
	}
	return buf.Bytes(), nil
}

// downscale shrinks an image to fit the profile's panel, preserving aspect
// ratio. Images already small enough are returned untouched, and an image is
// never enlarged.
func downscale(img image.Image, p Profile) image.Image {
	if !p.KnowsPanelSize() {
		return img
	}

	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	if w <= p.MaxWidth && h <= p.MaxHeight {
		return img
	}

	// Scale by the tighter of the two ratios so both dimensions fit.
	sx := float64(p.MaxWidth) / float64(w)
	sy := float64(p.MaxHeight) / float64(h)
	s := sx
	if sy < s {
		s = sy
	}

	nw := max(1, int(float64(w)*s))
	nh := max(1, int(float64(h)*s))

	// CatmullRom keeps text edges legible at the large reductions typical
	// here; a box filter turns small type to mush.
	out := image.NewRGBA(image.Rect(0, 0, nw, nh))
	xdraw.CatmullRom.Scale(out, out.Bounds(), img, b, xdraw.Over, nil)
	return out
}

// toGray converts to 8-bit grayscale, optionally with error diffusion.
func toGray(img image.Image, dither bool) image.Image {
	b := img.Bounds()
	if !dither {
		if g, ok := img.(*image.Gray); ok {
			return g // already there; skip a full copy
		}
		out := image.NewGray(image.Rect(0, 0, b.Dx(), b.Dy()))
		draw.Draw(out, out.Bounds(), img, b.Min, draw.Src)
		return out
	}

	// Floyd-Steinberg over a 16-level ramp, matching the grey levels an e-ink
	// panel can actually hold. Done by hand because image/draw's ditherer only
	// targets a paletted image, and the output here must stay Gray.
	return ditherGray(img)
}

// grayLevels is the number of distinct greys the target panels display.
const grayLevels = 16

// ditherGray applies Floyd-Steinberg error diffusion to a grayscale ramp.
func ditherGray(img image.Image) *image.Gray {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	out := image.NewGray(image.Rect(0, 0, w, h))

	// Work in float to carry error between pixels without clipping early.
	buf := make([]float64, w*h)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			c := color.GrayModel.Convert(img.At(b.Min.X+x, b.Min.Y+y)).(color.Gray)
			buf[y*w+x] = float64(c.Y)
		}
	}

	step := 255.0 / float64(grayLevels-1)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			i := y*w + x
			old := buf[i]
			quant := clamp255(round(old/step) * step)
			out.SetGray(x, y, color.Gray{Y: uint8(quant)})

			err := old - quant
			// Standard Floyd-Steinberg weights: 7/16 right, 3/16 below-left,
			// 5/16 below, 1/16 below-right.
			spread(buf, w, h, x+1, y, err*7.0/16.0)
			spread(buf, w, h, x-1, y+1, err*3.0/16.0)
			spread(buf, w, h, x, y+1, err*5.0/16.0)
			spread(buf, w, h, x+1, y+1, err*1.0/16.0)
		}
	}
	return out
}

// fileSize returns the size of a file in bytes.
func fileSize(name string) (int64, error) {
	fi, err := os.Stat(name)
	if err != nil {
		return 0, fmt.Errorf("convert: %w", err)
	}
	return fi.Size(), nil
}

func spread(buf []float64, w, h, x, y int, err float64) {
	if x < 0 || x >= w || y < 0 || y >= h {
		return
	}
	buf[y*w+x] += err
}

func round(v float64) float64 {
	if v < 0 {
		return float64(int(v - 0.5))
	}
	return float64(int(v + 0.5))
}

func clamp255(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 255 {
		return 255
	}
	return v
}
