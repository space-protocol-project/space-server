package postgres

import (
	"bytes"
	"encoding/xml"
	pb "github.com/space-protocol-project/space-server/gen/space/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"image/png"
	"io"
	"strings"
	"unicode/utf8"
)

func validateServerIcon(icon *pb.ServerIcon) error {
	invalid := func() error {
		return status.Error(codes.InvalidArgument, "Нужна стандартная иконка или безопасный PNG/SVG до 8 КиБ")
	}
	if icon == nil {
		return nil
	}
	if len(icon.Data) == 0 {
		if icon.MediaType != "" {
			return invalid()
		}
		switch icon.Preset {
		case "orbit", "planet", "leaf", "star", "bolt", "rocket":
			return nil
		}
		return invalid()
	}
	if len(icon.Data) > 8192 || icon.Preset != "" {
		return invalid()
	}
	switch icon.MediaType {
	case "image/png":
		config, err := png.DecodeConfig(bytes.NewReader(icon.Data))
		if err != nil || config.Width < 1 || config.Height < 1 || config.Width > 256 || config.Height > 256 {
			return invalid()
		}
		if _, err = png.Decode(bytes.NewReader(icon.Data)); err != nil {
			return invalid()
		}
		return nil
	case "image/svg+xml":
		if !utf8.Valid(icon.Data) || !safeSVG(icon.Data) {
			return invalid()
		}
		return nil
	}
	return invalid()
}
func safeSVG(data []byte) bool {
	tags := map[string]bool{"svg": true, "g": true, "path": true, "rect": true, "circle": true, "ellipse": true, "line": true, "polyline": true, "polygon": true}
	attrs := strings.Fields("xmlns viewBox width height d fill stroke stroke-width stroke-linecap stroke-linejoin fill-rule clip-rule cx cy r rx ry x y x1 y1 x2 y2 points transform opacity fill-opacity stroke-opacity")
	allowed := map[string]bool{}
	for _, a := range attrs {
		allowed[a] = true
	}
	decoder := xml.NewDecoder(bytes.NewReader(data))
	depth, count, roots := 0, 0, 0
	for {
		token, err := decoder.Token()
		if err == io.EOF {
			return roots == 1 && depth == 0
		}
		if err != nil {
			return false
		}
		count++
		if count > 512 {
			return false
		}
		switch t := token.(type) {
		case xml.StartElement:
			if !tags[t.Name.Local] || (t.Name.Space != "" && t.Name.Space != "http://www.w3.org/2000/svg") {
				return false
			}
			if depth == 0 {
				roots++
				if t.Name.Local != "svg" || roots > 1 {
					return false
				}
			}
			depth++
			if depth > 16 {
				return false
			}
			for _, a := range t.Attr {
				if !allowed[a.Name.Local] || (a.Name.Space != "" && a.Name.Space != "xmlns") || strings.Contains(strings.ToLower(strings.Join(strings.Fields(a.Value), "")), "url(") {
					return false
				}
			}
		case xml.EndElement:
			depth--
		case xml.Directive:
			return false
		case xml.ProcInst:
			if t.Target != "xml" {
				return false
			}
		case xml.CharData:
			if strings.TrimSpace(string(t)) != "" {
				return false
			}
		case xml.Comment:
		default:
			return false
		}
	}
}
