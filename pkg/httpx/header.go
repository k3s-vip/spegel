package httpx

import "net/http"

const (
	HeaderContentType     = "Content-Type"
	HeaderContentLength   = "Content-Length"
	HeaderContentRange    = "Content-Range"
	HeaderRange           = "Range"
	HeaderAcceptRanges    = "Accept-Ranges"
	HeaderUserAgent       = "User-Agent"
	HeaderAccept          = "Accept"
	HeaderAuthorization   = "Authorization"
	HeaderWWWAuthenticate = "WWW-Authenticate"
	HeaderXForwardedFor   = "X-Forwarded-For"
)

const (
	ContentTypeBinary = "application/octet-stream"
	ContentTypeJSON   = "application/json"
)

// CopyHeader copies header from source to destination.
func CopyHeader(dst, src http.Header) {
	for k, vv := range src {
		for _, v := range vv {
			dst.Add(k, v)
		}
	}
}
