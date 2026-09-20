// Draws the application icon: a rounded blue tile carrying a playlist and a
// note. Geometry is written in a 1024-wide space and scaled to each size.
package main

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"math"
	"os"
	"path/filepath"
)

const base = 1024.0

type pt struct{ x, y float64 }

// ---------- shapes -------------------------------------------------------

func roundedRect(p pt, x0, y0, x1, y1, r float64) bool {
	cx := math.Max(x0+r, math.Min(p.x, x1-r))
	cy := math.Max(y0+r, math.Min(p.y, y1-r))
	dx, dy := p.x-cx, p.y-cy
	return dx*dx+dy*dy <= r*r
}

func ellipse(p pt, cx, cy, rx, ry, deg float64) bool {
	a := deg * math.Pi / 180
	dx, dy := p.x-cx, p.y-cy
	ux := dx*math.Cos(a) + dy*math.Sin(a)
	uy := -dx*math.Sin(a) + dy*math.Cos(a)
	return (ux*ux)/(rx*rx)+(uy*uy)/(ry*ry) <= 1
}

// taperedCurve is a quadratic Bezier stroked with a width that shrinks from
// w0 to w1, which is what gives the note flag its tip.
type taperedCurve struct {
	pts     []pt
	half    []float64
	bx0     float64
	by0     float64
	bx1     float64
	by1     float64
	maxHalf float64
}

func newCurve(p0, c, p1 pt, w0, w1 float64) *taperedCurve {
	const steps = 240
	tc := &taperedCurve{maxHalf: math.Max(w0, w1) / 2}
	tc.bx0, tc.by0 = math.Inf(1), math.Inf(1)
	tc.bx1, tc.by1 = math.Inf(-1), math.Inf(-1)
	for i := 0; i <= steps; i++ {
		t := float64(i) / steps
		u := 1 - t
		x := u*u*p0.x + 2*u*t*c.x + t*t*p1.x
		y := u*u*p0.y + 2*u*t*c.y + t*t*p1.y
		tc.pts = append(tc.pts, pt{x, y})
		tc.half = append(tc.half, (w0+(w1-w0)*t*t)/2)
		tc.bx0, tc.by0 = math.Min(tc.bx0, x), math.Min(tc.by0, y)
		tc.bx1, tc.by1 = math.Max(tc.bx1, x), math.Max(tc.by1, y)
	}
	return tc
}

func (tc *taperedCurve) hit(p pt) bool {
	if p.x < tc.bx0-tc.maxHalf || p.x > tc.bx1+tc.maxHalf ||
		p.y < tc.by0-tc.maxHalf || p.y > tc.by1+tc.maxHalf {
		return false
	}
	for i, q := range tc.pts {
		dx, dy := p.x-q.x, p.y-q.y
		h := tc.half[i]
		if dx*dx+dy*dy <= h*h {
			return true
		}
	}
	return false
}

// ---------- the icon -----------------------------------------------------

var flag = newCurve(pt{800, 322}, pt{1000, 402}, pt{862, 626}, 80, 16)

// mark reports whether the point is part of the white foreground.
func mark(p pt, simple bool) bool {
	// Note head, stem and flag.
	if ellipse(p, 646, 706, 122, 95, -20) {
		return true
	}
	if roundedRect(p, 740, 300, 798, 720, 29) {
		return true
	}
	if flag.hit(p) {
		return true
	}
	if simple {
		return false
	}

	// Playlist lines, shortening as they go down.
	for i, w := range []float64{500, 412, 324} {
		y := 262 + float64(i)*152
		if roundedRect(p, 150, y, 150+w, y+74, 37) {
			return true
		}
	}
	return false
}

func lerp(a, b, t float64) float64 { return a + (b-a)*t }

func background(p pt) color.RGBA {
	t := math.Min(1, math.Max(0, (p.x*0.42+p.y*0.58)/base))
	return color.RGBA{
		R: uint8(lerp(0x74, 0x31, t)),
		G: uint8(lerp(0x9d, 0x40, t)),
		B: uint8(lerp(0xff, 0xc4, t)),
		A: 255,
	}
}

func draw(size int) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, size, size))
	simple := size <= 40
	scale := base / float64(size)
	radius := 230.0
	if simple {
		radius = 200
	}

	const sub = 4
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			var r, g, b, a float64
			for sy := 0; sy < sub; sy++ {
				for sx := 0; sx < sub; sx++ {
					p := pt{
						(float64(x) + (float64(sx)+0.5)/sub) * scale,
						(float64(y) + (float64(sy)+0.5)/sub) * scale,
					}
					if !roundedRect(p, 6, 6, base-6, base-6, radius) {
						continue
					}
					c := background(p)
					if mark(p, simple) {
						c = color.RGBA{0xff, 0xff, 0xff, 0xff}
					}
					r += float64(c.R)
					g += float64(c.G)
					b += float64(c.B)
					a++
				}
			}
			if a == 0 {
				continue
			}
			alpha := a / (sub * sub)
			// Premultiplied: the colour average is over covered samples only.
			img.SetRGBA(x, y, color.RGBA{
				R: uint8(r / a * alpha),
				G: uint8(g / a * alpha),
				B: uint8(b / a * alpha),
				A: uint8(alpha * 255),
			})
		}
	}
	return img
}

// ---------- output -------------------------------------------------------

func writePNG(path string, img image.Image) error {
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return err
	}
	return os.WriteFile(path, buf.Bytes(), 0o644)
}

// writeICO packs the images as PNG entries, which every Windows since Vista
// reads and which keeps the file small.
func writeICO(path string, images []*image.RGBA) error {
	var body [][]byte
	for _, img := range images {
		var buf bytes.Buffer
		if err := png.Encode(&buf, img); err != nil {
			return err
		}
		body = append(body, buf.Bytes())
	}

	var out bytes.Buffer
	binary.Write(&out, binary.LittleEndian, uint16(0))
	binary.Write(&out, binary.LittleEndian, uint16(1))
	binary.Write(&out, binary.LittleEndian, uint16(len(body)))

	offset := 6 + 16*len(body)
	for i, data := range body {
		size := images[i].Bounds().Dx()
		dim := byte(size)
		if size >= 256 {
			dim = 0
		}
		out.WriteByte(dim)
		out.WriteByte(dim)
		out.WriteByte(0) // colours in palette
		out.WriteByte(0) // reserved
		binary.Write(&out, binary.LittleEndian, uint16(1))
		binary.Write(&out, binary.LittleEndian, uint16(32))
		binary.Write(&out, binary.LittleEndian, uint32(len(data)))
		binary.Write(&out, binary.LittleEndian, uint32(offset))
		offset += len(data)
	}
	for _, data := range body {
		out.Write(data)
	}
	return os.WriteFile(path, out.Bytes(), 0o644)
}

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: icon <build dir>")
		os.Exit(1)
	}
	dir := os.Args[1]

	big := draw(1024)
	if err := writePNG(filepath.Join(dir, "appicon.png"), big); err != nil {
		panic(err)
	}

	var icons []*image.RGBA
	for _, size := range []int{256, 128, 64, 48, 32, 16} {
		icons = append(icons, draw(size))
	}
	if err := writeICO(filepath.Join(dir, "windows", "icon.ico"), icons); err != nil {
		panic(err)
	}
	fmt.Println("wrote appicon.png and windows/icon.ico")
}
