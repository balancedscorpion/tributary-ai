package vision

import (
	"strings"

	"ai-analyst-local/internal/chat/types"
)

type EncodedImage struct {
	FileName   string
	MIMEType   string
	Base64Data string
	DataURI    string
}

func ParseDataURI(dataURI string) (mimeType string, base64Data string, ok bool) {
	raw := strings.TrimSpace(dataURI)
	if raw == "" {
		return "", "", false
	}
	if !strings.HasPrefix(strings.ToLower(raw), "data:") {
		return "", "", false
	}

	comma := strings.Index(raw, ",")
	if comma <= len("data:") {
		return "", "", false
	}
	meta := strings.TrimSpace(raw[len("data:"):comma])
	payload := sanitizeBase64(raw[comma+1:])
	if payload == "" {
		return "", "", false
	}

	parts := strings.Split(meta, ";")
	mime := strings.ToLower(strings.TrimSpace(parts[0]))
	if !strings.HasPrefix(mime, "image/") {
		return "", "", false
	}

	hasBase64 := false
	for _, p := range parts[1:] {
		if strings.EqualFold(strings.TrimSpace(p), "base64") {
			hasBase64 = true
			break
		}
	}
	if !hasBase64 {
		return "", "", false
	}
	return mime, payload, true
}

func NormalizeAttachments(in []types.ImageAttachment) []EncodedImage {
	if len(in) == 0 {
		return nil
	}
	out := make([]EncodedImage, 0, len(in))
	for _, item := range in {
		mime, data, ok := ParseDataURI(item.DataURI)
		if !ok {
			continue
		}
		if override := strings.ToLower(strings.TrimSpace(item.MimeType)); strings.HasPrefix(override, "image/") {
			mime = override
		}
		dataURI := strings.TrimSpace(item.DataURI)
		out = append(out, EncodedImage{
			FileName:   strings.TrimSpace(item.FileName),
			MIMEType:   mime,
			Base64Data: data,
			DataURI:    dataURI,
		})
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func sanitizeBase64(raw string) string {
	s := strings.TrimSpace(raw)
	if s == "" {
		return ""
	}
	s = strings.ReplaceAll(s, "\n", "")
	s = strings.ReplaceAll(s, "\r", "")
	s = strings.ReplaceAll(s, "\t", "")
	s = strings.ReplaceAll(s, " ", "")
	return s
}
