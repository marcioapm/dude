package fakelux

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"unicode"
	"unicode/utf8"

	"github.com/marciomartins/dude/orchestrator/internal/lux"
)

// wireAttachment is an attachment as it arrives, its data still base64.
type wireAttachment struct {
	Name        string `json:"name"`
	ContentType string `json:"contentType"`
	Data        string `json:"data"`
}

// attachmentMeta is what lux records of an attachment: never its bytes.
type attachmentMeta struct {
	Name        string `json:"name"`
	ContentType string `json:"contentType"`
	Size        int    `json:"size"`
	SHA256      string `json:"sha256"`
	data        []byte
}

func (m attachmentMeta) record() map[string]any {
	return map[string]any{"name": m.Name, "contentType": m.ContentType, "size": m.Size, "sha256": m.SHA256}
}

func records(ms []attachmentMeta) []any {
	out := make([]any, len(ms))
	for i, m := range ms {
		out[i] = m.record()
	}
	return out
}

var magic = map[string]func([]byte) bool{
	"image/png":  func(b []byte) bool { return bytes.HasPrefix(b, []byte("\x89PNG\r\n\x1a\n")) },
	"image/jpeg": func(b []byte) bool { return bytes.HasPrefix(b, []byte{0xff, 0xd8, 0xff}) },
	"image/gif": func(b []byte) bool {
		return bytes.HasPrefix(b, []byte("GIF87a")) || bytes.HasPrefix(b, []byte("GIF89a"))
	},
	"image/webp": func(b []byte) bool {
		return len(b) >= 12 && bytes.Equal(b[:4], []byte("RIFF")) && bytes.Equal(b[8:12], []byte("WEBP"))
	},
}

// validAttachmentName is lux's rule: 1..255 bytes of UTF-8, no path
// separators, NUL or control characters.
func validAttachmentName(name string) bool {
	if len(name) == 0 || len(name) > 255 || !utf8.ValidString(name) {
		return false
	}
	for _, r := range name {
		if r == '/' || r == '\\' || unicode.IsControl(r) {
			return false
		}
	}
	return true
}

// checkAttachments decodes and validates attachments as lux does
// (feat/input-attachments), or says which one it refuses and why.
func checkAttachments(raw json.RawMessage) ([]attachmentMeta, string) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, ""
	}
	var in []wireAttachment
	if err := json.Unmarshal(raw, &in); err != nil {
		return nil, "attachments: not a list of attachments"
	}
	if len(in) > lux.MaxAttachments {
		return nil, fmt.Sprintf("attachments: at most %d per input, got %d", lux.MaxAttachments, len(in))
	}
	out := make([]attachmentMeta, 0, len(in))
	for i, a := range in {
		is, ok := magic[a.ContentType]
		if !ok {
			return nil, fmt.Sprintf("attachments[%d]: unknown content type %q", i, a.ContentType)
		}
		if !validAttachmentName(a.Name) {
			return nil, fmt.Sprintf("attachments[%d]: bad name", i)
		}
		data, err := base64.StdEncoding.DecodeString(a.Data)
		if err != nil {
			return nil, fmt.Sprintf("attachments[%d]: data is not standard base64", i)
		}
		if len(data) > lux.MaxAttachmentBytes {
			return nil, fmt.Sprintf("attachments[%d]: %d bytes, over %d", i, len(data), lux.MaxAttachmentBytes)
		}
		if !is(data) {
			return nil, fmt.Sprintf("attachments[%d]: the data is not %s", i, a.ContentType)
		}
		sum := sha256.Sum256(data)
		out = append(out, attachmentMeta{Name: a.Name, ContentType: a.ContentType, Size: len(data),
			SHA256: hex.EncodeToString(sum[:]), data: data})
	}
	return out, ""
}

// Attachments is what the agent got with each input of a Run that carried
// images, by request id ("prompt" for the task's): name, type and bytes.
func (s *Server) Attachments(runID string) map[string][]lux.Attachment {
	s.mu.Lock()
	defer s.mu.Unlock()
	run := s.runs[runID]
	if run == nil {
		return nil
	}
	out := map[string][]lux.Attachment{}
	for id, ms := range run.attachments {
		for _, m := range ms {
			out[id] = append(out[id], lux.Attachment{Name: m.Name, ContentType: m.ContentType, Data: m.data})
		}
	}
	return out
}
