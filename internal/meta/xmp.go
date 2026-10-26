package meta

import (
	"fmt"
	"math"
	"strings"
)

// XMP — содержимое сайдкара со всеми полями Info.
func XMP(info Info) string {
	var b strings.Builder
	b.WriteString(`<?xpacket begin="` + "\ufeff" + `" id="W5M0MpCehiHzreSzNTczkc9d"?>
<x:xmpmeta xmlns:x="adobe:ns:meta/" x:xmptk="icloud-export">
 <rdf:RDF xmlns:rdf="http://www.w3.org/1999/02/22-rdf-syntax-ns#">
  <rdf:Description rdf:about=""
    xmlns:xmp="http://ns.adobe.com/xap/1.0/"
    xmlns:photoshop="http://ns.adobe.com/photoshop/1.0/"
    xmlns:exif="http://ns.adobe.com/exif/1.0/"
    xmlns:dc="http://purl.org/dc/elements/1.1/"`)
	attr := func(name, val string) { fmt.Fprintf(&b, "\n    %s=\"%s\"", name, esc(val)) }
	if !info.Date.IsZero() {
		d := info.Date.Format("2006-01-02T15:04:05-07:00")
		attr("xmp:CreateDate", d)
		attr("photoshop:DateCreated", d)
		attr("exif:DateTimeOriginal", d)
	}
	if l := info.Location; l != nil {
		attr("exif:GPSLatitude", gpsCoord(l.Lat, "N", "S"))
		attr("exif:GPSLongitude", gpsCoord(l.Lon, "E", "W"))
		if l.Alt != nil {
			attr("exif:GPSAltitude", fmt.Sprintf("%d/10", int64(math.Round(math.Abs(*l.Alt)*10))))
			attr("exif:GPSAltitudeRef", map[bool]string{true: "0", false: "1"}[*l.Alt >= 0])
		}
	}
	if info.Favorite {
		attr("xmp:Rating", "5")
	}
	b.WriteString(">")
	if info.Caption != "" {
		fmt.Fprintf(&b, "\n   <dc:description><rdf:Alt><rdf:li xml:lang=\"x-default\">%s</rdf:li></rdf:Alt></dc:description>", esc(info.Caption))
	}
	if len(info.Keywords) > 0 {
		b.WriteString("\n   <dc:subject><rdf:Bag>")
		for _, k := range info.Keywords {
			fmt.Fprintf(&b, "<rdf:li>%s</rdf:li>", esc(k))
		}
		b.WriteString("</rdf:Bag></dc:subject>")
	}
	b.WriteString("\n  </rdf:Description>\n </rdf:RDF>\n</x:xmpmeta>\n<?xpacket end=\"w\"?>\n")
	return b.String()
}

// gpsCoord — формат XMP «DDD,MM.mmmmmmK».
func gpsCoord(v float64, pos, neg string) string {
	ref := pos
	if v < 0 {
		ref, v = neg, -v
	}
	deg := math.Floor(v)
	return fmt.Sprintf("%d,%.6f%s", int(deg), (v-deg)*60, ref)
}

var xmlEsc = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;")

func esc(s string) string { return xmlEsc.Replace(s) }
