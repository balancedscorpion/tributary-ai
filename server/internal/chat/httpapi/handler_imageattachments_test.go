package httpapi

import "testing"

func TestExtractImageAttachments_ParsesArtifactID(t *testing.T) {
	t.Parallel()

	raw := []any{
		map[string]any{
			"artifactId": "img-artifact-1",
			"fileName":   "diagram.png",
			"mimeType":   "image/png",
			"dataUri":    "data:image/png;base64,AAAA",
			"sizeBytes":  float64(4),
			"width":      float64(2),
			"height":     float64(3),
			"source":     "upload",
		},
	}

	attachments, err := extractImageAttachments(raw)
	if err != nil {
		t.Fatalf("extractImageAttachments: %v", err)
	}
	if len(attachments) != 1 {
		t.Fatalf("len(attachments)=%d, want 1", len(attachments))
	}

	attachment := attachments[0]
	if attachment.ArtifactID != "img-artifact-1" {
		t.Fatalf("ArtifactID=%q, want img-artifact-1", attachment.ArtifactID)
	}
	if attachment.FileName != "diagram.png" {
		t.Fatalf("FileName=%q, want diagram.png", attachment.FileName)
	}
	if attachment.MimeType != "image/png" {
		t.Fatalf("MimeType=%q, want image/png", attachment.MimeType)
	}
	if attachment.DataURI != "data:image/png;base64,AAAA" {
		t.Fatalf("DataURI=%q, want expected data URI", attachment.DataURI)
	}
	if attachment.SizeBytes == nil || *attachment.SizeBytes != 4 {
		t.Fatalf("SizeBytes=%v, want 4", attachment.SizeBytes)
	}
	if attachment.Width == nil || *attachment.Width != 2 {
		t.Fatalf("Width=%v, want 2", attachment.Width)
	}
	if attachment.Height == nil || *attachment.Height != 3 {
		t.Fatalf("Height=%v, want 3", attachment.Height)
	}
	if attachment.Source != "upload" {
		t.Fatalf("Source=%q, want upload", attachment.Source)
	}
}
