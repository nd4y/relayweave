package catalog

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
	"unicode/utf8"

	"gopkg.in/yaml.v3"
)

type Parsed struct {
	Format   string    `json:"format"`
	Profiles []Profile `json:"profiles"`
	Warnings []string  `json:"warnings"`
}

func unbase64(s string) ([]byte, error) {
	s = strings.TrimSpace(s)
	for _, encoding := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding} {
		if b, e := encoding.DecodeString(s); e == nil {
			return b, nil
		}
	}
	return nil, fmt.Errorf("invalid base64")
}
func fingerprint(v any) string {
	b, _ := json.Marshal(v)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:16])
}
func Parse(body []byte) (Parsed, error) {
	result := Parsed{Profiles: []Profile{}, Warnings: []string{}}
	if len(body) == 0 || len(body) > 8<<20 {
		return result, fmt.Errorf("empty subscription or size limit")
	}
	body = bytes.TrimSpace(bytes.TrimPrefix(body, []byte{0xef, 0xbb, 0xbf}))
	if len(body) == 0 {
		return result, fmt.Errorf("empty subscription after whitespace normalization")
	}
	if bytes.HasPrefix(bytes.ToLower(body), []byte("<!doctype")) || bytes.HasPrefix(bytes.ToLower(body), []byte("<html")) {
		return result, fmt.Errorf("HTML is not a subscription")
	}
	if !bytes.Contains(body, []byte("://")) && body[0] != '{' && body[0] != '[' && !bytes.Contains(body, []byte("proxies:")) {
		if decoded, e := unbase64(string(body)); e == nil {
			body = bytes.TrimSpace(decoded)
			result.Format = "base64"
		}
	}
	if len(body) == 0 {
		return result, fmt.Errorf("empty decoded subscription")
	}
	var err error
	switch body[0] {
	case '{', '[':
		result.Format = "json"
		result.Profiles, err = parseJSON(body)
	default:
		if bytes.Contains(body, []byte("proxies:")) {
			result.Format = "mihomo"
			result.Profiles, err = parseYAML(body)
		} else {
			if result.Format == "" {
				result.Format = "uri"
			}
			for _, line := range strings.Split(string(body), "\n") {
				line = strings.TrimSpace(line)
				if line == "" || strings.HasPrefix(line, "#") {
					continue
				}
				p, e := parseURI(line)
				if e != nil {
					result.Warnings = append(result.Warnings, "unsupported_or_invalid_entry")
					continue
				}
				result.Profiles = append(result.Profiles, p)
			}
		}
	}
	if err != nil {
		return result, err
	}
	if len(result.Profiles) > 10000 {
		return result, fmt.Errorf("profile limit exceeded")
	}
	if len(result.Profiles) == 0 {
		return result, fmt.Errorf("subscription contains no usable profile records")
	}
	for i := range result.Profiles {
		p := &result.Profiles[i]
		p.Enabled = true
		p.Country = country(p.Name)
		p.CountryInferred = p.Country != ""
		p.Identity = fingerprint([]any{p.Protocol, p.Host, p.Port, p.Transport, p.Security})
		p.Fingerprint = fingerprint(json.RawMessage(p.Outbound))
		if p.Name == "" {
			p.Name = fmt.Sprintf("%s %s:%d", p.Protocol, p.Host, p.Port)
		}
	}
	return result, nil
}
func country(name string) string {
	r := []rune(name)
	for i := 0; i+1 < len(r); i++ {
		if r[i] >= 0x1f1e6 && r[i] <= 0x1f1ff && r[i+1] >= 0x1f1e6 && r[i+1] <= 0x1f1ff {
			return string([]rune{'A' + r[i] - 0x1f1e6, 'A' + r[i+1] - 0x1f1e6})
		}
	}
	for _, s := range strings.FieldsFunc(strings.ToUpper(name), func(r rune) bool { return r < 'A' || r > 'Z' }) {
		if contains([]string{"RU", "DE", "NL", "FI", "SE", "US", "GB", "FR", "PL", "TR", "KZ", "JP", "SG", "HK", "CA", "CH", "AT", "CZ", "EE", "LV", "LT", "NO", "DK", "IT", "ES", "PT", "RO", "AM", "GE", "AU", "IL", "KR", "TW"}, s) {
			return s
		}
	}
	return ""
}
func parseURI(raw string) (Profile, error) {
	u, e := url.Parse(raw)
	if e != nil || u.Scheme == "" {
		return Profile{}, fmt.Errorf("invalid URI")
	}
	p := Profile{Name: u.Fragment, Protocol: strings.ToLower(u.Scheme), Transport: "tcp", Security: "none", Supported: true, UDP: true}
	if p.Protocol == "vmess" {
		part := strings.TrimPrefix(raw, "vmess://")
		if n := strings.IndexByte(part, '#'); n >= 0 {
			part = part[:n]
		}
		b, e := unbase64(part)
		if e != nil {
			return p, e
		}
		var v map[string]any
		if e = json.Unmarshal(b, &v); e != nil {
			return p, e
		}
		return vmessJSON(v)
	}
	if p.Protocol == "ss" {
		if u.Hostname() == "" {
			part := strings.TrimPrefix(raw, "ss://")
			if n := strings.IndexByte(part, '#'); n >= 0 {
				part = part[:n]
			}
			decoded, e := unbase64(part)
			if e != nil {
				return p, e
			}
			u, e = url.Parse("ss://" + string(decoded))
			if e != nil {
				return p, e
			}
		}
	}
	p.Host = u.Hostname()
	p.Port, _ = strconv.Atoi(u.Port())
	if p.Port == 0 && (p.Protocol == "trojan" || u.Query().Get("security") == "tls") {
		p.Port = 443
	}
	if p.Host == "" || p.Port < 1 || p.Port > 65535 || len(p.Name) > 512 || !utf8.ValidString(p.Name) {
		return p, fmt.Errorf("invalid endpoint")
	}
	q := u.Query()
	transport := q.Get("type")
	if transport == "" {
		transport = "tcp"
	}
	p.Transport = transport
	p.Security = q.Get("security")
	if p.Security == "" {
		p.Security = "none"
	}
	if p.Protocol == "trojan" && p.Security == "none" {
		p.Security = "tls"
	}
	settings := map[string]any{}
	password := ""
	if u.User != nil {
		password = u.User.Username()
	}
	switch p.Protocol {
	case "vless":
		settings["vnext"] = []any{map[string]any{"address": p.Host, "port": p.Port, "users": []any{map[string]any{"id": password, "encryption": "none", "flow": q.Get("flow")}}}}
	case "trojan":
		settings["servers"] = []any{map[string]any{"address": p.Host, "port": p.Port, "password": password}}
	case "ss":
		p.Protocol = "shadowsocks"
		userpass := password
		if u.User != nil {
			if pass, ok := u.User.Password(); ok {
				userpass = password + ":" + pass
			} else if b, e := unbase64(password); e == nil {
				userpass = string(b)
			}
		}
		method, pass, ok := strings.Cut(userpass, ":")
		if !ok {
			return p, fmt.Errorf("invalid Shadowsocks identity")
		}
		settings["servers"] = []any{map[string]any{"address": p.Host, "port": p.Port, "method": method, "password": pass}}
	case "socks", "socks5":
		p.Protocol = "socks"
		server := map[string]any{"address": p.Host, "port": p.Port}
		if u.User != nil {
			pass, _ := u.User.Password()
			server["users"] = []any{map[string]any{"user": u.User.Username(), "pass": pass}}
		}
		settings["servers"] = []any{server}
	case "http", "https":
		p.Protocol = "http"
		p.UDP = false
		if u.Scheme == "https" {
			p.Security = "tls"
		}
		server := map[string]any{"address": p.Host, "port": p.Port}
		if u.User != nil {
			pass, _ := u.User.Password()
			server["users"] = []any{map[string]any{"user": u.User.Username(), "pass": pass}}
		}
		settings["servers"] = []any{server}
	default:
		p.Supported = false
		p.Reason = "protocol_requires_another_engine"
	}
	stream := map[string]any{"network": transport, "security": p.Security}
	sni := q.Get("sni")
	if sni == "" {
		sni = q.Get("peer")
	}
	if sni == "" {
		sni = p.Host
	}
	if p.Security == "tls" {
		tls := map[string]any{"serverName": sni, "allowInsecure": q.Get("allowInsecure") == "1" || q.Get("allowInsecure") == "true"}
		if fp := q.Get("fp"); fp != "" {
			tls["fingerprint"] = fp
		}
		if alpn := q.Get("alpn"); alpn != "" {
			tls["alpn"] = strings.Split(alpn, ",")
		}
		stream["tlsSettings"] = tls
	}
	if p.Security == "reality" {
		stream["realitySettings"] = map[string]any{"serverName": sni, "fingerprint": q.Get("fp"), "publicKey": q.Get("pbk"), "shortId": q.Get("sid"), "spiderX": q.Get("spx")}
	}
	switch transport {
	case "tcp", "raw":
	case "ws":
		stream["wsSettings"] = map[string]any{"path": q.Get("path"), "headers": map[string]string{"Host": q.Get("host")}}
	case "grpc":
		stream["grpcSettings"] = map[string]any{"serviceName": q.Get("serviceName"), "authority": q.Get("authority"), "multiMode": q.Get("mode") == "multi"}
	case "httpupgrade":
		stream["httpupgradeSettings"] = map[string]any{"path": q.Get("path"), "host": q.Get("host")}
	case "xhttp":
		stream["xhttpSettings"] = map[string]any{"path": q.Get("path"), "host": q.Get("host"), "mode": q.Get("mode")}
	default:
		p.Supported = false
		p.Reason = "unsupported_transport"
	}
	if q.Get("plugin") != "" {
		p.Supported = false
		p.Reason = "external_plugin_not_supported"
	}
	p.Outbound, _ = json.Marshal(map[string]any{"protocol": p.Protocol, "settings": settings, "streamSettings": stream})
	return p, nil
}
func vmessJSON(v map[string]any) (Profile, error) {
	str := func(k string) string { return fmt.Sprint(v[k]) }
	port, _ := strconv.Atoi(str("port"))
	aid, _ := strconv.Atoi(str("aid"))
	network := str("net")
	if network == "<nil>" || network == "" {
		network = "tcp"
	}
	security := "none"
	if str("tls") == "tls" {
		security = "tls"
	}
	p := Profile{Name: str("ps"), Protocol: "vmess", Host: str("add"), Port: port, Transport: network, Security: security, UDP: true, Supported: true}
	if p.Port < 1 || p.Host == "" || p.Host == "<nil>" {
		return p, fmt.Errorf("invalid VMess endpoint")
	}
	stream := map[string]any{"network": network, "security": security}
	if security == "tls" {
		sni := str("sni")
		if sni == "<nil>" || sni == "" {
			sni = p.Host
		}
		stream["tlsSettings"] = map[string]any{"serverName": sni}
	}
	if network == "ws" {
		stream["wsSettings"] = map[string]any{"path": str("path"), "headers": map[string]any{"Host": str("host")}}
	}
	if network == "grpc" {
		stream["grpcSettings"] = map[string]any{"serviceName": str("path")}
	}
	settings := map[string]any{"vnext": []any{map[string]any{"address": p.Host, "port": port, "users": []any{map[string]any{"id": str("id"), "alterId": aid, "security": "auto"}}}}}
	p.Outbound, _ = json.Marshal(map[string]any{"protocol": "vmess", "settings": settings, "streamSettings": stream})
	return p, nil
}
func parseJSON(body []byte) ([]Profile, error) {
	var value any
	d := json.NewDecoder(bytes.NewReader(body))
	d.UseNumber()
	if e := d.Decode(&value); e != nil {
		return nil, e
	}
	var records []any
	switch v := value.(type) {
	case []any:
		records = v
	case map[string]any:
		records = []any{v}
	default:
		return nil, fmt.Errorf("subscription must be an object or array")
	}
	out := []Profile{}
	for _, raw := range records {
		v, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		if list, ok := v["outbounds"].([]any); ok {
			for _, raw := range list {
				ob, ok := raw.(map[string]any)
				if !ok {
					continue
				}
				if _, ok := ob["type"]; ok {
					if p, e := singbox(ob); e == nil {
						out = append(out, p)
					}
				} else if p, e := xrayOutbound(ob, stringValue(v["remarks"])); e == nil {
					out = append(out, p)
				}
			}
		} else if _, ok := v["protocol"]; ok {
			if p, e := xrayOutbound(v, stringValue(v["remarks"])); e == nil {
				out = append(out, p)
			}
		} else if _, ok := v["type"]; ok {
			if p, e := singbox(v); e == nil {
				out = append(out, p)
			}
		}
	}
	return out, nil
}
func stringValue(v any) string {
	if v == nil {
		return ""
	}
	return fmt.Sprint(v)
}
func number(v any) int { i, _ := strconv.Atoi(stringValue(v)); return i }
func xrayOutbound(ob map[string]any, name string) (Profile, error) {
	proto := stringValue(ob["protocol"])
	if proto == "freedom" || proto == "blackhole" || proto == "dns" || proto == "loopback" {
		return Profile{}, fmt.Errorf("non-proxy outbound")
	}
	p := Profile{Name: name, Protocol: proto, Transport: "tcp", Security: "none", UDP: proto != "http", Supported: contains([]string{"vless", "vmess", "trojan", "shadowsocks", "http", "socks"}, proto)}
	if p.Name == "" {
		p.Name = stringValue(ob["tag"])
	}
	settings, _ := ob["settings"].(map[string]any)
	for _, key := range []string{"vnext", "servers"} {
		if list, ok := settings[key].([]any); ok && len(list) > 0 {
			if first, ok := list[0].(map[string]any); ok {
				p.Host = stringValue(first["address"])
				p.Port = number(first["port"])
			}
			if len(list) > 1 {
				p.Supported = false
				p.Reason = "multi_server_outbound_requires_split"
			}
		}
	}
	if p.Host == "" {
		p.Host = stringValue(settings["address"])
		p.Port = number(settings["port"])
	}
	if stream, ok := ob["streamSettings"].(map[string]any); ok {
		if n := stringValue(stream["network"]); n != "" {
			p.Transport = n
		}
		if n := stringValue(stream["security"]); n != "" {
			p.Security = n
		}
		for _, key := range []string{"tlsSettings", "realitySettings"} {
			if security, ok := stream[key].(map[string]any); ok {
				if unsafeFileReference(security) {
					p.Supported = false
					p.Reason = "subscription_file_reference_rejected"
				}
			}
		}
		if sockopt, ok := stream["sockopt"].(map[string]any); ok {
			if stringValue(sockopt["dialerProxy"]) != "" {
				p.Supported = false
				p.Reason = "outbound_dependency_requires_review"
			}
		}
	}
	if unsafeFileReference(ob) {
		p.Supported = false
		p.Reason = "subscription_file_reference_rejected"
	}
	if ob["proxySettings"] != nil {
		p.Supported = false
		p.Reason = "outbound_dependency_requires_review"
	}
	if !p.Supported && p.Reason == "" {
		p.Reason = "protocol_requires_another_engine"
	}
	if p.Host == "" || p.Port < 1 || p.Port > 65535 {
		return p, fmt.Errorf("invalid outbound endpoint")
	}
	delete(ob, "tag")
	p.Outbound, _ = json.Marshal(ob)
	return p, nil
}
func unsafeFileReference(v any) bool {
	switch x := v.(type) {
	case map[string]any:
		for k, v := range x {
			switch strings.ToLower(k) {
			case "certificatefile", "keyfile", "keylog", "keylogfile", "masterkeylog", "masterkeylogfile":
				if stringValue(v) != "" {
					return true
				}
			}
			if unsafeFileReference(v) {
				return true
			}
		}
	case []any:
		for _, v := range x {
			if unsafeFileReference(v) {
				return true
			}
		}
	}
	return false
}
func singbox(v map[string]any) (Profile, error) {
	proto := stringValue(v["type"])
	if contains([]string{"direct", "block", "dns", "selector", "urltest"}, proto) {
		return Profile{}, fmt.Errorf("not a proxy")
	}
	q := url.Values{}
	q.Set("type", "tcp")
	secret := stringValue(v["uuid"])
	if secret == "" {
		secret = stringValue(v["password"])
	}
	scheme := proto
	if proto == "shadowsocks" {
		scheme = "ss"
		secret = stringValue(v["method"]) + ":" + secret
	}
	if tls, ok := v["tls"].(map[string]any); ok && tls["enabled"] == true {
		q.Set("security", "tls")
		q.Set("sni", stringValue(tls["server_name"]))
		if tls["insecure"] == true {
			q.Set("allowInsecure", "1")
		}
		if reality, ok := tls["reality"].(map[string]any); ok && reality["enabled"] == true {
			q.Set("security", "reality")
			q.Set("pbk", stringValue(reality["public_key"]))
			q.Set("sid", stringValue(reality["short_id"]))
		}
		if utls, ok := tls["utls"].(map[string]any); ok {
			q.Set("fp", stringValue(utls["fingerprint"]))
		}
	}
	if tr, ok := v["transport"].(map[string]any); ok {
		q.Set("type", stringValue(tr["type"]))
		q.Set("path", stringValue(tr["path"]))
		q.Set("serviceName", stringValue(tr["service_name"]))
		if h, ok := tr["headers"].(map[string]any); ok {
			q.Set("host", stringValue(h["Host"]))
		}
	}
	q.Set("flow", stringValue(v["flow"]))
	if proto == "vmess" {
		return vmessJSON(map[string]any{"ps": v["tag"], "add": v["server"], "port": v["server_port"], "id": v["uuid"], "aid": v["alter_id"], "net": q.Get("type"), "tls": q.Get("security"), "sni": q.Get("sni"), "path": q.Get("path"), "host": q.Get("host")})
	}
	u := url.URL{Scheme: scheme, Host: net.JoinHostPort(stringValue(v["server"]), strconv.Itoa(number(v["server_port"]))), User: url.User(secret), Fragment: stringValue(v["tag"]), RawQuery: q.Encode()}
	if proto == "http" || proto == "socks" {
		u.User = url.UserPassword(stringValue(v["username"]), stringValue(v["password"]))
	}
	return parseURI(u.String())
}
func parseYAML(body []byte) ([]Profile, error) {
	var node yaml.Node
	d := yaml.NewDecoder(bytes.NewReader(body))
	if e := d.Decode(&node); e != nil {
		return nil, e
	}
	budget := 50000
	visiting := map[*yaml.Node]bool{}
	var walk func(*yaml.Node, int) error
	walk = func(n *yaml.Node, depth int) error {
		budget--
		if budget < 0 || depth > 32 || visiting[n] {
			return fmt.Errorf("YAML complexity limit")
		}
		visiting[n] = true
		defer delete(visiting, n)
		if n.Alias != nil {
			if e := walk(n.Alias, depth+1); e != nil {
				return e
			}
		}
		for _, child := range n.Content {
			if e := walk(child, depth+1); e != nil {
				return e
			}
		}
		return nil
	}
	if e := walk(&node, 0); e != nil {
		return nil, e
	}
	var doc map[string]any
	if e := node.Decode(&doc); e != nil {
		return nil, e
	}
	proxies, _ := doc["proxies"].([]any)
	out := []Profile{}
	for _, raw := range proxies {
		v, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		converted := map[string]any{"type": v["type"], "tag": v["name"], "server": v["server"], "server_port": v["port"], "uuid": v["uuid"], "password": v["password"], "username": v["username"], "method": v["cipher"], "flow": v["flow"], "alter_id": v["alterId"]}
		proto := stringValue(v["type"])
		if proto == "ss" {
			converted["type"] = "shadowsocks"
		}
		if proto == "socks5" {
			converted["type"] = "socks"
		}
		if v["tls"] == true || v["type"] == "trojan" {
			tls := map[string]any{"enabled": true, "server_name": v["servername"], "insecure": v["skip-cert-verify"]}
			if tls["server_name"] == nil {
				tls["server_name"] = v["sni"]
			}
			if reality, ok := v["reality-opts"].(map[string]any); ok {
				tls["reality"] = map[string]any{"enabled": true, "public_key": reality["public-key"], "short_id": reality["short-id"]}
				tls["utls"] = map[string]any{"fingerprint": v["client-fingerprint"]}
			}
			converted["tls"] = tls
		}
		if network := stringValue(v["network"]); network != "" {
			tr := map[string]any{"type": network}
			if ws, ok := v["ws-opts"].(map[string]any); ok {
				tr["path"] = ws["path"]
				tr["headers"] = ws["headers"]
			}
			if gr, ok := v["grpc-opts"].(map[string]any); ok {
				tr["service_name"] = gr["grpc-service-name"]
			}
			converted["transport"] = tr
		}
		p, e := singbox(converted)
		if e == nil {
			out = append(out, p)
		}
	}
	return out, nil
}
