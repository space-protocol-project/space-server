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
		return validatePNGIcon(icon.Data)
	case "image/svg+xml":
		if !utf8.Valid(icon.Data) || !safeSVG(icon.Data) {
			return invalid()
		}
		return nil
	}
	return invalid()
}

const maxSVGTokens = 512
const maxSVGDepth = 16

type svgDocument struct {
	depth, tokenCount, rootCount       int
	allowedElements, allowedAttributes map[string]bool
}

func newSVGDocument() *svgDocument {
	attributes := strings.Fields("xmlns viewBox width height d fill stroke stroke-width stroke-linecap stroke-linejoin fill-rule clip-rule cx cy r rx ry x y x1 y1 x2 y2 points transform opacity fill-opacity stroke-opacity")
	allowedAttributes := make(map[string]bool, len(attributes))
	for _, attribute := range attributes {
		allowedAttributes[attribute] = true
	}
	return &svgDocument{
		allowedElements:   map[string]bool{"svg": true, "g": true, "path": true, "rect": true, "circle": true, "ellipse": true, "line": true, "polyline": true, "polygon": true},
		allowedAttributes: allowedAttributes,
	}
}

func safeSVG(data []byte) bool {
	decoder := xml.NewDecoder(bytes.NewReader(data))
	document := newSVGDocument()
	for {
		token, err := decoder.Token()
		if err == io.EOF {
			return document.rootCount == 1 && document.depth == 0
		}
		if err != nil || !document.acceptToken(token) {
			return false
		}
	}
}

func (document *svgDocument) acceptToken(token xml.Token) bool {
	document.tokenCount++
	if document.tokenCount > maxSVGTokens {
		return false
	}
	switch element := token.(type) {
	case xml.StartElement:
		return document.startElement(element)
	case xml.EndElement:
		document.depth--
		return true
	case xml.ProcInst:
		return element.Target == "xml"
	case xml.CharData:
		return strings.TrimSpace(string(element)) == ""
	case xml.Comment:
		return true
	default:
		return false
	}
}

func (document *svgDocument) startElement(element xml.StartElement) bool {
	if !safeSVGElement(element, document.allowedElements, document.allowedAttributes) {
		return false
	}
	if document.depth == 0 {
		document.rootCount++
		if element.Name.Local != "svg" || document.rootCount > 1 {
			return false
		}
	}
	document.depth++
	return document.depth <= maxSVGDepth
}

func safeSVGElement(element xml.StartElement, tags, allowed map[string]bool) bool {
	if !tags[element.Name.Local] {
		return false
	}
	if element.Name.Space != "" && element.Name.Space != "http://www.w3.org/2000/svg" {
		return false
	}
	for _, attribute := range element.Attr {
		if !safeSVGAttribute(attribute, allowed) {
			return false
		}
	}
	return true
}

func safeSVGAttribute(attribute xml.Attr, allowed map[string]bool) bool {
	if !allowed[attribute.Name.Local] {
		return false
	}
	if attribute.Name.Space != "" && attribute.Name.Space != "xmlns" {
		return false
	}
	value := strings.ToLower(strings.Join(strings.Fields(attribute.Value), ""))
	return !strings.Contains(value, "url(")
}

func validatePNGIcon(data []byte) error {
	invalid := func() error {
		return status.Error(codes.InvalidArgument, "Нужна стандартная иконка или безопасный PNG/SVG до 8 КиБ")
	}
	config, err := png.DecodeConfig(bytes.NewReader(data))
	if err != nil || config.Width < 1 || config.Height < 1 || config.Width > 256 || config.Height > 256 {
		return invalid()
	}
	if _, err = png.Decode(bytes.NewReader(data)); err != nil {
		return invalid()
	}
	return nil
}
