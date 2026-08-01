package epub

import (
	"bytes"
	"fmt"
	"image"
	"path"
	"strings"

	"golang.org/x/image/draw"

	"image/png"

	// Decoders for the other formats that appear as EPUB cover art.
	_ "image/gif"
	_ "image/jpeg"

	_ "golang.org/x/image/webp"
)

// DefaultThumbnailWidth is the width of cover thumbnails stored in the index.
const DefaultThumbnailWidth = 256

// Cover images larger than this are almost certainly not cover images.
const maxCoverSize = 64 << 20

// CoverPath returns the archive-relative path of the cover image.
//
// Resolution order: the EPUB 3 cover-image property, then the EPUB 2
// <meta name="cover"> manifest pointer, then a name-based guess. The guess
// matters because a large share of real EPUBs declare neither.
func (f *File) CoverPath() (string, error) {
	for _, it := range f.pkg.Manifest {
		if it.hasProperty("cover-image") && it.Href != "" {
			return f.resolveHref(it.Href), nil
		}
	}

	if id := f.pkg.CoverID; id != "" {
		for _, it := range f.pkg.Manifest {
			if it.ID == id && it.Href != "" {
				return f.resolveHref(it.Href), nil
			}
		}
	}

	// Last resort: an image whose id or filename says "cover".
	for _, it := range f.pkg.Manifest {
		if !strings.HasPrefix(it.MediaType, "image/") || it.Href == "" {
			continue
		}
		base := strings.ToLower(path.Base(it.Href))
		if strings.Contains(strings.ToLower(it.ID), "cover") || strings.Contains(base, "cover") {
			return f.resolveHref(it.Href), nil
		}
	}
	return "", ErrNoCover
}

// CoverImage decodes the cover image.
func (f *File) CoverImage() (image.Image, error) {
	p, err := f.CoverPath()
	if err != nil {
		return nil, err
	}

	data, err := f.readEntry(p, maxCoverSize)
	if err != nil {
		// A manifest that points at a missing file is common enough in the
		// wild that it should read as "no cover", not as a corrupt archive.
		return nil, fmt.Errorf("%w: declared cover %s is missing", ErrNoCover, p)
	}

	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("%w: decode %s: %v", ErrNoCover, p, err)
	}
	return img, nil
}

// CoverThumbnailPNG returns the cover downscaled to width, encoded as PNG. It
// never upscales: a cover smaller than the target is returned at its own size.
func (f *File) CoverThumbnailPNG(width int) ([]byte, error) {
	img, err := f.CoverImage()
	if err != nil {
		return nil, err
	}
	if width <= 0 {
		width = DefaultThumbnailWidth
	}
	return encodePNG(Thumbnail(img, width))
}

// Thumbnail scales img to the given width, preserving aspect ratio. Images
// already narrower than width are returned unchanged.
func Thumbnail(img image.Image, width int) image.Image {
	b := img.Bounds()
	if b.Dx() <= width || b.Dx() == 0 {
		return img
	}

	height := int(float64(b.Dy()) * float64(width) / float64(b.Dx()))
	if height < 1 {
		height = 1
	}

	dst := image.NewRGBA(image.Rect(0, 0, width, height))
	// CatmullRom is slower than the alternatives but cover art is downscaled
	// once at import and then looked at repeatedly.
	draw.CatmullRom.Scale(dst, dst.Bounds(), img, b, draw.Src, nil)
	return dst
}

// encodePNG encodes an image as PNG with maximum compression, since thumbnails
// are written once into the index and read many times.
func encodePNG(img image.Image) ([]byte, error) {
	var buf bytes.Buffer
	enc := png.Encoder{CompressionLevel: png.BestCompression}
	if err := enc.Encode(&buf, img); err != nil {
		return nil, fmt.Errorf("encode cover thumbnail: %w", err)
	}
	return buf.Bytes(), nil
}
