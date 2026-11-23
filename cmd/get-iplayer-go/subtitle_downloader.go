package main

import (
	"encoding/binary"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
)

// ttmlDoc is the top-level TTML document structure
type ttmlDoc struct {
	XMLName   xml.Name   `xml:"tt"`
	FrameRate string     `xml:"frameRate,attr"`
	Head      ttmlHead   `xml:"head"`
	Body      ttmlBody   `xml:"body"`
}

type ttmlHead struct {
	Styling ttmlStyling `xml:"styling"`
}

type ttmlStyling struct {
	Styles []ttmlStyle `xml:"style"`
}

type ttmlStyle struct {
	ID    string `xml:"id,attr"`
	Color string `xml:"color,attr"`
}

type ttmlBody struct {
	Style string    `xml:"style,attr"`
	Divs  []ttmlDiv `xml:"div"`
}

type ttmlDiv struct {
	Style string     `xml:"style,attr"`
	Paras []ttmlPara `xml:"p"`
}

type ttmlPara struct {
	Begin string `xml:"begin,attr"`
	End   string `xml:"end,attr"`
	Style string `xml:"style,attr"`
	Color string `xml:"color,attr"`
	// Raw inner XML is collected via custom UnmarshalXML
	Nodes []ttmlNode
}

// ttmlNode represents a child of <p>: plain text, <br/>, or <span>
type ttmlNode struct {
	Kind  string // "text", "br", "span"
	Text  string
	Color string
	// span children
	Children []ttmlNode
}

// UnmarshalXML for ttmlPara collects child nodes manually so we can
// distinguish text nodes, <br/> elements, and <span> elements.
func (p *ttmlPara) UnmarshalXML(d *xml.Decoder, start xml.StartElement) error {
	for _, attr := range start.Attr {
		switch attr.Name.Local {
		case "begin":
			p.Begin = attr.Value
		case "end":
			p.End = attr.Value
		case "style":
			p.Style = attr.Value
		case "color":
			p.Color = attr.Value
		}
	}
	p.Nodes = collectNodes(d, start.Name)
	return nil
}

// collectNodes reads tokens until the closing tag matching endName,
// returning a flat slice of ttmlNodes.
func collectNodes(d *xml.Decoder, endName xml.Name) []ttmlNode {
	var nodes []ttmlNode
	for {
		tok, err := d.Token()
		if err != nil {
			break
		}
		switch t := tok.(type) {
		case xml.CharData:
			text := string(t)
			if strings.TrimSpace(text) != "" {
				nodes = append(nodes, ttmlNode{Kind: "text", Text: text})
			}
		case xml.StartElement:
			switch t.Name.Local {
			case "br":
				nodes = append(nodes, ttmlNode{Kind: "br"})
				d.Skip() // consume </br> or self-close
			case "span":
				spanColor := ""
				spanStyle := ""
				for _, attr := range t.Attr {
					switch attr.Name.Local {
					case "color":
						spanColor = attr.Value
					case "style":
						spanStyle = attr.Value
					}
				}
				children := collectNodes(d, t.Name)
				nodes = append(nodes, ttmlNode{
					Kind:     "span",
					Color:    spanColor,
					Text:     spanStyle, // reuse Text to carry style id
					Children: children,
				})
			default:
				d.Skip()
			}
		case xml.EndElement:
			if t.Name == endName {
				return nodes
			}
		}
	}
	return nodes
}

// namedColors maps BBC-used CSS colour names to hex values
var namedColors = map[string]string{
	"black":   "#000000",
	"blue":    "#0000ff",
	"green":   "#00ff00",
	"lime":    "#00ff00",
	"aqua":    "#00ffff",
	"cyan":    "#00ffff",
	"red":     "#ff0000",
	"fuchsia": "#ff00ff",
	"magenta": "#ff00ff",
	"yellow":  "#ffff00",
	"white":   "#ffffff",
}

// resolveColor converts a colour value to a normalised 7-char hex string.
// Returns "" if the value is empty or unrecognised.
func resolveColor(v string) string {
	if v == "" {
		return ""
	}
	if strings.HasPrefix(v, "#") {
		// Truncate to 7 chars (#rrggbb) — TTML sometimes includes alpha (#rrggbbaa)
		if len(v) >= 7 {
			return v[:7]
		}
		return v
	}
	if hex, ok := namedColors[strings.ToLower(v)]; ok {
		return hex
	}
	return ""
}

// ttmlTimestampToMS converts a TTML timestamp to milliseconds.
// Handles both "HH:MM:SS.mmm" and "HH:MM:SS:FF" (frame-based) formats.
func ttmlTimestampToMS(ts string, fps float64) (int64, error) {
	// Frame-based: HH:MM:SS:FF
	frameRe := regexp.MustCompile(`^(\d+):(\d+):(\d+):(\d+)$`)
	if m := frameRe.FindStringSubmatch(ts); m != nil {
		h, _ := strconv.ParseInt(m[1], 10, 64)
		min, _ := strconv.ParseInt(m[2], 10, 64)
		s, _ := strconv.ParseInt(m[3], 10, 64)
		frames, _ := strconv.ParseInt(m[4], 10, 64)
		ms := (h*3600+min*60+s)*1000
		if fps > 0 {
			ms += int64(float64(frames) / fps * 1000)
		}
		return ms, nil
	}

	// Decimal: HH:MM:SS.mmm or HH:MM:SS,mmm
	ts = strings.ReplaceAll(ts, ",", ".")
	parts := strings.SplitN(ts, ":", 3)
	if len(parts) != 3 {
		return 0, fmt.Errorf("unrecognised timestamp: %s", ts)
	}
	h, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		return 0, err
	}
	min, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil {
		return 0, err
	}
	secParts := strings.SplitN(parts[2], ".", 2)
	sec, err := strconv.ParseInt(secParts[0], 10, 64)
	if err != nil {
		return 0, err
	}
	var frac int64
	if len(secParts) == 2 {
		// Pad or truncate to 3 digits for milliseconds
		f := secParts[1]
		for len(f) < 3 {
			f += "0"
		}
		frac, _ = strconv.ParseInt(f[:3], 10, 64)
	}
	return (h*3600+min*60+sec)*1000 + frac, nil
}

// msToSRTTimestamp formats milliseconds as SRT timestamp "HH:MM:SS,mmm"
func msToSRTTimestamp(ms int64) string {
	h := ms / 3_600_000
	ms -= h * 3_600_000
	m := ms / 60_000
	ms -= m * 60_000
	s := ms / 1000
	ms -= s * 1000
	return fmt.Sprintf("%02d:%02d:%02d,%03d", h, m, s, ms)
}

// DownloadAndConvertSubtitles downloads the TTML from subtitleURL, converts it
// to SRT (with colour tags), writes the SRT to srtPath, and returns any error.
// If the download or conversion fails the error is returned but no file is written.
func DownloadAndConvertSubtitles(subtitleURL, srtPath string) error {
	// Fetch TTML
	resp, err := http.Get(subtitleURL)
	if err != nil {
		return fmt.Errorf("subtitle download failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("subtitle download returned HTTP %d", resp.StatusCode)
	}

	ttmlData, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("reading subtitle response: %v", err)
	}

	// Convert
	return convertTTMLToSRT(ttmlData, srtPath)
}

// convertTTMLToSRT parses raw TTML bytes and writes an SRT file to srtPath.
func convertTTMLToSRT(ttmlData []byte, srtPath string) error {
	var doc ttmlDoc
	if err := xml.Unmarshal(ttmlData, &doc); err != nil {
		return fmt.Errorf("parsing TTML: %v", err)
	}

	// Parse frame rate (used for HH:MM:SS:FF timestamps)
	var fps float64
	if doc.FrameRate != "" {
		fps, _ = strconv.ParseFloat(doc.FrameRate, 64)
	}

	// Build style→colour lookup from <tt:styling>
	styleColors := make(map[string]string)
	for _, style := range doc.Head.Styling.Styles {
		if style.ID != "" && style.Color != "" {
			if hex := resolveColor(style.Color); hex != "" {
				styleColors[style.ID] = hex
			}
		}
	}

	// Resolve body default colour
	bodyColor := "#ffffff"
	if c := resolveColor(doc.Body.Style); c != "" {
		bodyColor = c
	} else if c, ok := styleColors[doc.Body.Style]; ok {
		bodyColor = c
	}

	// Open output file
	f, err := os.Create(srtPath)
	if err != nil {
		return fmt.Errorf("creating SRT file: %v", err)
	}
	defer f.Close()

	index := 0

	for _, div := range doc.Body.Divs {
		// Resolve div colour, cascading from body
		divColor := bodyColor
		if c, ok := styleColors[div.Style]; ok {
			divColor = c
		}

		for _, para := range div.Paras {
			beginMS, err := ttmlTimestampToMS(para.Begin, fps)
			if err != nil {
				continue
			}
			endMS, err := ttmlTimestampToMS(para.End, fps)
			if err != nil {
				continue
			}
			if endMS <= beginMS {
				continue
			}

			// Resolve paragraph colour, cascading from div
			paraColor := divColor
			if c, ok := styleColors[para.Style]; ok {
				paraColor = c
			} else if c := resolveColor(para.Color); c != "" {
				paraColor = c
			}

			// Render the text content of this paragraph
			text := renderNodes(para.Nodes, paraColor, divColor, styleColors)
			text = strings.TrimSpace(text)
			if text == "" {
				continue
			}

			index++
			fmt.Fprintf(f, "%d\n%s --> %s\n%s\n\n",
				index,
				msToSRTTimestamp(beginMS),
				msToSRTTimestamp(endMS),
				text,
			)
		}
	}

	if index == 0 {
		// Wrote nothing — remove the empty file
		f.Close()
		os.Remove(srtPath)
		return fmt.Errorf("no subtitle cues found in TTML")
	}

	return nil
}

// renderNodes converts a slice of ttmlNodes into SRT text, applying colour tags.
// inheritColor is the colour already in effect from the parent element.
func renderNodes(nodes []ttmlNode, inheritColor, divColor string, styleColors map[string]string) string {
	var sb strings.Builder
	currentColor := inheritColor

	for _, node := range nodes {
		switch node.Kind {
		case "br":
			sb.WriteString("\n")

		case "text":
			text := collapseSpaces(node.Text)
			if text == "" {
				continue
			}
			if currentColor != "" && currentColor != "#ffffff" {
				sb.WriteString(fmt.Sprintf("<font color=\"%s\">%s</font>", currentColor, text))
			} else {
				sb.WriteString(text)
			}

		case "span":
			// Resolve span colour — node.Text carries the style id (see collectNodes)
			spanColor := inheritColor
			if styleID := node.Text; styleID != "" {
				if c, ok := styleColors[styleID]; ok {
					spanColor = c
				}
			}
			if c := resolveColor(node.Color); c != "" {
				spanColor = c
			}

			// Temporarily update current colour for nested spans
			prevColor := currentColor
			currentColor = spanColor

			spanText := renderNodes(node.Children, spanColor, divColor, styleColors)
			if spanText != "" {
				sb.WriteString(spanText)
			}

			currentColor = prevColor
		}
	}

	return sb.String()
}

// collapseSpaces trims leading/trailing whitespace and collapses internal runs
// of two or more spaces to a single space, matching the Perl behaviour.
func collapseSpaces(s string) string {
	// Replace runs of 2+ spaces at start/end with single space
	s = regexp.MustCompile(`^\s{2,}`).ReplaceAllString(s, " ")
	s = regexp.MustCompile(`\s{2,}$`).ReplaceAllString(s, " ")
	return s
}

type mp4Box struct {
	boxType   string
	dataStart int64
	end       int64
}

// readMP4Boxes iterates the child ISO BMFF boxes within [start, end).
func readMP4Boxes(rs io.ReadSeeker, start, end int64) ([]mp4Box, error) {
	var boxes []mp4Box
	var hdr [8]byte
	var large [8]byte

	for pos := start; pos+8 <= end; {
		if _, err := rs.Seek(pos, io.SeekStart); err != nil {
			return nil, err
		}
		if _, err := io.ReadFull(rs, hdr[:]); err != nil {
			return nil, err
		}

		size32 := binary.BigEndian.Uint32(hdr[0:4])
		boxType := string(hdr[4:8])
		headerSize := int64(8)
		var boxSize int64

		switch size32 {
		case 0:
			boxSize = end - pos
		case 1:
			if pos+16 > end {
				return boxes, nil
			}
			if _, err := io.ReadFull(rs, large[:]); err != nil {
				return nil, err
			}
			boxSize = int64(binary.BigEndian.Uint64(large[:]))
			headerSize = 16
		default:
			boxSize = int64(size32)
		}

		if boxSize < headerSize || pos+boxSize > end {
			return nil, fmt.Errorf("invalid MP4 box %q size %d at offset %d", boxType, boxSize, pos)
		}

		boxes = append(boxes, mp4Box{
			boxType:   boxType,
			dataStart: pos + headerSize,
			end:       pos + boxSize,
		})
		pos += boxSize
	}

	return boxes, nil
}

// clearSubtitleDefaultFlag clears the track_enabled bit (0x01) in the tkhd box
// of any subtitle track in an MP4 file.
//
// FFmpeg's MP4 muxer (libavformat/movenc.c) unconditionally sets MOV_TRACK_ENABLED
// on the first subtitle track for legacy QuickTime compatibility even when
// "-disposition:s:0 0" is passed. Media servers like Jellyfin read that bit via
// ffprobe as DISPOSITION:default=1 and auto-enable subtitles in Default mode.
// Clearing bit 0 leaves the subtitle track embedded and selectable without
// marking it as default.
func clearSubtitleDefaultFlag(mp4Path string) error {
	f, err := os.OpenFile(mp4Path, os.O_RDWR, 0)
	if err != nil {
		return fmt.Errorf("opening MP4 to clear subtitle default flag: %w", err)
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return fmt.Errorf("stat MP4: %w", err)
	}

	topBoxes, err := readMP4Boxes(f, 0, info.Size())
	if err != nil {
		return err
	}

	for _, top := range topBoxes {
		if top.boxType != "moov" {
			continue
		}
		moovChildren, err := readMP4Boxes(f, top.dataStart, top.end)
		if err != nil {
			return err
		}
		for _, child := range moovChildren {
			if child.boxType != "trak" {
				continue
			}
			if err := clearTrackEnabledIfSubtitle(f, child.dataStart, child.end); err != nil {
				return err
			}
		}
	}

	return nil
}

func clearTrackEnabledIfSubtitle(f *os.File, trakStart, trakEnd int64) error {
	trakChildren, err := readMP4Boxes(f, trakStart, trakEnd)
	if err != nil {
		return err
	}

	var tkhdFlagsByteOffset int64 = -1
	var handlerType string

	for _, child := range trakChildren {
		switch child.boxType {
		case "tkhd":
			// FullBox: 1 byte version + 3 bytes flags (big-endian); bit 0 is at offset +3.
			if child.end-child.dataStart >= 4 {
				tkhdFlagsByteOffset = child.dataStart + 3
			}
		case "mdia":
			mdiaChildren, err := readMP4Boxes(f, child.dataStart, child.end)
			if err != nil {
				return err
			}
			for _, mchild := range mdiaChildren {
				if mchild.boxType == "hdlr" && mchild.end-mchild.dataStart >= 12 {
					// FullBox (4) + pre_defined (4) + handler_type (4)
					var ht [4]byte
					if _, err := f.ReadAt(ht[:], mchild.dataStart+8); err != nil {
						return err
					}
					handlerType = string(ht[:])
				}
			}
		}
	}

	if tkhdFlagsByteOffset < 0 {
		return nil
	}

	switch handlerType {
	case "sbtl", "subt", "text":
		var b [1]byte
		if _, err := f.ReadAt(b[:], tkhdFlagsByteOffset); err != nil {
			return err
		}
		if b[0]&0x01 != 0 {
			b[0] &^= 0x01
			if _, err := f.WriteAt(b[:], tkhdFlagsByteOffset); err != nil {
				return err
			}
		}
	}

	return nil
}
