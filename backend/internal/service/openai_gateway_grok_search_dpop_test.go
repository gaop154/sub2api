package service

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestGrokSearchDPoPProof 验证 DPoP proof JWT 构造正确性
func TestGrokSearchDPoPProof(t *testing.T) {
	privateKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)

	publicJWK := grokSearchDPoPJWKFromKey(&privateKey.PublicKey)
	session := grokSearchDPoPSession{
		accessToken: "test-access-token",
		privateKey:  privateKey,
		publicJWK:   publicJWK,
		expiresAt:   time.Now().UTC().Add(time.Hour),
	}

	req := httptest.NewRequest("POST", "https://console.x.ai/v1/responses?query=test", nil)
	err = applyGrokSearchDPoPAuthorization(req, session)
	require.NoError(t, err)

	// 验证 Authorization 头
	authHeader := req.Header.Get("Authorization")
	require.True(t, strings.HasPrefix(authHeader, "DPoP "), "Authorization 应以 DPoP 开头")
	accessToken := strings.TrimPrefix(authHeader, "DPoP ")
	require.Equal(t, "test-access-token", accessToken, "access token 应正确注入")

	// 验证 DPoP proof JWT
	dpopHeader := req.Header.Get("DPoP")
	require.NotEmpty(t, dpopHeader, "DPoP 头应存在")

	// 解析 JWT
	parser := jwt.NewParser()
	token, parts, err := parser.ParseUnverified(dpopHeader, jwt.MapClaims{})
	require.NoError(t, err, "JWT 应可解析")
	require.Len(t, parts, 3, "JWT 应有 3 部分")

	// 验证 JOSE header
	header := token.Header
	require.Equal(t, "dpop+jwt", header["typ"], "typ 应为 dpop+jwt")
	require.Equal(t, "ES256", header["alg"], "alg 应为 ES256")

	jwkFromHeader, ok := header["jwk"].(map[string]any)
	require.True(t, ok, "jwk 应存在")
	require.Equal(t, "EC", jwkFromHeader["kty"], "JWK kty 应为 EC")
	require.Equal(t, "P-256", jwkFromHeader["crv"], "JWK crv 应为 P-256")
	require.NotEmpty(t, jwkFromHeader["x"], "JWK x 应存在")
	require.NotEmpty(t, jwkFromHeader["y"], "JWK y 应存在")

	// 验证 claims
	claims, ok := token.Claims.(jwt.MapClaims)
	require.True(t, ok)

	// htm 应为大写
	require.Equal(t, "POST", claims["htm"], "htm 应为大写 POST")

	// htu 应去 query 且 host 小写
	require.Equal(t, "https://console.x.ai/v1/responses", claims["htu"], "htu 应去 query 且 host 小写")

	// iat 应存在且为近期时间
	iat, ok := claims["iat"].(float64)
	require.True(t, ok)
	iatTime := time.Unix(int64(iat), 0).UTC()
	require.WithinDuration(t, time.Now().UTC(), iatTime, time.Minute, "iat 应为近期时间")

	// jti 应为有效 UUID
	jti, ok := claims["jti"].(string)
	require.True(t, ok)
	_, err = uuid.Parse(jti)
	require.NoError(t, err, "jti 应为有效 UUID")

	// ath 应为 base64url(sha256(access_token))
	ath, ok := claims["ath"].(string)
	require.True(t, ok)
	expectedATH := base64.RawURLEncoding.EncodeToString(sum256([]byte("test-access-token")))
	require.Equal(t, expectedATH, ath, "ath 应为 access_token 的 SHA256 base64url")
}

// TestGrokSearchDPoPProofJTIVerifies 验证两次 proof 的 jti 不同
func TestGrokSearchDPoPProofJTIVerifies(t *testing.T) {
	privateKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	publicJWK := grokSearchDPoPJWKFromKey(&privateKey.PublicKey)
	session := grokSearchDPoPSession{
		accessToken: "test",
		privateKey:  privateKey,
		publicJWK:   publicJWK,
		expiresAt:   time.Now().UTC().Add(time.Hour),
	}

	req1 := httptest.NewRequest("POST", "https://console.x.ai/v1/responses", nil)
	req2 := httptest.NewRequest("POST", "https://console.x.ai/v1/responses", nil)

	_ = applyGrokSearchDPoPAuthorization(req1, session)
	_ = applyGrokSearchDPoPAuthorization(req2, session)

	dpop1 := req1.Header.Get("DPoP")
	dpop2 := req2.Header.Get("DPoP")

	parser := jwt.NewParser()
	token1, _, _ := parser.ParseUnverified(dpop1, jwt.MapClaims{})
	token2, _, _ := parser.ParseUnverified(dpop2, jwt.MapClaims{})

	claims1, _ := token1.Claims.(jwt.MapClaims)
	claims2, _ := token2.Claims.(jwt.MapClaims)

	jti1, _ := claims1["jti"].(string)
	jti2, _ := claims2["jti"].(string)

	require.NotEqual(t, jti1, jti2, "两次 proof 的 jti 应不同")
}

// TestGrokSearchDPoPJWKThumbprint 验证 JWK thumbprint 计算（RFC 7638）
func TestGrokSearchDPoPJWKThumbprint(t *testing.T) {
	// 固定测试密钥（基于固定坐标确保可重复）
	privateKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)

	publicJWK := grokSearchDPoPJWKFromKey(&privateKey.PublicKey)
	thumbprint, err := grokSearchDPoPJWKThumbprint(publicJWK)
	require.NoError(t, err)

	// thumbprint 应为 base64url 字符串，长度固定（SHA256 → 32 字节 → base64url 约 43 字符）
	assert.Len(t, thumbprint, 43, "SHA256 thumbprint base64url 长度应为 43")

	// 两次计算应一致
	thumbprint2, _ := grokSearchDPoPJWKThumbprint(publicJWK)
	assert.Equal(t, thumbprint, thumbprint2, "相同 JWK 的 thumbprint 应一致")

	// 不同于 JWK 本身
	assert.NotEqual(t, thumbprint, publicJWK.X, "thumbprint 应不同于 x 坐标")
	assert.NotEqual(t, thumbprint, publicJWK.Y, "thumbprint 应不同于 y 坐标")
}

// TestGrokSearchDPoPHTUNormalization 验证 HTU 规范化（host 小写、去 query/fragment）
func TestGrokSearchDPoPHTUNormalization(t *testing.T) {
	tests := []struct {
		name     string
		inputURL string
		expected string
	}{
		{
			name:     "标准 HTTPS",
			inputURL: "https://Console.X.AI/v1/responses",
			expected: "https://console.x.ai/v1/responses",
		},
		{
			name:     "带 query 参数",
			inputURL: "https://console.x.ai/v1/responses?query=test&foo=bar",
			expected: "https://console.x.ai/v1/responses",
		},
		{
			name:     "带 fragment",
			inputURL: "https://console.x.ai/v1/responses#section",
			expected: "https://console.x.ai/v1/responses",
		},
		{
			name:     "带 query 和 fragment",
			inputURL: "https://CONSOLE.X.AI/v1/responses?query=test#section",
			expected: "https://console.x.ai/v1/responses",
		},
		{
			name:     "根路径补全",
			inputURL: "https://console.x.ai",
			expected: "https://console.x.ai/",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// 用 http.NewRequest（生产路径，url.Parse 正确分离 fragment），
			// 而非 httptest.NewRequest（用 ParseRequestURI，不分离 fragment，会把 #section 编码进 path）。
			req, err := http.NewRequest("POST", tt.inputURL, nil)
			require.NoError(t, err)
			htu := grokSearchDPoPHTU(req)
			assert.Equal(t, tt.expected, htu, "HTU 应规范化正确")
		})
	}
}

// TestGrokSearchDPoPSessionCache 验证 session 缓存机制
func TestGrokSearchDPoPSessionCache(t *testing.T) {
	account := &Account{
		ID:          1001,
		Name:        "test-grok-search",
		Platform:    PlatformGrokSearch,
		Credentials: map[string]any{"sso_token": "test-sso-token"},
	}

	// 测试缓存 key 构造稳定性
	baseURL := "https://console.x.ai"
	key1 := grokSearchDPoPSessionCacheKey(baseURL, account, "test-sso-token")
	key2 := grokSearchDPoPSessionCacheKey(baseURL, account, "test-sso-token")
	assert.Equal(t, key1, key2, "相同参数应生成相同 cache key")

	// 不同账号应有不同 key
	account2 := &Account{ID: 1002}
	key3 := grokSearchDPoPSessionCacheKey(baseURL, account2, "test-sso-token")
	assert.NotEqual(t, key1, key3, "不同账号应有不同 cache key")
}

// TestGrokSearchDPoPRequiredError 验证 DPoP required 错误识别
func TestGrokSearchDPoPRequiredError(t *testing.T) {
	tests := []struct {
		name     string
		body     []byte
		expected bool
	}{
		{
			name:     "标准 dpop-required",
			body:     []byte(`{"code":"unauthorized:dpop-required","error":"DPoP proof required but was not verified."}`),
			expected: true,
		},
		{
			name:     "dpop proof required",
			body:     []byte(`{"error":"DPoP proof required"}`),
			expected: true,
		},
		{
			name:     "大小写不敏感",
			body:     []byte(`{"error":"DPOP-REQUIRED"}`),
			expected: true,
		},
		{
			name:     "其它 403",
			body:     []byte(`{"code":"permission-denied"}`),
			expected: false,
		},
		{
			name:     "空 body",
			body:     []byte(``),
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := isGrokSearchDPoPRequired(tt.body)
			assert.Equal(t, tt.expected, result, "isGrokSearchDPoPRequired 应正确识别")
		})
	}
}

// TestGrokSearchUnauthorizedError 验证未授权错误识别
func TestGrokSearchUnauthorizedError(t *testing.T) {
	tests := []struct {
		name     string
		body     []byte
		expected bool
	}{
		{
			name:     "unauthorized",
			body:     []byte(`{"code":"unauthorized","error":"Unauthorized access"}`),
			expected: true,
		},
		{
			name:     "not authorized",
			body:     []byte(`{"error":"not authorized"}`),
			expected: true,
		},
		{
			name:     "排除 CF 挑战",
			body:     []byte(`<html><title>Attention Required! | Cloudflare</title></html>`),
			expected: false,
		},
		{
			name:     "排除 dpop-required",
			body:     []byte(`{"code":"unauthorized:dpop-required"}`),
			expected: false,
		},
		{
			name:     "排除 permission-denied",
			body:     []byte(`{"code":"permission-denied"}`),
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := isGrokSearchUnauthorized(tt.body)
			assert.Equal(t, tt.expected, result, "isGrokSearchUnauthorized 应正确识别")
		})
	}
}

// TestGrokSearchDPoPEndpointErrorResponse 验证 token endpoint 错误伪造的 http.Response
func TestGrokSearchDPoPEndpointErrorResponse(t *testing.T) {
	req := httptest.NewRequest("POST", "https://console.x.ai/v1/dpop/token", nil)
	err := &grokSearchDPoPTokenError{
		status:        http.StatusForbidden,
		statusText:    "403 Forbidden",
		header:        http.Header{"Content-Type": []string{"application/json"}},
		body:          []byte(`{"code":"unauthorized:dpop-required","error":"DPoP proof required"}`),
		bodyTruncated: false,
		request:       req,
	}

	resp := err.response()
	require.NotNil(t, resp)
	assert.Equal(t, http.StatusForbidden, resp.StatusCode, "状态码应为 403")
	assert.Equal(t, "403 Forbidden", resp.Status, "Status 应正确")
	assert.Equal(t, "application/json", resp.Header.Get("Content-Type"), "Content-Type 应保留")

	bodyBytes, _ := readAllAndClose(resp.Body)
	assert.Equal(t, err.body, bodyBytes, "body 应一致")
	assert.Equal(t, req, resp.Request, "request 应保留")
}

// TestGrokSearchDPoPHTUEscapedPath 验证 HTU 路径转义处理
func TestGrokSearchDPoPHTUEscapedPath(t *testing.T) {
	// 带 %2F（转义的 /）的路径
	req := httptest.NewRequest("POST", "https://console.x.ai/v1/responses/encoded%2Fpath", nil)
	htu := grokSearchDPoPHTU(req)
	assert.Equal(t, "https://console.x.ai/v1/responses/encoded%2Fpath", htu, "HTU 应保留转义")
}

// 辅助函数：sum256 计算 SHA256
func sum256(data []byte) []byte {
	h := sha256.New()
	h.Write(data)
	return h.Sum(nil)
}

// 辅助函数：readAllAndClose 安全读取并关闭 response body
func readAllAndClose(r io.ReadCloser) ([]byte, error) {
	defer r.Close()
	return io.ReadAll(r)
}

// storeGrokSearchDPoPSessionForTest 预填充 DPoP session 缓存，跳过真实 token 交换。
// 让 forward / chat-bridge / test-connection 测试聚焦业务逻辑，不依赖 console.x.ai /v1/dpop/token
// （真实 token 交换含 EC 密钥绑定校验，测试无法预知随机密钥的 cnf.jkt）。
// 预填充后 doGrokSearchDPoPRequest.manager.get 直接命中缓存，只发一次业务请求。
func storeGrokSearchDPoPSessionForTest(t *testing.T, manager *grokSearchDPoPSessionManager, account *Account, ssoToken string) {
	t.Helper()
	privateKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	session := grokSearchDPoPSession{
		accessToken: "mock-dpop-access-token",
		privateKey:  privateKey,
		publicJWK:   grokSearchDPoPJWKFromKey(&privateKey.PublicKey),
		expiresAt:   time.Now().UTC().Add(time.Hour),
	}
	key := grokSearchDPoPSessionCacheKey(getBaseURL(account), account, ssoToken)
	manager.store(key, session)
}
