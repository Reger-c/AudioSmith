package telephony

import (
	"crypto/hmac"
	"crypto/sha1"
	"encoding/base64"
	"net/url"
	"sort"
	"strings"
)

// Signature computes Twilio's X-Twilio-Signature of a request: the
// base64 HMAC-SHA1, keyed with the account's auth token, of the full URL
// Twilio requested (scheme, host, path and query) followed by every POST
// parameter's name and value, sorted by name (then value), with no
// separators.
func Signature(token, fullURL string, params url.Values) string {
	var b strings.Builder
	b.WriteString(fullURL)
	keys := make([]string, 0, len(params))
	for k := range params {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		vs := append([]string(nil), params[k]...)
		sort.Strings(vs)
		for _, v := range vs {
			b.WriteString(k)
			b.WriteString(v)
		}
	}
	m := hmac.New(sha1.New, []byte(token))
	m.Write([]byte(b.String()))
	return base64.StdEncoding.EncodeToString(m.Sum(nil))
}

// ValidSignature reports whether sig signs the request to fullURL with
// params. The URL is tried as given and with its default port added or
// removed, as proxies and tunnels may report either.
func ValidSignature(token, fullURL string, params url.Values, sig string) bool {
	if token == "" || sig == "" {
		return false
	}
	for _, u := range urlVariants(fullURL) {
		if hmac.Equal([]byte(Signature(token, u, params)), []byte(sig)) {
			return true
		}
	}
	return false
}

// urlVariants are fullURL with and without its scheme's default port.
func urlVariants(fullURL string) []string {
	out := []string{fullURL}
	u, err := url.Parse(fullURL)
	if err != nil || u.Host == "" {
		return out
	}
	def := map[string]string{"https": "443", "wss": "443", "http": "80", "ws": "80"}[u.Scheme]
	if def == "" {
		return out
	}
	v := *u
	if u.Port() == "" {
		v.Host = u.Host + ":" + def
	} else if u.Port() == def {
		v.Host = u.Hostname()
	} else {
		return out
	}
	return append(out, v.String())
}
