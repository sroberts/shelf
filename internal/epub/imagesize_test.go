package epub

import (
	"bytes"
	"encoding/binary"
	"errors"
	"hash/crc32"
	"image"
	"image/png"
	"testing"
)

// pngHeader is the start of a PNG claiming w x h: enough for DecodeConfig,
// and all a decompression bomb needs to declare.
func pngHeader(w, h uint32) []byte {
	var buf bytes.Buffer
	buf.WriteString("\x89PNG\r\n\x1a\n")
	ihdr := make([]byte, 13)
	binary.BigEndian.PutUint32(ihdr[0:], w)
	binary.BigEndian.PutUint32(ihdr[4:], h)
	ihdr[8], ihdr[9] = 8, 0 // 8-bit greyscale
	binary.Write(&buf, binary.BigEndian, uint32(len(ihdr)))
	chunk := append([]byte("IHDR"), ihdr...)
	buf.Write(chunk)
	binary.Write(&buf, binary.BigEndian, crc32.ChecksumIEEE(chunk))
	return buf.Bytes()
}

func TestCheckImageSizeRefusesABomb(t *testing.T) {
	if err := CheckImageSize(pngHeader(60000, 60000)); !errors.Is(err, ErrImageTooLarge) {
		t.Errorf("60000x60000: %v, want ErrImageTooLarge", err)
	}
	var ok bytes.Buffer
	png.Encode(&ok, image.NewGray(image.Rect(0, 0, 600, 900)))
	if err := CheckImageSize(ok.Bytes()); err != nil {
		t.Errorf("an ordinary cover was refused: %v", err)
	}
}
