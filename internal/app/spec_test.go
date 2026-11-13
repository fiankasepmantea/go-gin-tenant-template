package app

import (
	"encoding/json"
	"strings"
	"testing"
)

// Every route of the API is in the OpenAPI document, and the documented bodies match the real input types.
func TestSpec_CoversEveryRoute(t *testing.T) {
	modules := Modules(Deps{Account: nil})
	raw, err := Spec(modules, "t", "1").Marshal()
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Paths map[string]map[string]struct {
			Summary string `json:"summary"`
		} `json:"paths"`
		Components struct {
			Schemas map[string]struct {
				Required []string `json:"required"`
			} `json:"schemas"`
		} `json:"components"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	for _, r := range Routes(modules) {
		p := strings.NewReplacer(":id", "{id}", ":column", "{column}", ":value", "{value}").Replace(r.Path)
		if _, ok := doc.Paths[p][strings.ToLower(r.Method)]; !ok {
			t.Errorf("%s %s is not in the document", r.Method, r.Path)
		}
	}
	// described, not just listed
	if got := doc.Paths["/api/v1/groups"]["post"].Summary; !strings.HasPrefix(got, "Create") {
		t.Errorf("POST /groups summary = %q", got)
	}
	if req := doc.Components.Schemas["group.createInput"].Required; len(req) != 1 || req[0] != "name" {
		t.Errorf("group.createInput required = %v", req)
	}
}
