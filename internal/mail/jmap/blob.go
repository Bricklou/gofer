package jmap

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
)

// ContentTypeMessage is the type to request when downloading an Email's
// blobId, which is the raw RFC 5322 message.
const ContentTypeMessage = "message/rfc822"

// DownloadBlob fetches a blob through the session's downloadUrl. name is
// only a suggested file name for the download. The caller must close the
// returned body and should limit how much of it it reads.
func (c *Client) DownloadBlob(ctx context.Context, session *Session, accountID, blobID, name, contentType string) (io.ReadCloser, error) {
	if session == nil || session.DownloadURL == "" {
		return nil, errors.New("jmap: session has no downloadUrl")
	}
	if name == "" {
		name = blobID
	}
	endpoint := expandTemplate(session.DownloadURL, map[string]string{
		"accountId": accountID,
		"blobId":    blobID,
		"name":      name,
		"type":      contentType,
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	if c.Auth != nil {
		c.Auth(req)
	}
	resp, err := c.httpClient().Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		defer resp.Body.Close()
		return nil, newHTTPError(resp)
	}
	return resp.Body, nil
}

// expandTemplate fills {name} placeholders in a session URL template
// (RFC 6570 simple expansion): every value is percent-encoded except the
// unreserved characters, so a "/" in a type or a space in a name cannot
// change the URL's structure.
func expandTemplate(template string, values map[string]string) string {
	var out strings.Builder
	for {
		start := strings.IndexByte(template, '{')
		if start < 0 {
			out.WriteString(template)
			return out.String()
		}
		end := strings.IndexByte(template[start:], '}')
		if end < 0 {
			out.WriteString(template)
			return out.String()
		}
		end += start
		out.WriteString(template[:start])
		out.WriteString(escapeTemplateValue(values[template[start+1:end]]))
		template = template[end+1:]
	}
}

func escapeTemplateValue(value string) string {
	const hex = "0123456789ABCDEF"
	var out strings.Builder
	for i := 0; i < len(value); i++ {
		b := value[i]
		if 'A' <= b && b <= 'Z' || 'a' <= b && b <= 'z' || '0' <= b && b <= '9' || b == '-' || b == '.' || b == '_' || b == '~' {
			out.WriteByte(b)
			continue
		}
		out.WriteByte('%')
		out.WriteByte(hex[b>>4])
		out.WriteByte(hex[b&0x0f])
	}
	return out.String()
}
