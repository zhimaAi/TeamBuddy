package cloud

import (
	"context"
	"net/http"
	"net/url"
)

// BrowserLoginScheme 客户端注册的自定义协议名。浏览器登录确认页由服务端按这个
// 协议拼回调地址唤起客户端；改动这里必须同步 desktop/main/deep-link.cjs 与
// electron-builder.yml 的 protocols 配置。
const BrowserLoginScheme = "teamsboard"

// BrowserLoginCallbackURL 浏览器确认登录后跳回客户端的深链接地址。
const BrowserLoginCallbackURL = BrowserLoginScheme + "://auth/callback"

// BrowserLoginSession 一次「浏览器登录」会话的登记结果。
type BrowserLoginSession struct {
	State     string `json:"state"`
	LoginURL  string `json:"login_url"`
	ExpiresIn int    `json:"expires_in"`
}

// CreateBrowserLoginSession 向云端登记一次浏览器登录，返回应在系统浏览器中打开的地址。
// 该接口不需要登录态：调用方（客户端）此时还没有任何凭据。
func (c *Client) CreateBrowserLoginSession(ctx context.Context, state string) (*BrowserLoginSession, error) {
	c = c.WithoutCredentials()
	body := map[string]string{
		"state":        state,
		"redirect_uri": BrowserLoginCallbackURL,
	}
	var payload BrowserLoginSession
	if err := c.do(ctx, http.MethodPost, "/api/client/login-sessions", body, &payload); err != nil {
		return nil, err
	}
	if payload.State == "" {
		payload.State = state
	}
	// Official and self-hosted services expose the same confirmation route.
	// Keep registration, browser confirmation and ticket exchange on the selected
	// deployment, even if its response contains an internal/default web address.
	payload.LoginURL = c.baseURL + "/client-login?state=" + url.QueryEscape(payload.State)
	return &payload, nil
}

// ExchangeBrowserLoginTicket 用一次性 ticket 换取登录态，返回结构与账号密码登录一致。
// state 与 ticket 都参与校验，云端应保证 ticket 一次性且与 state 绑定。
func (c *Client) ExchangeBrowserLoginTicket(ctx context.Context, state, ticket string) (*LoginResponse, error) {
	c = c.WithoutCredentials()
	body := map[string]string{
		"state":  state,
		"ticket": ticket,
	}
	var payload loginPayload
	if err := c.do(ctx, http.MethodPost, "/api/client/login-sessions/exchange", body, &payload); err != nil {
		return nil, err
	}
	return payload.toLoginResponse("")
}
