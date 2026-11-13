package handler

import (
	"encoding/json"
	"fmt"
	"net/url"
	"slices"
	"strings"
	"testing"

	"yagp/internal/model"
)

func TestNodeHTTPTags(t *testing.T) {
	f := newHandlerHTTPFixture(t)
	f.registerNodes()
	const specialTag = `边缘 "relay\ops" <& %_`
	want := []string{"入口", specialTag}
	encoded, err := json.Marshal([]string{" 入口 ", specialTag, "入口"})
	if err != nil {
		t.Fatal(err)
	}
	created := handlerData[model.GostNode](t, f.request(t, "POST", "/nodes", `{"name":"edge-a","tags":`+string(encoded)+`}`, 200))
	if !slices.Equal(created.Tags, want) {
		t.Fatalf("created tags = %v, want %v", created.Tags, want)
	}
	path := fmt.Sprintf("/nodes/%d", created.ID)
	other := handlerData[model.GostNode](t, f.request(t, "POST", "/nodes", `{"name":"edge-b","tags":["入口","West"]}`, 200))
	f.request(t, "POST", "/nodes", `{"name":"edge-c","tags":["主入口","west"]}`, 200)
	legacy := model.GostNode{Name: "legacy"}
	if err := f.db.Omit("Tags").Create(&legacy).Error; err != nil {
		t.Fatal(err)
	}
	legacyNode := handlerData[model.GostNode](t, f.request(t, "GET", fmt.Sprintf("/nodes/%d", legacy.ID), "", 200))
	if legacyNode.Tags == nil || len(legacyNode.Tags) != 0 {
		t.Fatalf("legacy tags = %#v, want []", legacyNode.Tags)
	}

	for _, body := range []string{`{"name":"edge-a"}`, `{"name":"edge-a","tags":null}`} {
		updated := handlerData[model.GostNode](t, f.request(t, "PUT", path, body, 200))
		if !slices.Equal(updated.Tags, want) {
			t.Fatalf("metadata update changed tags: %v", updated.Tags)
		}
	}
	detail := handlerData[model.GostNode](t, f.request(t, "GET", path, "", 200))
	if !slices.Equal(detail.Tags, want) {
		t.Fatalf("persisted tags = %v, want %v", detail.Tags, want)
	}

	list := func(tag, filters string) handlerPage[model.GostNode] {
		t.Helper()
		return handlerData[handlerPage[model.GostNode]](t, f.request(t, "GET", "/nodes?tag="+url.QueryEscape(tag)+filters, "", 200))
	}
	if found := list(" 入口 ", "&pageSize=1"); found.Total != 2 || len(found.List) != 1 || len(found.List[0].Tags) != 2 {
		t.Fatalf("tag filter did not count matches before pagination: %+v", found)
	}
	if found := list("入口", "&keyword=edge-a&status=offline"); found.Total != 1 || len(found.List) != 1 || found.List[0].ID != created.ID {
		t.Fatalf("tag filter did not combine with other filters: %+v", found)
	}
	for _, test := range []struct {
		tag string
		id  uint
	}{
		{specialTag, created.ID},
		{"West", other.ID},
	} {
		if found := list(test.tag, ""); found.Total != 1 || len(found.List) != 1 || found.List[0].ID != test.id {
			t.Fatalf("tag %q did not match exactly: %+v", test.tag, found)
		}
	}
	if found := list("relay", ""); found.Total != 0 || len(found.List) != 0 {
		t.Fatalf("tag filter matched a substring: %+v", found)
	}

	f.request(t, "PUT", path, `{"name":"edge-a","tags":["已更新"]}`, 200)
	if found := list("已更新", ""); found.Total != 1 || len(found.List) != 1 || found.List[0].ID != created.ID {
		t.Fatalf("replaced tag was not searchable: %+v", found)
	}
	f.request(t, "PUT", path, `{"name":"edge-a","tags":[]}`, 200)
	cleared := handlerData[model.GostNode](t, f.request(t, "GET", path, "", 200))
	if cleared.Tags == nil || len(cleared.Tags) != 0 || list("已更新", "").Total != 0 {
		t.Fatalf("empty array did not clear the saved tags: %#v", cleared.Tags)
	}
}

func TestNodeHTTPRejectsInvalidTags(t *testing.T) {
	f := newHandlerHTTPFixture(t)
	f.registerNodes()
	node := handlerData[model.GostNode](t, f.request(t, "POST", "/nodes", `{"name":"tagged","tags":["existing"]}`, 200))
	path := fmt.Sprintf("/nodes/%d", node.ID)
	for _, raw := range []string{
		`"not an array"`,
		`[123]`,
		`[null]`,
		`[" "]`,
		`["control\u0000character"]`,
		`["` + strings.Repeat("字", 33) + `"]`,
		`["a","b","c","d","e","f","g","h","i","j","k","l","m","n","o","p","q","r","s","t","u"]`,
	} {
		body := `{"name":"tagged","tags":` + raw + `}`
		f.request(t, "POST", "/nodes", `{"name":"new","tags":`+raw+`}`, 400)
		f.request(t, "PUT", path, body, 400)
	}
	stored := handlerData[model.GostNode](t, f.request(t, "GET", path, "", 200))
	if !slices.Equal(stored.Tags, []string{"existing"}) {
		t.Fatalf("invalid tags changed persisted tags: %v", stored.Tags)
	}
}
