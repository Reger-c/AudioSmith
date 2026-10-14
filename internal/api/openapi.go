package api

import (
	"bytes"
	"cmp"
	_ "embed"
	"encoding/json"
	"net/http"
	"path"
	"slices"
	"strings"
	"time"
)

// openapiJSON is the API's OpenAPI 3.1 description, written by hand next
// to the code (TestOpenAPIMatchesRoutes keeps the two in step).
//
//go:embed openapi.json
var openapiJSON []byte

// Spec returns the OpenAPI document with this server's version.
func (s *Server) Spec() []byte {
	var doc map[string]any
	if err := json.Unmarshal(openapiJSON, &doc); err != nil {
		return openapiJSON
	}
	if info, ok := doc["info"].(map[string]any); ok && s.o.Version != "" {
		info["version"] = s.o.Version
	}
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	enc.Encode(doc)
	return b.Bytes()
}

// SpecOperation is one operation of the spec, for the settings page.
type SpecOperation struct {
	Method, Path, Summary, Tag string
}

// Operations lists the spec's operations in its order.
func Operations() []SpecOperation {
	var doc struct {
		Paths map[string]map[string]struct {
			Summary string   `json:"summary"`
			Tags    []string `json:"tags"`
		} `json:"paths"`
		Tags []struct {
			Name string `json:"name"`
		} `json:"tags"`
	}
	if err := json.Unmarshal(openapiJSON, &doc); err != nil {
		return nil
	}
	tagOrder := map[string]int{}
	for i, t := range doc.Tags {
		tagOrder[t.Name] = i
	}
	var out []SpecOperation
	for p, ops := range doc.Paths {
		for m, op := range ops {
			tag := ""
			if len(op.Tags) > 0 {
				tag = op.Tags[0]
			}
			out = append(out, SpecOperation{Method: strings.ToUpper(m), Path: p, Summary: op.Summary, Tag: tag})
		}
	}
	methodOrder := map[string]int{"GET": 0, "POST": 1, "PUT": 2, "DELETE": 3}
	slices.SortFunc(out, func(a, b SpecOperation) int {
		return cmp.Or(cmp.Compare(tagOrder[a.Tag], tagOrder[b.Tag]), strings.Compare(a.Path, b.Path),
			cmp.Compare(methodOrder[a.Method], methodOrder[b.Method]))
	})
	return out
}

func (s *Server) openapi(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Write(s.Spec())
}

// The reference viewer: a page of the API's own origin that renders
// /openapi.json with RapiDoc (MIT, vendored in docs/), offline.
//
//go:embed docs/index.html
var docsHTML []byte

//go:embed docs/rapidoc-min.js
var rapidocJS []byte

var started = time.Now()

func (s *Server) docs(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; connect-src 'self'")
	page := docsHTML
	if r.URL.Query().Get("theme") == "dark" {
		page = bytes.Replace(page, []byte(`theme="light"`), []byte(`theme="dark"`), 1)
	}
	w.Write(page)
}

func (s *Server) docsAsset(w http.ResponseWriter, r *http.Request) {
	switch path.Base(r.PathValue("file")) {
	case "rapidoc-min.js":
		w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
		w.Header().Set("Cache-Control", "max-age=86400")
		http.ServeContent(w, r, "rapidoc-min.js", started, bytes.NewReader(rapidocJS))
	default:
		writeError(w, errNotFound(CodeNotFound, "Not found."))
	}
}
