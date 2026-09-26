// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"
	"unicode"
	"unicode/utf8"
)

// checkInfo fails when a string in info is not safe to pass on: invalid
// UTF-8, a control or format character, or longer than its limit.
func checkInfo(t *testing.T, info *ServerInfo) {
	t.Helper()
	if info == nil {
		return
	}
	v := reflect.ValueOf(*info)
	for i := 0; i < v.NumField(); i++ {
		f := v.Type().Field(i)
		if !f.IsExported() {
			continue
		}
		limit := rawMax
		if f.Name == "Icon" {
			limit = iconMax
		}
		switch f.Type.Kind() {
		case reflect.String:
			checkText(t, f.Name, v.Field(i).String(), limit)
		case reflect.Slice:
			if f.Type.Elem().Kind() != reflect.String {
				continue
			}
			if v.Field(i).Len() > sampleMax {
				t.Fatalf("%s: %d entries", f.Name, v.Field(i).Len())
			}
			for j := 0; j < v.Field(i).Len(); j++ {
				checkText(t, f.Name, v.Field(i).Index(j).String(), limit)
			}
		}
	}
}

func checkText(t *testing.T, name, s string, limit int) {
	t.Helper()
	if len(s) > limit || !utf8.ValidString(s) {
		t.Fatalf("%s: %d bytes or invalid UTF-8: %q", name, len(s), s)
	}
	for _, r := range s {
		if r != '\n' && (unicode.IsControl(r) || unicode.Is(unicode.Cf, r)) {
			t.Fatalf("%s holds %U: %q", name, r, s)
		}
	}
}

func FuzzParsePong(f *testing.F) {
	pong := func(payload string) []byte {
		b := append([]byte{raknetUnconnectedPong}, make([]byte, 16)...)
		b = append(b, raknetMagic...)
		b = append(b, byte(len(payload)>>8), byte(len(payload)))
		return append(b, payload...)
	}
	f.Add(pong("MCPE;Name;766;1.21.50;3;20;123;World;Survival;1;19132;19133;"))
	f.Add(pong("MCPE;\xff\x00" + string(rune(0x202e)) + ";;;;;;;;;;;"))
	f.Add(pong(""))
	f.Add([]byte{raknetUnconnectedPong})
	f.Fuzz(func(t *testing.T, b []byte) {
		info, _ := parsePong(b)
		checkInfo(t, info)
	})
}

func FuzzParseJavaStatus(f *testing.F) {
	f.Add([]byte(fakeStatus))
	f.Add([]byte(`{"description":[{"text":"a","color":"#12ab34","extra":[["b",{"translate":"k","fallback":"c"}]]}],"version":{"name":7}}`))
	f.Add([]byte(`{"description":{"text":"\u202eevil\u0000"},"contact":"\ud800","favicon":"data:image/png;base64,===="}`))
	f.Add([]byte(`{"version":{"name":"x"},"players":{"sample":[{"name":"\u0007a"},{"name":7},"b",{"name":"` + strings.Repeat("n", 100) + `"}]},"enforcesSecureChat":"yes"}`))
	f.Fuzz(func(t *testing.T, raw []byte) {
		info, _ := parseJavaStatus(raw)
		checkInfo(t, info)
	})
}

func FuzzParseNetherNetStatus(f *testing.F) {
	f.Add([]byte(joinStatus))
	f.Add([]byte(`{"name":"\u0007x","protocol":"99999999999999999999","gameType":-1,"players":1e300}`))
	f.Fuzz(func(t *testing.T, raw []byte) {
		info, _ := parseNetherNetStatus(raw)
		checkInfo(t, info)
	})
}

// A probe that panics fails; the backend goes on.
func TestProbePanic(t *testing.T) {
	boom := transport{name: "boom", network: "tcp", probe: func(context.Context, string, target) (*ServerInfo, time.Duration, error) {
		var b []byte
		_ = b[1]
		return nil, 0, nil
	}}
	res := ping(context.Background(), edition{transports: []transport{boom}}, target{ip: net.ParseIP("127.0.0.1"), port: 1})
	if res.State != "offline" || res.Error != "invalid data (unreadable answer)" {
		t.Errorf("%+v", res)
	}
}

// A NetherNet answer with more header than a status needs is refused.
func TestNetherNetHeaderLimit(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Filler", strings.Repeat("a", 4*netherNetMaxBody))
		_ = json.NewEncoder(w).Encode(map[string]string{"name": "x"})
	}))
	defer srv.Close()
	if info, _, err := pingNetherNet(context.Background(), "tcp4", joinTarget(t, srv.Listener.Addr().String())); err == nil {
		t.Errorf("accepted: %+v", info)
	}
}
