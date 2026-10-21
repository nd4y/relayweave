package config

import (
	"strings"
	"testing"
)

const valid = `{"cores":[{"id":"one","version":"1","binary":"/xray"}],"routes":[{"id":"one","core":"one","endpoint":"127.0.0.1:19001","outbound":{"protocol":"vless"}}],"listeners":[{"id":"socks","protocol":"socks5","listen":"0.0.0.0:1080","udp":true}],"udp":{"bind":"0.0.0.0","advertise":"127.0.0.1","port_min":30000,"port_max":30063},"state_dir":"/state","metrics_listen":"127.0.0.1:9090","probe_url":"http://origin/healthz"}`

func TestConfigBoundaries(t *testing.T) {
	if _, e := Decode(strings.NewReader(valid)); e != nil {
		t.Fatal(e)
	}
	for _, bad := range []string{valid + `{}`, strings.Replace(valid, `"version":"1"`, `"version":"1","typo":true`, 1), strings.Replace(valid, "127.0.0.1:19001", "0.0.0.0:19001", 1), strings.Replace(valid, `"core":"one"`, `"core":"missing"`, 1), strings.Replace(valid, `"protocol":"socks5"`, `"protocol":"http"`, 1), strings.Replace(valid, `"port_max":30063`, `"port_max":65536`, 1), strings.Replace(valid, `"id":"socks"`, `"id":"../bad"`, 1), strings.Replace(valid, `"udp":true`, `"udp":true,"password":"alone"`, 1)} {
		if _, e := Decode(strings.NewReader(bad)); e == nil {
			t.Errorf("accepted invalid configuration: %s", bad)
		}
	}
}
