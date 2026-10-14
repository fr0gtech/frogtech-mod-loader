// Package icon draws the Frogtech frog icon (used for the exe resource, the
// window, the tray and the page favicon).
package icon

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/color"
	"image/png"
	"math"
	"sync"
)

// Sizes rendered into the .ico / exe icon resource.
var Sizes = []int{16, 24, 32, 48, 64, 256}

// Render draws the icon at the given pixel size.
func Render(size int) *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, size, size))
	green := color.NRGBA{0x4c, 0xc2, 0x5a, 0xff}
	dark := color.NRGBA{0x12, 0x1a, 0x14, 0xff}
	white := color.NRGBA{0xf2, 0xf5, 0xef, 0xff}
	f := float64(size)
	in := func(x, y, cx, cy, r float64) bool { return math.Hypot(x-cx, y-cy) <= r }
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			px, py := float64(x)+0.5, float64(y)+0.5
			var c color.NRGBA
			switch {
			case in(px, py, f*0.30, f*0.36, f*0.10) || in(px, py, f*0.70, f*0.36, f*0.10):
				c = dark // pupils
			case in(px, py, f*0.30, f*0.36, f*0.19) || in(px, py, f*0.70, f*0.36, f*0.19):
				c = white // eyes
			case in(px, py, f*0.5, f*0.6, f*0.40):
				c = green // head
			default:
				continue
			}
			img.SetNRGBA(x, y, c)
		}
	}
	return img
}

// ICO returns a .ico file with PNG-compressed images of the given sizes.
func ICO(sizes ...int) []byte {
	var pngs [][]byte
	for _, s := range sizes {
		var b bytes.Buffer
		png.Encode(&b, Render(s))
		pngs = append(pngs, b.Bytes())
	}
	var buf bytes.Buffer
	binary.Write(&buf, binary.LittleEndian, [3]uint16{0, 1, uint16(len(pngs))})
	offset := 6 + 16*len(pngs)
	for i, p := range pngs {
		s := byte(sizes[i]) // 256 is stored as 0
		buf.Write([]byte{s, s, 0, 0})
		binary.Write(&buf, binary.LittleEndian, uint16(1))
		binary.Write(&buf, binary.LittleEndian, uint16(32))
		binary.Write(&buf, binary.LittleEndian, uint32(len(p)))
		binary.Write(&buf, binary.LittleEndian, uint32(offset))
		offset += len(p)
	}
	for _, p := range pngs {
		buf.Write(p)
	}
	return buf.Bytes()
}

var favicon []byte
var faviconOnce sync.Once

// Favicon is the page's favicon (16, 32 and 48 px).
func Favicon() []byte {
	faviconOnce.Do(func() { favicon = ICO(16, 32, 48) })
	return favicon
}
