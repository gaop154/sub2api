package service

import (
	"bytes"
	"container/list"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"golang.org/x/sync/singleflight"
)

// DPoP 相关常量
const (
	grokSearchDPoPSessionLimit     = 4096
	grokSearchDPoPRefreshSkew      = 20 * time.Second
	grokSearchDPoPMaxTokenLifetime = time.Hour
	grokSearchDPoPTokenPath        = "/dpop/token"
)

// grokSearchDPoPJWK 是 DPoP 公钥的 JWK 表示（RFC 7638）
type grokSearchDPoPJWKStruct struct {
	Kty string `json:"kty"` // EC
	Crv string `json:"crv"` // P-256
	X   string `json:"x"`   // base64url
	Y   string `json:"y"`   // base64url
}

// grokSearchDPoPSession 是 DPoP token 会话，包含 access_token 与绑定密钥
type grokSearchDPoPSession struct {
	accessToken string
	privateKey  *ecdsa.PrivateKey
	publicJWK grokSearchDPoPJWKStruct
	expiresAt   time.Time
}

// grokSearchDPoPSessionManager 管理 DPoP session 的 LRU 缓存与并发去重
type grokSearchDPoPSessionManager struct {
	mu       sync.Mutex
	sessions map[string]*grokSearchDPoPSessionEntry
	lru      list.List
	loads    singleflight.Group
	now      func() time.Time
}

type grokSearchDPoPSessionEntry struct {
	key     string
	session grokSearchDPoPSession
	element *list.Element
}

// grokSearchDPoPTokenError 表示 token endpoint 返回的错误，可伪造 *http.Response 汇入现有错误处理
type grokSearchDPoPTokenError struct {
	status        int
	statusText    string
	header        http.Header
	body          []byte
	bodyTruncated bool
	request       *http.Request
}

func (e *grokSearchDPoPTokenError) Error() string {
	suffix := ""
	if e.bodyTruncated {
		suffix = " (response truncated)"
	}
	return fmt.Sprintf("Console DPoP token 接口返回 %d%s", e.status, suffix)
}

// response 伪造 *http.Response，汇入 handleGrokSearchAccountUpstreamError
func (e *grokSearchDPoPTokenError) response() *http.Response {
	header := e.header.Clone()
	header.Set("Content-Length", strconv.Itoa(len(e.body)))
	if e.bodyTruncated {
		header.Set("X-Grok2API-Body-Truncated", "1")
	}
	return &http.Response{
		StatusCode: e.status,
		Status:     e.statusText,
		Header:     header,
		Body:       io.NopCloser(bytes.NewReader(e.body)),
		ContentLength: int64(len(e.body)),
		Request:    e.request,
	}
}

// newGrokSearchDPoPSessionManager 创建 DPoP session manager
func newGrokSearchDPoPSessionManager() *grokSearchDPoPSessionManager {
	return &grokSearchDPoPSessionManager{
		sessions: make(map[string]*grokSearchDPoPSessionEntry),
		now:      time.Now,
	}
}

// get 获取或刷新 DPoP session（带 LRU 缓存与 singleflight 并发去重）
func (m *grokSearchDPoPSessionManager) get(
	ctx context.Context,
	s *OpenAIGatewayService,
	account *Account,
	ssoToken string,
) (grokSearchDPoPSession, string, error) {
	key := grokSearchDPoPSessionCacheKey(getBaseURL(account), account, ssoToken)
	if session, ok := m.cached(key); ok {
		return session, key, nil
	}

	value, err, _ := m.loads.Do(key, func() (any, error) {
		if session, ok := m.cached(key); ok {
			return session, nil
		}
		session, fetchErr := s.fetchGrokSearchDPoPSession(ctx, account, ssoToken)
		if fetchErr != nil {
			return grokSearchDPoPSession{}, fetchErr
		}
		m.store(key, session)
		return session, nil
	})
	if err != nil {
		return grokSearchDPoPSession{}, key, err
	}
	session, ok := value.(grokSearchDPoPSession)
	if !ok {
		return grokSearchDPoPSession{}, key, errors.New("Console DPoP session 类型无效")
	}
	return session, key, nil
}

// cached 从缓存读取 session（未过期则命中并 LRU 前移）
func (m *grokSearchDPoPSessionManager) cached(key string) (grokSearchDPoPSession, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	entry, ok := m.sessions[key]
	if !ok {
		return grokSearchDPoPSession{}, false
	}
	// 过期检查（提前 skew 秒视为过期）
	if !entry.session.expiresAt.After(m.now().UTC().Add(grokSearchDPoPRefreshSkew)) {
		m.removeLocked(entry)
		return grokSearchDPoPSession{}, false
	}
	m.lru.MoveToFront(entry.element)
	return entry.session, true
}

// store 存储 session（LRU 满则淘汰最旧）
func (m *grokSearchDPoPSessionManager) store(key string, session grokSearchDPoPSession) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if entry := m.sessions[key]; entry != nil {
		entry.session = session
		m.lru.MoveToFront(entry.element)
		return
	}
	// LRU 满淘汰最旧
	if len(m.sessions) >= grokSearchDPoPSessionLimit {
		if oldest := m.lru.Back(); oldest != nil {
			m.removeLocked(oldest.Value.(*grokSearchDPoPSessionEntry))
		}
	}
	entry := &grokSearchDPoPSessionEntry{key: key, session: session}
	entry.element = m.lru.PushFront(entry)
	m.sessions[key] = entry
}

// invalidate 失效指定 session（仅当 accessToken 匹配时，防误删并发刷新的新 session）
func (m *grokSearchDPoPSessionManager) invalidate(key, accessToken string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	current := m.sessions[key]
	if current == nil || (accessToken != "" && current.session.accessToken != accessToken) {
		return
	}
	m.removeLocked(current)
	// 不调用 singleflight.Forget：保持过期 token 并发 401 时的去重，避免 burst 换 token
}

// removeLocked 从 LRU 移除 entry（需持有 mu）
func (m *grokSearchDPoPSessionManager) removeLocked(entry *grokSearchDPoPSessionEntry) {
	if entry == nil {
		return
	}
	delete(m.sessions, entry.key)
	if entry.element != nil {
		m.lru.Remove(entry.element)
		entry.element = nil
	}
}

// grokSearchDPoPSessionCacheKey 构造缓存键：base_url|account.ID|sha256(sso)
func grokSearchDPoPSessionCacheKey(baseURL string, account *Account, ssoToken string) string {
	return strings.TrimRight(strings.TrimSpace(baseURL), "/") + "|" +
		strconv.FormatUint(uint64(account.ID), 10) + "|" +
		hashToken(ssoToken)
}
// getBaseURL 获取账号的 base_url（优先从 credential，否则使用默认值）
func getBaseURL(account *Account) string {
	baseURL := strings.TrimSpace(account.GetCredential("base_url"))
	if baseURL == "" {
		return grokSearchDefaultBaseURL
	}
	return baseURL
}

// fetchGrokSearchDPoPSession 执行 DPoP token 交换与绑定校验
func (s *OpenAIGatewayService) fetchGrokSearchDPoPSession(
	ctx context.Context,
	account *Account,
	ssoToken string,
) (grokSearchDPoPSession, error) {
	// 1. 生成 EC P-256 密钥对
	privateKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return grokSearchDPoPSession{}, fmt.Errorf("生成 Console DPoP 密钥: %w", err)
	}
	publicJWK := grokSearchDPoPJWKFromKey(&privateKey.PublicKey)

	// 2. POST /v1/dpop/token
	payload, err := json.Marshal(map[string]any{"jwk": publicJWK})
	if err != nil {
		return grokSearchDPoPSession{}, err
	}

	tokenURL := strings.TrimRight(strings.TrimSpace(getBaseURL(account)), "/")
	if !strings.HasSuffix(tokenURL, "/v1") {
		tokenURL += "/v1"
	}
	tokenURL += grokSearchDPoPTokenPath

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenURL, bytes.NewReader(payload))
	if err != nil {
		return grokSearchDPoPSession{}, err
	}

	applyGrokSearchBrowserHeaders(req, ssoToken)
	req.Header.Set("Content-Type", "application/json")

	// 用 DoWithTLS 发送（Chrome profile 过 CF）
	resp, err := s.httpUpstream.DoWithTLS(req, "", account.ID, 0, grokSearchChromeProfile())
	if err != nil {
		return grokSearchDPoPSession{}, err
	}

	body, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if err != nil {
		return grokSearchDPoPSession{}, err
	}

	// 3. 解析响应
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return grokSearchDPoPSession{}, &grokSearchDPoPTokenError{
			status:        resp.StatusCode,
			statusText:    resp.Status,
			header:        resp.Header.Clone(),
			body:          body,
			bodyTruncated: false,
			request:       req,
		}
	}

	var tokenResponse struct {
		AccessToken string `json:"access_token"`
		TokenType   string `json:"token_type"`
		ExpiresIn   int    `json:"expires_in"`
	}
	if err := json.Unmarshal(body, &tokenResponse); err != nil {
		return grokSearchDPoPSession{}, fmt.Errorf("解析 Console DPoP token: %w", err)
	}
	if strings.TrimSpace(tokenResponse.AccessToken) == "" ||
		!strings.EqualFold(strings.TrimSpace(tokenResponse.TokenType), "DPoP") {
		return grokSearchDPoPSession{}, errors.New("Console DPoP token 响应无效")
	}
	if tokenResponse.ExpiresIn <= 0 ||
		time.Duration(tokenResponse.ExpiresIn)*time.Second > grokSearchDPoPMaxTokenLifetime {
		return grokSearchDPoPSession{}, errors.New("Console DPoP token 有效期无效")
	}

	// 4. 绑定校验（JWK thumbprint == cnf.jkt）
	thumbprint, err := grokSearchDPoPJWKThumbprint(publicJWK)
	if err != nil {
		return grokSearchDPoPSession{}, err
	}
	tokenExpiry, tokenThumbprint, err := parseGrokSearchDPoPAccessToken(tokenResponse.AccessToken)
	if err != nil {
		return grokSearchDPoPSession{}, err
	}
	if tokenThumbprint != thumbprint {
		return grokSearchDPoPSession{}, errors.New("Console DPoP token 与本地密钥不匹配")
	}

	// 5. 计算过期时间（取 expires_in 与 token exp 的较小值）
	now := time.Now().UTC()
	expiresAt := now.Add(time.Duration(tokenResponse.ExpiresIn) * time.Second)
	if tokenExpiry.Before(expiresAt) {
		expiresAt = tokenExpiry
	}
	if !expiresAt.After(now.Add(grokSearchDPoPRefreshSkew)) {
		return grokSearchDPoPSession{}, errors.New("Console DPoP token 已过期或即将过期")
	}

	logger.LegacyPrintf("service.openai_gateway_grok_search",
		"grok_search DPoP token 交换成功 account_id=%d expires_at=%s",
		account.ID, expiresAt.Format(time.RFC3339))

	return grokSearchDPoPSession{
		accessToken: tokenResponse.AccessToken,
		privateKey:  privateKey,
		publicJWK:   publicJWK,
		expiresAt:   expiresAt,
	}, nil
}

// doGrokSearchDPoPRequest 执行带 DPoP proof 的业务请求（401 自动重试一次）
func (s *OpenAIGatewayService) doGrokSearchDPoPRequest(
	ctx context.Context,
	account *Account,
	ssoToken string,
	proxyURL string,
	method, upstreamURL string,
	body []byte,
	accept string,
) (*http.Response, error) {
	for attempt := 0; attempt < 2; attempt++ {
		session, cacheKey, err := s.grokSearchDPoP.get(ctx, s, account, ssoToken)
		if err != nil {
			var endpointErr *grokSearchDPoPTokenError
			if errors.As(err, &endpointErr) {
				return endpointErr.response(), nil
			}
			return nil, err
		}

		req, err := http.NewRequestWithContext(ctx, method, upstreamURL, bytes.NewReader(body))
		if err != nil {
			return nil, err
		}

		applyGrokSearchBrowserHeaders(req, ssoToken)
		if len(body) > 0 {
			req.Header.Set("Content-Type", "application/json")
		}
		if strings.TrimSpace(accept) != "" {
			req.Header.Set("Accept", accept)
		}
		if strings.HasSuffix(req.URL.Path, "/responses") {
			req.Header.Set("x-cluster", grokSearchXCluster)
		}

		// 注入 DPoP 头
		if err := applyGrokSearchDPoPAuthorization(req, session); err != nil {
			return nil, err
		}

		resp, err := s.httpUpstream.DoWithTLS(req, proxyURL, account.ID, 0, grokSearchChromeProfile())
		if err != nil {
			return nil, err
		}

		// 401 则失效 session 并重试一次
		if resp.StatusCode != http.StatusUnauthorized || attempt > 0 {
			return resp, nil
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
		s.grokSearchDPoP.invalidate(cacheKey, session.accessToken)
	}
	return nil, errors.New("Console DPoP 重试状态无效")
}

// grokSearchDPoPJWK 从 EC 公钥构造 JWK（RFC 7638）
func grokSearchDPoPJWKFromKey(key *ecdsa.PublicKey) grokSearchDPoPJWKStruct {
	return grokSearchDPoPJWKStruct{
		Kty: "EC",
		Crv: "P-256",
		X:   base64.RawURLEncoding.EncodeToString(key.X.FillBytes(make([]byte, 32))),
		Y:   base64.RawURLEncoding.EncodeToString(key.Y.FillBytes(make([]byte, 32))),
	}
}

// grokSearchDPoPJWKThumbprint 计算 JWK thumbprint（RFC 7638：字典序 crv/kty/x/y + sha256 + base64url）
func grokSearchDPoPJWKThumbprint(jwk grokSearchDPoPJWKStruct) (string, error) {
	canonical := struct {
		Crv string `json:"crv"`
		Kty string `json:"kty"`
		X   string `json:"x"`
		Y   string `json:"y"`
	}{Crv: jwk.Crv, Kty: jwk.Kty, X: jwk.X, Y: jwk.Y}
	data, err := json.Marshal(canonical)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(data)
	return base64.RawURLEncoding.EncodeToString(digest[:]), nil
}

// parseGrokSearchDPoPAccessToken 解析 access_token JWT，提取 exp 与 cnf.jkt
func parseGrokSearchDPoPAccessToken(value string) (time.Time, string, error) {
	parts := strings.Split(value, ".")
	if len(parts) != 3 {
		return time.Time{}, "", errors.New("Console DPoP access token 格式无效")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return time.Time{}, "", errors.New("Console DPoP access token payload 无效")
	}
	var claims struct {
		ExpiresAt int64 `json:"exp"`
		CNF       struct {
			JKT string `json:"jkt"`
		} `json:"cnf"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil ||
		claims.ExpiresAt <= 0 || strings.TrimSpace(claims.CNF.JKT) == "" {
		return time.Time{}, "", errors.New("Console DPoP access token claims 无效")
	}
	return time.Unix(claims.ExpiresAt, 0).UTC(), claims.CNF.JKT, nil
}

// applyGrokSearchDPoPAuthorization 注入 DPoP 头（Authorization: DPoP <token> + DPoP: <proof>）
func applyGrokSearchDPoPAuthorization(req *http.Request, session grokSearchDPoPSession) error {
	if req == nil || req.URL == nil || session.privateKey == nil ||
		strings.TrimSpace(session.accessToken) == "" {
		return errors.New("Console DPoP 请求参数无效")
	}
	digest := sha256.Sum256([]byte(session.accessToken))
	claims := jwt.MapClaims{
		"jti": uuid.NewString(),
		"htm": strings.ToUpper(req.Method),
		"htu": grokSearchDPoPHTU(req),
		"iat": time.Now().UTC().Unix(),
		"ath": base64.RawURLEncoding.EncodeToString(digest[:]),
	}
	proof := jwt.NewWithClaims(jwt.SigningMethodES256, claims)
	proof.Header["typ"] = "dpop+jwt"
	proof.Header["jwk"] = session.publicJWK
	signed, err := proof.SignedString(session.privateKey)
	if err != nil {
		return fmt.Errorf("签名 Console DPoP proof: %w", err)
	}
	req.Header.Set("Authorization", "DPoP "+session.accessToken)
	req.Header.Set("DPoP", signed)
	return nil
}

// grokSearchDPoPHTU 规范化 HTTP-TU（scheme://host/path，host 小写，去 query/fragment）
func grokSearchDPoPHTU(req *http.Request) string {
	path := req.URL.EscapedPath()
	if path == "" {
		path = "/"
	}
	return req.URL.Scheme + "://" + strings.ToLower(req.URL.Host) + path
}

// applyGrokSearchBrowserHeaders 注入 console.x.ai 网页态请求头（不含 Authorization/x-cluster）
func applyGrokSearchBrowserHeaders(req *http.Request, ssoToken string) {
	req.Header.Set("Cookie", "sso="+ssoToken+"; sso-rw="+ssoToken)
	req.Header.Set("Origin", grokSearchOrigin)
	req.Header.Set("Referer", grokSearchOrigin+"/")
	req.Header.Set("User-Agent", grokSearchUserAgent)
	req.Header.Set("Sec-Ch-Ua", grokSearchSecChUa)
	req.Header.Set("Sec-Ch-Ua-Mobile", grokSearchSecChUaMobile)
	req.Header.Set("Sec-Ch-Ua-Platform", grokSearchSecChUaPlatform)
	req.Header.Set("Accept", "*/*")
	req.Header.Set("Accept-Language", "zh-CN,zh;q=0.9,en;q=0.8")
	req.Header.Set("Cache-Control", "no-cache")
	req.Header.Set("Pragma", "no-cache")
	req.Header.Set("Sec-Fetch-Dest", "empty")
	req.Header.Set("Sec-Fetch-Mode", "cors")
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	req.Header.Set("Priority", "u=1, i")
}
