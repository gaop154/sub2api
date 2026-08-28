package service

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// grokSearchDPoPUpstreamCall 记录 stub upstream 收到的一次请求（路径 + 出口）
type grokSearchDPoPUpstreamCall struct {
	path     string
	proxyURL string
}

// grokSearchDPoPMintUpstream 拦截 DPoP mint 与业务请求的 stub upstream：
// 记录每次请求收到的 proxyURL（验证出口一致性），并对 /dpop/token 返回
// 按请求体 jwk 绑定 thumbprint 的可用 token（parseGrokSearchDPoPAccessToken 不校验签名，
// 只解 payload，故签名段可为任意占位）。dateHeader 控制 mint 响应的 Date 头（空则不写）。
type grokSearchDPoPMintUpstream struct {
	mu         sync.Mutex
	dateHeader string
	mintCalls  []grokSearchDPoPUpstreamCall
	bizCalls   []grokSearchDPoPUpstreamCall
}

var _ HTTPUpstream = (*grokSearchDPoPMintUpstream)(nil)

func (u *grokSearchDPoPMintUpstream) Do(req *http.Request, proxyURL string, accountID int64, accountConcurrency int) (*http.Response, error) {
	return u.handle(req, proxyURL)
}

func (u *grokSearchDPoPMintUpstream) DoWithTLS(req *http.Request, proxyURL string, accountID int64, accountConcurrency int, profile *tlsfingerprint.Profile) (*http.Response, error) {
	return u.handle(req, proxyURL)
}

// handle 按路径分发：/dpop/token 走 mint（返回绑定 token），其余视为业务请求（直接 200）
func (u *grokSearchDPoPMintUpstream) handle(req *http.Request, proxyURL string) (*http.Response, error) {
	call := grokSearchDPoPUpstreamCall{path: req.URL.Path, proxyURL: proxyURL}

	if strings.HasSuffix(req.URL.Path, grokSearchDPoPTokenPath) {
		body, err := io.ReadAll(req.Body)
		_ = req.Body.Close()
		if err != nil {
			return nil, err
		}
		var mintReq struct {
			JWK grokSearchDPoPJWKStruct `json:"jwk"`
		}
		if err := json.Unmarshal(body, &mintReq); err != nil {
			return nil, err
		}
		thumbprint, err := grokSearchDPoPJWKThumbprint(mintReq.JWK)
		if err != nil {
			return nil, err
		}
		token, err := buildUnsignedGrokSearchDPoPTestToken(thumbprint, time.Now().Add(time.Hour))
		if err != nil {
			return nil, err
		}
		respBody, err := json.Marshal(map[string]any{
			"access_token": token,
			"token_type":   "DPoP",
			"expires_in":   1800,
		})
		if err != nil {
			return nil, err
		}
		header := http.Header{}
		header.Set("Content-Type", "application/json")
		if u.dateHeader != "" {
			header.Set("Date", u.dateHeader)
		}
		u.mu.Lock()
		u.mintCalls = append(u.mintCalls, call)
		u.mu.Unlock()
		return &http.Response{
			StatusCode: http.StatusOK,
			Status:     "200 OK",
			Header:     header,
			Body:       io.NopCloser(bytes.NewReader(respBody)),
			Request:    req,
		}, nil
	}

	u.mu.Lock()
	u.bizCalls = append(u.bizCalls, call)
	u.mu.Unlock()
	return &http.Response{
		StatusCode: http.StatusOK,
		Status:     "200 OK",
		Header:     http.Header{},
		Body:       io.NopCloser(strings.NewReader("")),
		Request:    req,
	}, nil
}

func (u *grokSearchDPoPMintUpstream) mintCount() int {
	u.mu.Lock()
	defer u.mu.Unlock()
	return len(u.mintCalls)
}

func (u *grokSearchDPoPMintUpstream) lastMint() grokSearchDPoPUpstreamCall {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.mintCalls[len(u.mintCalls)-1]
}

func (u *grokSearchDPoPMintUpstream) lastBiz() grokSearchDPoPUpstreamCall {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.bizCalls[len(u.bizCalls)-1]
}

// buildUnsignedGrokSearchDPoPTestToken 构造仅 payload 可解析的 JWT（三段式、签名段为占位），
// 绑定指定 thumbprint（cnf.jkt）与过期时间，用于 stub mint 响应
func buildUnsignedGrokSearchDPoPTestToken(thumbprint string, expiry time.Time) (string, error) {
	payload, err := json.Marshal(map[string]any{
		"exp": expiry.Unix(),
		"cnf": map[string]any{"jkt": thumbprint},
	})
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"ES256","typ":"JWT"}`)) + "." +
		base64.RawURLEncoding.EncodeToString(payload) + "." +
		base64.RawURLEncoding.EncodeToString([]byte("sig")), nil
}

// newGrokSearchDPoPEgressTestAccount 构造未配代理、走默认 base_url 的测试账号
func newGrokSearchDPoPEgressTestAccount() *Account {
	return &Account{
		ID:          3001,
		Name:        "dpop-egress-test",
		Platform:    PlatformGrokSearch,
		Credentials: map[string]any{"sso_token": "sso-egress-value"},
	}
}

// TestGrokSearchDPoPCacheKeyIncludesEgress 验证缓存键含出口标识（AC10）：
// 同出口同键；换代理即换键；空串（直连）也是一种出口，参与键构成
func TestGrokSearchDPoPCacheKeyIncludesEgress(t *testing.T) {
	account := newGrokSearchDPoPEgressTestAccount()
	baseURL := "https://console.x.ai"

	// 同 proxyURL → 同键
	assert.Equal(t,
		grokSearchDPoPSessionCacheKey(baseURL, account, "sso", "http://proxy-a:8080"),
		grokSearchDPoPSessionCacheKey(baseURL, account, "sso", "http://proxy-a:8080"),
		"相同出口应生成相同 cache key")

	// 不同 proxyURL → 不同键（换代理不复用旧出口的 session）
	assert.NotEqual(t,
		grokSearchDPoPSessionCacheKey(baseURL, account, "sso", "http://proxy-a:8080"),
		grokSearchDPoPSessionCacheKey(baseURL, account, "sso", "http://proxy-b:8080"),
		"不同出口应有不同 cache key")

	// 空串（直连）与代理出口也不同键
	assert.NotEqual(t,
		grokSearchDPoPSessionCacheKey(baseURL, account, "sso", ""),
		grokSearchDPoPSessionCacheKey(baseURL, account, "sso", "http://proxy-a:8080"),
		"直连与代理出口应有不同 cache key")

	// 空串与空串 → 同键
	assert.Equal(t,
		grokSearchDPoPSessionCacheKey(baseURL, account, "sso", ""),
		grokSearchDPoPSessionCacheKey(baseURL, account, "sso", ""),
		"直连出口应稳定生成相同 cache key")
}

// TestFetchGrokSearchDPoPSessionMintUsesGivenProxyURL 验证 mint 请求使用调用链传入的 proxyURL（AC10）：
// 不再固定空串直连，配代理账号的 mint 与业务请求保持同出口
func TestFetchGrokSearchDPoPSessionMintUsesGivenProxyURL(t *testing.T) {
	account := newGrokSearchDPoPEgressTestAccount()

	// 配代理：mint 应带代理
	stub := &grokSearchDPoPMintUpstream{}
	_, err := fetchGrokSearchDPoPSession(t.Context(), stub, account, "sso-egress-value", "http://proxy-a:8080")
	require.NoError(t, err)
	require.Equal(t, 1, stub.mintCount(), "应发生一次 mint")
	assert.Equal(t, "http://proxy-a:8080", stub.lastMint().proxyURL, "mint 请求应使用传入的 proxyURL")

	// 直连（空串）：mint 也应收到空串
	stubDirect := &grokSearchDPoPMintUpstream{}
	_, err = fetchGrokSearchDPoPSession(t.Context(), stubDirect, account, "sso-egress-value", "")
	require.NoError(t, err)
	require.Equal(t, 1, stubDirect.mintCount(), "应发生一次 mint")
	assert.Equal(t, "", stubDirect.lastMint().proxyURL, "直连时 mint 请求的 proxyURL 应为空串")
}

// TestDoGrokSearchDPoPRequestKeepsMintAndBusinessOnSameEgress 验证端到端出口一致性（AC10）：
// mint 与业务请求同出口；同出口命中缓存不重复 mint；换出口重新 mint
func TestDoGrokSearchDPoPRequestKeepsMintAndBusinessOnSameEgress(t *testing.T) {
	account := newGrokSearchDPoPEgressTestAccount()
	manager := newGrokSearchDPoPSessionManager()
	stub := &grokSearchDPoPMintUpstream{}
	proxyA := "http://proxy-a:8080"
	proxyB := "http://proxy-b:8080"
	bizURL := "https://console.x.ai/v1/responses"

	// 第一次（出口 A）：mint + 业务各一次，均走 proxyA
	resp, err := doGrokSearchDPoPRequest(t.Context(), stub, manager, account, "sso-egress-value", proxyA,
		http.MethodPost, bizURL, []byte(`{}`), "application/json")
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	_ = resp.Body.Close()
	require.Equal(t, 1, stub.mintCount(), "首次请求应触发一次 mint")
	assert.Equal(t, proxyA, stub.lastMint().proxyURL, "mint 应与业务请求同出口")
	assert.Equal(t, proxyA, stub.lastBiz().proxyURL, "业务请求应使用 proxyA")

	// 第二次（同出口 A）：session 缓存命中，不再 mint
	resp, err = doGrokSearchDPoPRequest(t.Context(), stub, manager, account, "sso-egress-value", proxyA,
		http.MethodPost, bizURL, []byte(`{}`), "application/json")
	require.NoError(t, err)
	_ = resp.Body.Close()
	assert.Equal(t, 1, stub.mintCount(), "同出口重复请求应命中缓存，不重复 mint")

	// 第三次（换出口 B）：缓存键含出口 → 重新 mint，且 mint 与业务均走 proxyB
	resp, err = doGrokSearchDPoPRequest(t.Context(), stub, manager, account, "sso-egress-value", proxyB,
		http.MethodPost, bizURL, []byte(`{}`), "application/json")
	require.NoError(t, err)
	_ = resp.Body.Close()
	require.Equal(t, 2, stub.mintCount(), "换出口应重新 mint")
	assert.Equal(t, proxyB, stub.lastMint().proxyURL, "换出口后 mint 应使用新 proxyURL")
	assert.Equal(t, proxyB, stub.lastBiz().proxyURL, "换出口后业务请求应使用新 proxyURL")
}

// TestGrokSearchDPoPClockSkewFromDateHeader 验证 Date 头时钟偏差学习（AC11）：
// 缺失/坏格式 → 0；正负偏差按服务器时间 − RTT 中点计算；本地窗口异常先收敛
func TestGrokSearchDPoPClockSkewFromDateHeader(t *testing.T) {
	local := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)

	// Date 缺失 → 0
	assert.Equal(t, time.Duration(0), grokSearchDPoPClockSkewFromDateHeader("", local, local),
		"空 Date 应返回 0")
	// Date 坏格式 → 0
	assert.Equal(t, time.Duration(0), grokSearchDPoPClockSkewFromDateHeader("not-a-date", local, local),
		"坏格式 Date 应返回 0")

	// 正偏差：服务器快 90s
	server := local.Add(90 * time.Second)
	assert.Equal(t, 90*time.Second,
		grokSearchDPoPClockSkewFromDateHeader(server.Format(http.TimeFormat), local, local),
		"服务器快 90s 应学习到 +90s")

	// 负偏差：服务器慢 120s
	server = local.Add(-120 * time.Second)
	assert.Equal(t, -120*time.Second,
		grokSearchDPoPClockSkewFromDateHeader(server.Format(http.TimeFormat), local, local),
		"服务器慢 120s 应学习到 -120s")

	// RTT 中点：窗口 [T, T+10s] 中点 T+5s，服务器 T+20s → skew = +15s
	localBefore := local
	localAfter := local.Add(10 * time.Second)
	server = local.Add(20 * time.Second)
	assert.Equal(t, 15*time.Second,
		grokSearchDPoPClockSkewFromDateHeader(server.Format(http.TimeFormat), localBefore, localAfter),
		"应取 RTT 窗口中点计算偏差")

	// localAfter 零值：窗口收敛为 [T, T]，服务器 T+8s → +8s
	server = local.Add(8 * time.Second)
	assert.Equal(t, 8*time.Second,
		grokSearchDPoPClockSkewFromDateHeader(server.Format(http.TimeFormat), localBefore, time.Time{}),
		"localAfter 零值应收敛到 localBefore")

	// localAfter 早于 localBefore（倒序）：同样收敛
	assert.Equal(t, 8*time.Second,
		grokSearchDPoPClockSkewFromDateHeader(server.Format(http.TimeFormat), localBefore, localBefore.Add(-3*time.Second)),
		"倒序窗口应收敛到 localBefore")
}

// TestGrokSearchDPoPProofIATAppliesClockSkew 验证 proof iat 应用 session 时钟偏差（AC11）
func TestGrokSearchDPoPProofIATAppliesClockSkew(t *testing.T) {
	local := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)

	// 负偏差
	session := grokSearchDPoPSession{clockSkew: -120 * time.Second}
	assert.Equal(t, local.Add(-120*time.Second).Unix(), grokSearchDPoPProofIAT(session, local),
		"iat 应叠加 session 时钟偏差")

	// 正偏差
	session.clockSkew = 90 * time.Second
	assert.Equal(t, local.Add(90*time.Second).Unix(), grokSearchDPoPProofIAT(session, local),
		"iat 应叠加正向时钟偏差")

	// 零值 session（skew=0）：iat 即本地时间
	assert.Equal(t, local.Unix(), grokSearchDPoPProofIAT(grokSearchDPoPSession{}, local),
		"零偏差时 iat 应为本地时间")

	// localNow 零值兜底：不 panic，返回近期时间
	iat := grokSearchDPoPProofIAT(grokSearchDPoPSession{}, time.Time{})
	assert.InDelta(t, time.Now().UTC().Unix(), iat, 5,
		"localNow 零值应兜底为当前时间")
}

// TestApplyGrokSearchDPoPAuthorizationUsesClockSkewInIAT 验证注入 DPoP 头时 iat 反映 skew（AC11）：
// 端到端确认 applyGrokSearchDPoPAuthorization 已改用 grokSearchDPoPProofIAT
func TestApplyGrokSearchDPoPAuthorizationUsesClockSkewInIAT(t *testing.T) {
	privateKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	access := "test-access-token"
	session := grokSearchDPoPSession{
		accessToken: access,
		privateKey:  privateKey,
		publicJWK:   grokSearchDPoPJWKFromKey(&privateKey.PublicKey),
		expiresAt:   time.Now().UTC().Add(time.Hour),
		clockSkew:   -80 * time.Second,
	}

	req, err := http.NewRequest(http.MethodPost, "https://console.x.ai/v1/responses", nil)
	require.NoError(t, err)
	require.NoError(t, applyGrokSearchDPoPAuthorization(req, session))

	proof := req.Header.Get("DPoP")
	require.NotEmpty(t, proof, "DPoP 头应存在")
	parts := strings.Split(proof, ".")
	require.Len(t, parts, 3, "proof 应为三段式 JWT")

	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	require.NoError(t, err)
	var claims map[string]any
	require.NoError(t, json.Unmarshal(payload, &claims))

	iat, ok := claims["iat"].(float64)
	require.True(t, ok, "iat 应为数值")
	want := time.Now().UTC().Add(-80 * time.Second).Unix()
	assert.InDelta(t, want, int64(iat), 2, "proof iat 应反映 -80s 时钟偏差")
}

// TestFetchGrokSearchDPoPSessionStoresClockSkewFromDateHeader 验证 mint 会把 Date 头学到的偏差存入 session（AC11）
func TestFetchGrokSearchDPoPSessionStoresClockSkewFromDateHeader(t *testing.T) {
	account := newGrokSearchDPoPEgressTestAccount()

	// Date 比本地慢 30s：session.clockSkew 应约为 -30s
	stub := &grokSearchDPoPMintUpstream{
		dateHeader: time.Now().UTC().Add(-30 * time.Second).Format(http.TimeFormat),
	}
	session, err := fetchGrokSearchDPoPSession(t.Context(), stub, account, "sso-egress-value", "http://proxy-a:8080")
	require.NoError(t, err)
	assert.True(t, session.clockSkew <= -20*time.Second && session.clockSkew >= -40*time.Second,
		"session.clockSkew 应约为 -30s，实际 %v", session.clockSkew)

	// Date 缺失：skew 应为 0（定义值），流程正常
	stubNoDate := &grokSearchDPoPMintUpstream{}
	session, err = fetchGrokSearchDPoPSession(t.Context(), stubNoDate, account, "sso-egress-value", "")
	require.NoError(t, err)
	assert.Equal(t, time.Duration(0), session.clockSkew, "Date 缺失时 clockSkew 应为 0")
}
