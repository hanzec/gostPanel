package dto

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestNodeManualAddressesJSON(t *testing.T) {
	for _, test := range []struct {
		name    string
		input   string
		want    NodeManualAddresses
		wantErr bool
	}{
		{"IPv4", `["203.0.113.8","192.0.2.9"]`, NodeManualAddresses{"203.0.113.8", "192.0.2.9"}, false},
		{"IPv6", `["2001:0db8:0:0::8"]`, NodeManualAddresses{"2001:db8::8"}, false},
		{"normalize and deduplicate", `[" 203.0.113.8 ","::ffff:203.0.113.8","2001:0db8:0:0::8","2001:db8::8"]`, NodeManualAddresses{"203.0.113.8", "2001:db8::8"}, false},
		{"clear", `[]`, NodeManualAddresses{}, false},
		{"null marker", `null`, nil, false},
		{"scalar", `"203.0.113.8"`, nil, true},
		{"object", `{}`, nil, true},
		{"number entry", `[7]`, nil, true},
		{"null entry", `[null]`, nil, true},
		{"empty entry", `[""]`, nil, true},
		{"invalid address", `["not-an-ip"]`, nil, true},
		{"port", `["192.0.2.1:3333"]`, nil, true},
		{"network", `["192.0.2.0/24"]`, nil, true},
		{"zone", `["fe80::1%en0"]`, nil, true},
		{"valid then invalid", `["203.0.113.8","invalid"]`, nil, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			original := NodeManualAddresses{"192.0.2.99"}
			addresses := append(NodeManualAddresses{}, original...)
			err := json.Unmarshal([]byte(test.input), &addresses)
			if (err != nil) != test.wantErr {
				t.Fatalf("decode %s returned %v, want error %t", test.input, err, test.wantErr)
			}
			want := test.want
			if test.wantErr {
				want = original
			}
			// Distinguish [] (clear) from nil (leave the persisted values unchanged).
			if !reflect.DeepEqual(addresses, want) {
				t.Fatalf("decoded addresses = %#v, want %#v", addresses, want)
			}
		})
	}
}
