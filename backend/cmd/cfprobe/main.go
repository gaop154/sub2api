// cfprobe: 验证 sub2api 的 tlsfingerprint（utls）能否用 Chrome 指纹绕过 console.x.ai 的 CF。
// 临时 PoC 验证程序（不进产品），验证 console.x.ai 的 CF 是指纹级还是 IP/HTTP2 级，验证后会删除。
//
// 用法（在 backend 目录）:
//   go run ./cmd/cfprobe                      # 直连
//   go run ./cmd/cfprobe -proxy http://h:p    # 走 HTTP 代理（住宅/海外）
//
// 判读: STATUS=401 → CF 放行（指纹过），只差有效 SSO ✅
//       STATUS=403 + html → CF 仍拦截（指纹不够 / IP 被挡 / 需 HTTP2 指纹）❌
package main

import (
	"bytes"
	"context"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
)

// chromeProfile 构造 Chrome 风格的 TLS 指纹（用于探测 CF 是否放行 utls 模拟的 Chrome ClientHello）。
// 字段值参照公开的 Chrome 13x JA3/JA4；扩展 27(compress_certificate)/21(padding) 经 buildClientHelloSpecFromProfile
// 落为 GenericExtension（payload 不完全精确），是已知精度风险点。
func chromeProfile() *tlsfingerprint.Profile {
	return &tlsfingerprint.Profile{
		Name:         "chrome-probe",
		EnableGREASE: true,
		CipherSuites: []uint16{
			0x0a0a, // GREASE
			0x1301, 0x1302, 0x1303, // TLS 1.3
			0xc02b, 0xc02f, 0xc02c, 0xc030, // ECDHE AES-GCM
			0xcca9, 0xcca8, // ECDHE ChaCha20
			0xc009, 0xc013, 0xc00a, 0xc014, // ECDHE AES-CBC
			0x009c, 0x009d, 0x002f, 0x0035, // RSA
		},
		Curves:              []uint16{29, 23, 24}, // X25519, secp256r1, secp384r1
		PointFormats:        []uint16{0},
		SignatureAlgorithms: []uint16{0x0403, 0x0804, 0x0401, 0x0503, 0x0805, 0x0501, 0x0806, 0x0601, 0x0201},
		ALPNProtocols:       []string{"http/1.1"}, // 先 HTTP/1.1，隔离 TLS 指纹因素（避免 Go HTTP/2 帧顺序干扰）
		SupportedVersions:   []uint16{0x0304, 0x0303},
		KeyShareGroups:      []uint16{29},
		PSKModes:            []uint16{1},
		Extensions: []uint16{
			0x0a0a, // GREASE
			0,      // server_name
			23,     // extended_master_secret
			0xff01, // renegotiation_info
			10,     // supported_groups
			11,     // ec_point_formats
			35,     // session_ticket
			16,     // alpn
			5,      // status_request
			13,     // signature_algorithms
			18,     // sct
			51,     // key_share
			45,     // psk_key_exchange_modes
			43,     // supported_versions
			0x2a2a, // GREASE
			21,     // padding (GenericExtension)
		},
	}
}

func main() {
	sso := flag.String("sso", "", "SSO token (sso == sso-rw); empty = fake token (CF-only probe)")
	ssoFile := flag.String("sso-file", "", "grok-register accounts 文件 (email----password----sso)，读第一行的 sso")
	proxyStr := flag.String("proxy", "", "HTTP proxy URL (optional)")
	flag.Parse()

	profile := chromeProfile()
	var dialTLS func(ctx context.Context, network, addr string) (net.Conn, error)
	if *proxyStr != "" {
		pu, err := url.Parse(*proxyStr)
		if err != nil {
			fmt.Println("bad proxy:", err)
			return
		}
		dialTLS = tlsfingerprint.NewHTTPProxyDialer(profile, pu).DialTLSContext
	} else {
		dialTLS = tlsfingerprint.NewDialer(profile, nil).DialTLSContext
	}

	tr := &http.Transport{DialTLSContext: dialTLS, ForceAttemptHTTP2: false}
	client := &http.Client{Transport: tr, Timeout: 40 * time.Second}

	token := *sso
	if token == "" && *ssoFile != "" {
		t, err := readSSOFromFile(*ssoFile)
		if err != nil {
			fmt.Println("read sso-file:", err)
			return
		}
		token = t
	}
	if token == "" {
		token = "fake-probe-token"
	}
	// multi-agent 搜索 body（已 normalize 形态，对照 forwarder normalizeGrokSearchRequestBody 输出）
	body := bytes.NewReader([]byte(`{"model":"grok-4.20-multi-agent-0309","input":[{"role":"user","content":[{"type":"input_text","text":"搜一下今天的新闻"}]}],"store":false,"max_output_tokens":2000000,"reasoning":{"effort":"medium"},"include":["reasoning.encrypted_content"],"tools":[{"type":"web_search","enable_image_understanding":true},{"type":"x_search","enable_video_understanding":true}],"tool_choice":"auto","stream":true}`))
	req, err := http.NewRequest(http.MethodPost, "https://console.x.ai/v1/responses", body)
	if err != nil {
		fmt.Println("new req:", err)
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("Cookie", "sso="+token+"; sso-rw="+token) // 真 SSO（-sso）或假 token（仅测 CF）
	req.Header.Set("Authorization", "Bearer anonymous")
	req.Header.Set("Origin", "https://console.x.ai")
	req.Header.Set("Referer", "https://console.x.ai/")
	req.Header.Set("x-cluster", "https://us-east-1.api.x.ai")
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36")
	req.Header.Set("Sec-Ch-Ua", `"Google Chrome";v="131", "Chromium";v="131", "Not_A Brand";v="24"`)
	req.Header.Set("Sec-Ch-Ua-Mobile", "?0")
	req.Header.Set("Sec-Ch-Ua-Platform", `"Windows"`)

	resp, err := client.Do(req)
	if err != nil {
		fmt.Println("ERR:", err)
		return
	}
	defer func() { _ = resp.Body.Close() }()
	rb, _ := io.ReadAll(io.LimitReader(resp.Body, 1500))
	fmt.Printf("STATUS=%d PROTO=%s server=%q cf-ray=%q\n", resp.StatusCode, resp.Proto, resp.Header.Get("server"), resp.Header.Get("cf-ray"))
	fmt.Printf("BODY: %s\n", string(rb))
}

// readSSOFromFile 从 grok-register accounts 文件（email----password----sso）读第一行的 sso。
func readSSOFromFile(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	first := strings.SplitN(strings.TrimSpace(string(data)), "\n", 2)[0]
	parts := strings.SplitN(first, "----", 4)
	if len(parts) < 3 {
		return "", fmt.Errorf("no sso field in first line")
	}
	return strings.TrimSpace(parts[2]), nil
}
