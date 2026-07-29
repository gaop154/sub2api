package admin

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"

	"github.com/Wei-Shaw/sub2api/internal/handler/dto"
	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

// grokSearchSSOImportConcurrency 是 SSO 批量导入 grok_search 账号的并发上限，
// 与 grok OAuth 链路（grokSSOImportConcurrency）保持一致。
const grokSearchSSOImportConcurrency = 3

// grokSearchDefaultBaseURL 是 console.x.ai 默认入口，与 service 层
// grokSearchDefaultBaseURL（openai_gateway_grok_search.go）对齐——此处独立声明
// 是因为 service 包常量未导出，避免 admin 包为此跨层暴露。
const grokSearchDefaultBaseURL = "https://console.x.ai"

// GrokSearchHandler 处理 grok_search 平台的管理员接口。
//
// grok_search 走 SSO cookie + console.x.ai/v1/responses（Console 网页态），与现有
// grok 平台（OIDC access_token + cli-chat-proxy.grok.com）物理隔离。本 handler 仅负责
// SSO 凭证录入，**绝不调用** grok OAuth 兑换链路（ConvertFromSSO / ConvertSSOToBuild /
// BuildAccountCredentials）——SSO 直接作为 cookie 使用，不需要兑换为 OAuth token。
type GrokSearchHandler struct {
	adminService service.AdminService
}

// NewGrokSearchHandler 创建 grok_search 管理端 handler。
func NewGrokSearchHandler(adminService service.AdminService) *GrokSearchHandler {
	return &GrokSearchHandler{adminService: adminService}
}

// grokSearchSSOImportRequest 是批量导入 grok_search SSO 账号的请求体。
//
// SSOTokens 是多行纯文本，每行一个 SSO token，支持 "sso=xxx"/"sso-rw=xxx"/cookie
// 整串/CSV 混合格式（由 xai.NormalizeSSOToken 统一归一化，见 normalizeSSOImportTokens）。
type grokSearchSSOImportRequest struct {
	SSOTokens string  `json:"sso_tokens" binding:"required"`
	BaseURL   string  `json:"base_url"`
	ProxyID   *int64  `json:"proxy_id"`
	GroupIDs  []int64 `json:"group_ids"`
	// Name 为可选账号名前缀：单 token 时直接作为账号名，多 token 时追加 #N。
	// 为空则回落到默认名（批量导入无法用单一名称时按顺序命名）。
	Name string `json:"name"`
}

type grokSearchSSOImportItemResult struct {
	Token   string       `json:"token"`
	Account *dto.Account `json:"account,omitempty"`
	Error   string       `json:"error,omitempty"`
}

type grokSearchSSOImportResponse struct {
	Created []grokSearchSSOImportItemResult `json:"created"`
	Failed  []grokSearchSSOImportItemResult `json:"failed"`
}

type grokSearchSSOImportJob struct {
	index int
	token string
}

type grokSearchSSOImportWorkerResult struct {
	created bool
	item    grokSearchSSOImportItemResult
}

// CreateAccountsFromSSO 批量用 SSO token 创建 grok_search 账号。
//
// 凭证写入 {sso_token, base_url}，与 forwarder（service/openai_gateway_grok_search.go）
// 的 GetCredential 读取键严格对齐——一旦 mismatch，forwarder 会因 sso_token 为空直接报错。
//
// 与 grok OAuth 的 CreateAccountsFromSSO 的关键差异：
//   - 不调用 ConvertFromSSO（无远程 OAuth 兑换），SSO 原值直接作为 cookie 写入 credentials。
//   - platform = grok_search、type = apikey（PoC 决定，参见 PRD/design §6）。
//   - 账号无 email/expiry（SSO 不携带这些信息），失效仅由 forwarder 401 检测触发临时下线。
func (h *GrokSearchHandler) CreateAccountsFromSSO(c *gin.Context) {
	var req grokSearchSSOImportRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	// 复用 grok OAuth 链路的归一化（每行调 xai.NormalizeSSOToken，跳空行/去重）。
	tokens := normalizeSSOImportTokens(nil, req.SSOTokens)
	if len(tokens) == 0 {
		response.BadRequest(c, "sso_tokens is required")
		return
	}
	baseURL := strings.TrimSpace(req.BaseURL)
	if baseURL == "" {
		baseURL = grokSearchDefaultBaseURL
	}
	// 拷贝切片避免多个 goroutine 共享同一底层数组时被误改（防御性）。
	groupIDs := append([]int64(nil), req.GroupIDs...)

	ctx := c.Request.Context()
	// 用户在创建弹窗填写的账号名（可选）；非空时作为命名 base，否则用默认名。
	nameBase := strings.TrimSpace(req.Name)
	workerCount := grokSearchSSOImportConcurrency
	if len(tokens) < workerCount {
		workerCount = len(tokens)
	}
	jobs := make(chan grokSearchSSOImportJob)
	items := make([]grokSearchSSOImportWorkerResult, len(tokens))
	var wg sync.WaitGroup
	for i := 0; i < workerCount; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for job := range jobs {
				items[job.index] = h.safeCreateGrokSearchAccountFromSSOToken(ctx, baseURL, groupIDs, req.ProxyID, job.token, job.index+1, len(tokens), nameBase)
			}
		}()
	}
	for i, token := range tokens {
		jobs <- grokSearchSSOImportJob{index: i, token: token}
	}
	close(jobs)
	wg.Wait()

	result := grokSearchSSOImportResponse{
		Created: make([]grokSearchSSOImportItemResult, 0, len(tokens)),
		Failed:  make([]grokSearchSSOImportItemResult, 0),
	}
	for _, item := range items {
		if item.created {
			result.Created = append(result.Created, item.item)
		} else {
			result.Failed = append(result.Failed, item.item)
		}
	}
	response.Success(c, result)
}

// safeCreateGrokSearchAccountFromSSOToken 包裹 panic 恢复，确保单个 token 创建失败
// 不会炸掉整个导入批次（与 grok OAuth 链路 safeCreateAccountFromSSOToken 结构一致）。
func (h *GrokSearchHandler) safeCreateGrokSearchAccountFromSSOToken(
	ctx context.Context,
	baseURL string,
	groupIDs []int64,
	proxyID *int64,
	token string,
	index, total int,
	nameBase string,
) (result grokSearchSSOImportWorkerResult) {
	defer func() {
		if recovered := recover(); recovered != nil {
			slog.Error("grok_search_sso_import_worker_panic", "index", index, "recover", recovered)
			result = grokSearchSSOImportWorkerResult{
				item: grokSearchSSOImportItemResult{
					Token: token,
					Error: fmt.Sprintf("internal worker panic: %v", recovered),
				},
			}
		}
	}()
	return h.createGrokSearchAccountFromSSOToken(ctx, baseURL, groupIDs, proxyID, token, index, total, nameBase)
}

// createGrokSearchAccountFromSSOToken 单个 SSO token → grok_search 账号。
// 不走 BuildAccountCredentials（grok OAuth 才需要），凭证 = {sso_token, base_url}。
func (h *GrokSearchHandler) createGrokSearchAccountFromSSOToken(
	ctx context.Context,
	baseURL string,
	groupIDs []int64,
	proxyID *int64,
	token string,
	index, total int,
	nameBase string,
) grokSearchSSOImportWorkerResult {
	name := grokSearchSSOImportAccountName(index, total, nameBase)
	account, err := h.adminService.CreateAccount(ctx, &service.CreateAccountInput{
		Name:     name,
		Platform: service.PlatformGrokSearch,
		Type:     service.AccountTypeAPIKey,
		Credentials: map[string]any{
			"sso_token": token,
			"base_url":  baseURL,
		},
		ProxyID: proxyID,
		GroupIDs: groupIDs,
	})
	if err != nil {
		return grokSearchSSOImportWorkerResult{
			item: grokSearchSSOImportItemResult{
				Token: token,
				Error: grokSSOImportErrorMessage(err),
			},
		}
	}
	return grokSearchSSOImportWorkerResult{
		created: true,
		item: grokSearchSSOImportItemResult{
			Token:   token,
			Account: dto.AccountFromService(account),
		},
	}
}

// grokSearchSSOImportAccountName 生成账号名。优先用用户填写的 nameBase（前端 form.name），
// 为空时回落到默认名。SSO token 不携带 email，多 token 时按导入顺序追加 #N。
func grokSearchSSOImportAccountName(index, total int, nameBase string) string {
	const defaultBase = "Grok Search SSO Account"
	base := strings.TrimSpace(nameBase)
	if base == "" {
		base = defaultBase
	}
	if total > 1 {
		return fmt.Sprintf("%s #%d", base, index)
	}
	return base
}
