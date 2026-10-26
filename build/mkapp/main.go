// mkapp собирает icloud-export.app: Info.plist, бинарник, иконку (рисуется здесь же).
package main

import (
	"bytes"
	"encoding/binary"
	"flag"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io"
	"log"
	"math"
	"os"
	"path/filepath"
)

const infoPlist = `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>CFBundleExecutable</key><string>icloud-export</string>
	<key>CFBundleIdentifier</key><string>local.icloud-export</string>
	<key>CFBundleName</key><string>iCloud Export</string>
	<key>CFBundleDisplayName</key><string>iCloud Export</string>
	<key>CFBundleIconFile</key><string>icon</string>
	<key>CFBundlePackageType</key><string>APPL</string>
	<key>CFBundleShortVersionString</key><string>%[1]s</string>
	<key>CFBundleVersion</key><string>%[1]s</string>
	<key>CFBundleDevelopmentRegion</key><string>ru</string>
	<key>LSMinimumSystemVersion</key><string>13.0</string>
	<key>LSApplicationCategoryType</key><string>public.app-category.photography</string>
	<key>NSHighResolutionCapable</key><true/>
	<key>NSAppTransportSecurity</key>
	<dict><key>NSAllowsLocalNetworking</key><true/></dict>
</dict>
</plist>
`

func main() {
	bin := flag.String("bin", "dist/icloud-export", "собранный бинарник darwin")
	out := flag.String("out", "dist/icloud-export.app", "путь к .app")
	version := flag.String("version", "0.0.0", "версия")
	flag.Parse()

	if err := os.RemoveAll(*out); err != nil {
		log.Fatal(err)
	}
	macos := filepath.Join(*out, "Contents", "MacOS")
	res := filepath.Join(*out, "Contents", "Resources")
	for _, d := range []string{macos, res} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			log.Fatal(err)
		}
	}
	must(os.WriteFile(filepath.Join(*out, "Contents", "Info.plist"), fmt.Appendf(nil, infoPlist, *version), 0o644))
	must(copyFile(*bin, filepath.Join(macos, "icloud-export"), 0o755))
	must(os.WriteFile(filepath.Join(res, "icon.icns"), icns(drawIcon(1024)), 0o644))
}

func must(err error) {
	if err != nil {
		log.Fatal(err)
	}
}

func copyFile(src, dst string, mode os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// drawIcon — синяя плашка macOS-формы с белым облаком и стрелкой вниз (4×4 SSAA).
func drawIcon(size int) *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, size, size))
	s := float64(size)
	const ss = 4
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			var r, g, b, a float64
			for sy := 0; sy < ss; sy++ {
				for sx := 0; sx < ss; sx++ {
					px := (float64(x) + (float64(sx)+0.5)/ss) / s
					py := (float64(y) + (float64(sy)+0.5)/ss) / s
					cr, cg, cb, ca := shade(px, py)
					r, g, b, a = r+cr*ca, g+cg*ca, b+cb*ca, a+ca
				}
			}
			n := float64(ss * ss)
			if a > 0 {
				img.SetNRGBA(x, y, color.NRGBA{uint8(r / a * 255), uint8(g / a * 255), uint8(b / a * 255), uint8(a / n * 255)})
			}
		}
	}
	return img
}

// shade — цвет в точке (0..1), альфа 0 — вне иконки.
func shade(x, y float64) (r, g, b, a float64) {
	// плашка: скруглённый квадрат с полями как у системных иконок
	const m, rad = 0.1, 0.2
	if !roundRect(x, y, m, m, 1-m, 1-m, rad) {
		return 0, 0, 0, 0
	}
	t := (y - m) / (1 - 2*m)
	r, g, b = lerp(0.36, 0.05, t), lerp(0.72, 0.42, t), lerp(1.0, 0.93, t)

	// облако: три круга и основание
	cloud := circle(x, y, 0.38, 0.53, 0.13) || circle(x, y, 0.53, 0.45, 0.17) || circle(x, y, 0.66, 0.55, 0.11) ||
		roundRect(x, y, 0.25, 0.5, 0.77, 0.66, 0.08)
	if cloud {
		r, g, b = 1, 1, 1
		// стрелка вниз цветом плашки
		arrow := (x > 0.485 && x < 0.555 && y > 0.43 && y < 0.58) || triangle(x, y, 0.43, 0.56, 0.61, 0.56, 0.52, 0.65)
		if arrow {
			r, g, b = 0.12, 0.5, 0.96
		}
	}
	return r, g, b, 1
}

func lerp(a, b, t float64) float64 { return a + (b-a)*math.Max(0, math.Min(1, t)) }

func circle(x, y, cx, cy, r float64) bool { return (x-cx)*(x-cx)+(y-cy)*(y-cy) <= r*r }

func roundRect(x, y, x0, y0, x1, y1, r float64) bool {
	if x < x0 || x > x1 || y < y0 || y > y1 {
		return false
	}
	cx := math.Max(x0+r, math.Min(x, x1-r))
	cy := math.Max(y0+r, math.Min(y, y1-r))
	return circle(x, y, cx, cy, r)
}

func triangle(x, y, ax, ay, bx, by, cx, cy float64) bool {
	d := func(px, py, qx, qy, rx, ry float64) float64 { return (px-rx)*(qy-ry) - (qx-rx)*(py-ry) }
	d1, d2, d3 := d(x, y, ax, ay, bx, by), d(x, y, bx, by, cx, cy), d(x, y, cx, cy, ax, ay)
	neg := d1 < 0 || d2 < 0 || d3 < 0
	pos := d1 > 0 || d2 > 0 || d3 > 0
	return !(neg && pos)
}

// downscale — усреднение блоками (размеры кратны).
func downscale(src *image.NRGBA, size int) *image.NRGBA {
	k := src.Bounds().Dx() / size
	dst := image.NewNRGBA(image.Rect(0, 0, size, size))
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			var r, g, b, a float64
			for yy := 0; yy < k; yy++ {
				for xx := 0; xx < k; xx++ {
					c := src.NRGBAAt(x*k+xx, y*k+yy)
					al := float64(c.A)
					r, g, b, a = r+float64(c.R)*al, g+float64(c.G)*al, b+float64(c.B)*al, a+al
				}
			}
			if a > 0 {
				dst.SetNRGBA(x, y, color.NRGBA{uint8(r / a), uint8(g / a), uint8(b / a), uint8(a / float64(k*k))})
			}
		}
	}
	return dst
}

// icns — контейнер с PNG нужных размеров.
func icns(big *image.NRGBA) []byte {
	entries := []struct {
		typ  string
		size int
	}{{"ic11", 32}, {"ic12", 64}, {"ic07", 128}, {"ic13", 256}, {"ic08", 256}, {"ic14", 512}, {"ic09", 512}, {"ic10", 1024}}
	var body bytes.Buffer
	for _, e := range entries {
		img := big
		if e.size != big.Bounds().Dx() {
			img = downscale(big, e.size)
		}
		var p bytes.Buffer
		must(png.Encode(&p, img))
		body.WriteString(e.typ)
		_ = binary.Write(&body, binary.BigEndian, uint32(p.Len()+8))
		body.Write(p.Bytes())
	}
	var out bytes.Buffer
	out.WriteString("icns")
	_ = binary.Write(&out, binary.BigEndian, uint32(body.Len()+8))
	out.Write(body.Bytes())
	return out.Bytes()
}
