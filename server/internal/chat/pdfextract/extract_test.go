package pdfextract

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const fixturePDFBase64 = "JVBERi0xLjMKJZOMi54gUmVwb3J0TGFiIEdlbmVyYXRlZCBQREYgZG9jdW1lbnQgKG9wZW5zb3VyY2UpCjEgMCBvYmoKPDwKL0YxIDIgMCBSCj4+CmVuZG9iagoyIDAgb2JqCjw8Ci9CYXNlRm9udCAvSGVsdmV0aWNhIC9FbmNvZGluZyAvV2luQW5zaUVuY29kaW5nIC9OYW1lIC9GMSAvU3VidHlwZSAvVHlwZTEgL1R5cGUgL0ZvbnQKPj4KZW5kb2JqCjMgMCBvYmoKPDwKL0NvbnRlbnRzIDggMCBSIC9NZWRpYUJveCBbIDAgMCA2MTIgNzkyIF0gL1BhcmVudCA3IDAgUiAvUmVzb3VyY2VzIDw8Ci9Gb250IDEgMCBSIC9Qcm9jU2V0IFsgL1BERiAvVGV4dCAvSW1hZ2VCIC9JbWFnZUMgL0ltYWdlSSBdCj4+IC9Sb3RhdGUgMCAvVHJhbnMgPDwKCj4+IAogIC9UeXBlIC9QYWdlCj4+CmVuZG9iago0IDAgb2JqCjw8Ci9Db250ZW50cyA5IDAgUiAvTWVkaWFCb3ggWyAwIDAgNjEyIDc5MiBdIC9QYXJlbnQgNyAwIFIgL1Jlc291cmNlcyA8PAovRm9udCAxIDAgUiAvUHJvY1NldCBbIC9QREYgL1RleHQgL0ltYWdlQiAvSW1hZ2VDIC9JbWFnZUkgXQo+PiAvUm90YXRlIDAgL1RyYW5zIDw8Cgo+PiAKICAvVHlwZSAvUGFnZQo+PgplbmRvYmoKNSAwIG9iago8PAovUGFnZU1vZGUgL1VzZU5vbmUgL1BhZ2VzIDcgMCBSIC9UeXBlIC9DYXRhbG9nCj4+CmVuZG9iago2IDAgb2JqCjw8Ci9BdXRob3IgKGFub255bW91cykgL0NyZWF0aW9uRGF0ZSAoRDoyMDI2MDYwNjE2MzYwMSswMScwMCcpIC9DcmVhdG9yIChhbm9ueW1vdXMpIC9LZXl3b3JkcyAoKSAvTW9kRGF0ZSAoRDoyMDI2MDYwNjE2MzYwMSswMScwMCcpIC9Qcm9kdWNlciAoUmVwb3J0TGFiIFBERiBMaWJyYXJ5IC0gXChvcGVuc291cmNlXCkpIAogIC9TdWJqZWN0ICh1bnNwZWNpZmllZCkgL1RpdGxlIChGaXh0dXJlIERlY2spIC9UcmFwcGVkIC9GYWxzZQo+PgplbmRvYmoKNyAwIG9iago8PAovQ291bnQgMiAvS2lkcyBbIDMgMCBSIDQgMCBSIF0gL1R5cGUgL1BhZ2VzCj4+CmVuZG9iago4IDAgb2JqCjw8Ci9GaWx0ZXIgWyAvQVNDSUk4NURlY29kZSAvRmxhdGVEZWNvZGUgXSAvTGVuZ3RoIDE2Ngo+PgpzdHJlYW0KR2FyVzBfJFwlNSY0SCFjTUUraU5ST25daCQ3XEInZ0NGUjRkbWQjYUNeVGsoJUkxY15iVjRmXGhATj9tXWxPSXBKWyU/TVZ1XHRWXTRvKSVOYS5qQF49LUBkQ3FwKFBaYyllR0lDdEtnOFptI11hYEEpKj1gT3RjSl8sQlpvNzs3clVWS0Ntai0xO0hPZT1KUmFsZDpzWVgqYVs6cVprKnFDSnV+PmVuZHN0cmVhbQplbmRvYmoKOSAwIG9iago8PAovRmlsdGVyIFsgL0FTQ0lJODVEZWNvZGUgL0ZsYXRlRGVjb2RlIF0gL0xlbmd0aCAxMTkKPj4Kc3RyZWFtCkdhcFFoMEU9RiwwVVxIM1RccE5ZVF5RS2s/dGM+SVAsO1cjVTFeMjNpaFBFTV8/Q1c0S0lTaTwhWzdgI09CX3NLaTctaWNdRy0nT2A6UiNfbSpuWF1QL1FfOmA6Q2poQFNNUTwvOzRXZU9BNXMzISpkI088cn4+ZW5kc3RyZWFtCmVuZG9iagp4cmVmCjAgMTAKMDAwMDAwMDAwMCA2NTUzNSBmIAowMDAwMDAwMDYxIDAwMDAwIG4gCjAwMDAwMDAwOTIgMDAwMDAgbiAKMDAwMDAwMDE5OSAwMDAwMCBuIAowMDAwMDAwMzkyIDAwMDAwIG4gCjAwMDAwMDA1ODUgMDAwMDAgbiAKMDAwMDAwMDY1MyAwMDAwMCBuIAowMDAwMDAwOTE4IDAwMDAwIG4gCjAwMDAwMDA5ODMgMDAwMDAgbiAKMDAwMDAwMTIzOSAwMDAwMCBuIAp0cmFpbGVyCjw8Ci9JRCAKWzw0M2JkNzExOGE4ZjBkYTI2Mzk0NTUyOWVjMmQ1ZmI2Nj48NDNiZDcxMThhOGYwZGEyNjM5NDU1MjllYzJkNWZiNjY+XQolIFJlcG9ydExhYiBnZW5lcmF0ZWQgUERGIGRvY3VtZW50IC0tIGRpZ2VzdCAob3BlbnNvdXJjZSkKCi9JbmZvIDYgMCBSCi9Sb290IDUgMCBSCi9TaXplIDEwCj4+CnN0YXJ0eHJlZgoxNDQ4CiUlRU9GCg=="

func TestExtractUsesPyPDFWhenAvailable(t *testing.T) {
	python, ok := findPythonWithModule("pypdf")
	if !ok {
		t.Skip("pypdf unavailable")
	}
	t.Logf("using pypdf from %s", python)

	raw, err := base64.StdEncoding.DecodeString(fixturePDFBase64)
	if err != nil {
		t.Fatalf("decode fixture: %v", err)
	}
	dir := t.TempDir()
	sourcePath := filepath.Join(dir, "fixture.pdf")
	if err := os.WriteFile(sourcePath, raw, 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	result, err := Extract(Request{
		SourcePath: sourcePath,
		FileName:   "fixture.pdf",
		OutputDir:  filepath.Join(dir, "extract"),
	})
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if result.Manifest.TextExtractionQuality != "pypdf" {
		t.Fatalf("TextExtractionQuality=%q, want pypdf", result.Manifest.TextExtractionQuality)
	}
	if !strings.Contains(result.Document.Text, "GenAI Unleashed fixture text") {
		t.Fatalf("extracted text missing fixture content: %q", result.Document.Text)
	}
	if !strings.Contains(result.Document.Text, "Second page key takeaways") {
		t.Fatalf("extracted text missing second page content: %q", result.Document.Text)
	}
}
