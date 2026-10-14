package api

import (
	"encoding/json"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// TestOpenAPIMatchesRoutes: every route is described, and every
// described operation is a route.
func TestOpenAPIMatchesRoutes(t *testing.T) {
	f := newFake(t)
	s, _ := f.server()
	var doc struct {
		OpenAPI string                                `json:"openapi"`
		Paths   map[string]map[string]json.RawMessage `json:"paths"`
	}
	if err := json.Unmarshal(openapiJSON, &doc); err != nil {
		t.Fatal(err)
	}
	if doc.OpenAPI != "3.1.0" {
		t.Fatalf("openapi %q", doc.OpenAPI)
	}
	described := map[string]bool{}
	for p, ops := range doc.Paths {
		for m := range ops {
			described[strings.ToUpper(m)+" "+p] = true
		}
	}
	routes := map[string]bool{}
	for _, r := range s.Routes() {
		method, path, ok := strings.Cut(r.Pattern, " ")
		if !ok { // any method: /mcp
			path, method = r.Pattern, ""
		}
		path = strings.TrimSuffix(path, "{$}")
		switch path {
		case "/docs/{file}", "/mcp/":
			continue // assets; the slash form of /mcp
		}
		if method == "" {
			for _, m := range []string{"POST", "GET", "DELETE"} {
				routes[m+" "+path] = true
			}
			continue
		}
		routes[method+" "+path] = true
	}
	for r := range routes {
		if !described[r] {
			t.Errorf("route %s is not in openapi.json", r)
		}
	}
	for d := range described {
		if !routes[d] {
			t.Errorf("openapi.json describes %s, which is not a route", d)
		}
	}
}

// TestOpenAPIRefs: every $ref points at a component.
func TestOpenAPIRefs(t *testing.T) {
	var doc map[string]any
	json.Unmarshal(openapiJSON, &doc)
	comps := doc["components"].(map[string]any)
	for _, m := range regexp.MustCompile(`"\$ref":\s*"#/components/(\w+)/(\w+)"`).FindAllStringSubmatch(string(openapiJSON), -1) {
		sec, _ := comps[m[1]].(map[string]any)
		if _, ok := sec[m[2]]; !ok {
			t.Errorf("dangling $ref %s/%s", m[1], m[2])
		}
	}
	ops := Operations()
	if len(ops) < 30 || ops[0].Tag != "OpenAI" {
		t.Fatalf("operations: %d, first %+v", len(ops), ops[0])
	}
	ids := map[string]bool{}
	for _, p := range doc["paths"].(map[string]any) {
		for _, o := range p.(map[string]any) {
			id := o.(map[string]any)["operationId"].(string)
			if ids[id] {
				t.Errorf("operationId %s twice", id)
			}
			ids[id] = true
		}
	}
	if !slices.ContainsFunc(ops, func(o SpecOperation) bool { return o.Path == "/v1/audio/speech" && o.Method == "POST" }) {
		t.Fatal("speech missing")
	}
}
