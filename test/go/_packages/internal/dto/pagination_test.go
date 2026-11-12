package dto

import "testing"

func TestListDefaultsPreserveExplicitPagination(t *testing.T) {
	for _, test := range []struct {
		name     string
		pageSize int
		apply    func(page, size int) (int, int)
	}{
		{"logs", 20, func(page, size int) (int, int) {
			r := LogListReq{Page: page, PageSize: size}
			r.SetDefaults()
			return r.Page, r.PageSize
		}},
		{"nodes", 10, func(page, size int) (int, int) {
			r := NodeListReq{Page: page, PageSize: size}
			r.SetDefaults()
			return r.Page, r.PageSize
		}},
		{"rules", 10, func(page, size int) (int, int) {
			r := RuleListReq{Page: page, PageSize: size}
			r.SetDefaults()
			return r.Page, r.PageSize
		}},
		{"tunnels", 10, func(page, size int) (int, int) {
			r := TunnelListReq{Page: page, PageSize: size}
			r.SetDefaults()
			return r.Page, r.PageSize
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			if page, size := test.apply(0, 0); page != 1 || size != test.pageSize {
				t.Fatalf("default pagination = (%d, %d), want (1, %d)", page, size, test.pageSize)
			}
			if page, size := test.apply(3, 12); page != 3 || size != 12 {
				t.Fatalf("explicit pagination changed to (%d, %d)", page, size)
			}
		})
	}
}
