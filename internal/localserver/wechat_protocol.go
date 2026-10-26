package localserver

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
)

const wechatDefaultBaseURL = "https://ilinkai.weixin.qq.com"

// This version matches the currently documented iLink client wire format.
const wechatChannelVersion = "2.4.8"
const wechatAppClientVersion = "132104" // 0x00020408

var errWeChatSessionExpired = errors.New("微信会话暂不可用")

type wechatHTTPError struct{ StatusCode int }

func (e *wechatHTTPError) Error() string { return fmt.Sprintf("微信服务 HTTP %d", e.StatusCode) }

type wechatSendError struct{ Ret, ErrCode int }

func (e *wechatSendError) Error() string {
	return fmt.Sprintf("微信消息发送失败 ret=%d errcode=%d", e.Ret, e.ErrCode)
}

type wechatAPI struct {
	client *http.Client
}

// wechatTransport 封装与 iLink 服务端的所有通讯（二维码/状态/拉取/发送/生命周期/输入指示）。
// 这是微信通道对外的唯一通讯 seam：后续若替换底层实现（如换用 corespeed-io/wechatbot 等 SDK），
// 只需提供一个新的 wechatTransport 实现，上层 wechatRemote 的业务逻辑无需改动。
// 所有方法均走 call() 统一鉴权头与错误处理，新增微信通讯能力时只在这里加方法。
type wechatTransport interface {
	getQR(ctx context.Context) (wechatQRResponse, error)
	qrStatus(ctx context.Context, base, qr, code string) (wechatQRStatus, error)
	getUpdates(ctx context.Context, base, token, cursor string) (wechatUpdates, error)
	// sendText 走 /ilink/bot/sendmessage。
	// client_id 沿用 corespeed-io/wechatbot::BuildTextMessage 的语义：每次发送都即时生成新的 UUID，
	// 不再复用入站消息的 client_id。iLink 协议里 client_id 仅是发送侧的去重 / 幂等标记，
	// 强制回传持久化的入站 client_id 反而会因会话窗口不匹配被 iLink 静默丢弃，表现为
	// "sendmessage 返回 200 + message_id 但微信侧收不到"。
	sendText(ctx context.Context, base, token, recipient, contextToken, content string) (bool, error)
	notifyLifecycle(ctx context.Context, base, token string, starting bool) error
	// getConfig 拉取会话配置（当前仅 typing_ticket，用于"输入中"指示）。
	getConfig(ctx context.Context, base, token, userID, contextToken string) (string, error)
	// sendTyping 发送/取消"输入中"指示：status=1 开始，status=2 结束。
	sendTyping(ctx context.Context, base, token, userID, ticket string, status int) error
}

type wechatQRResponse struct {
	QRCode       string `json:"qrcode"`
	ImageContent string `json:"qrcode_img_content"`
}

type wechatQRStatus struct {
	Status       string `json:"status"`
	BotToken     string `json:"bot_token"`
	BotID        string `json:"ilink_bot_id"`
	BaseURL      string `json:"baseurl"`
	UserID       string `json:"ilink_user_id"`
	RedirectHost string `json:"redirect_host"`
}

type wechatTextItem struct {
	Type     int `json:"type"`
	TextItem struct {
		Text string `json:"text"`
	} `json:"text_item"`
}

type wechatMessage struct {
	Seq        json.Number `json:"seq"`
	MessageID  json.Number `json:"message_id"`
	FromUserID string      `json:"from_user_id"`
	GroupID    string      `json:"group_id"`
	// ClientID 是 iLink 给入站消息分配的每条消息唯一标识；与本次入站消息的会话绑定，
	// 不可作为发送侧的 client_id 复用——send 路径在 wechatAPI.sendText 中按
	// corespeed-io/wechatbot::BuildTextMessage 的方式即时生成新 UUID 作为发送方 client_id。
	ClientID     string           `json:"client_id"`
	MessageType  int              `json:"message_type"`
	ContextToken string           `json:"context_token"`
	Items        []wechatTextItem `json:"item_list"`
}

func (m wechatMessage) text() string {
	var value strings.Builder
	for _, item := range m.Items {
		if item.Type == 1 {
			value.WriteString(item.TextItem.Text)
		}
	}
	return strings.TrimSpace(value.String())
}

func (m wechatMessage) id() string {
	if id := m.MessageID.String(); id != "" && id != "0" {
		return id
	}
	if seq := m.Seq.String(); seq != "" {
		return "seq:" + seq
	}
	return ""
}

type wechatUpdates struct {
	Ret       int             `json:"ret"`
	ErrCode   int             `json:"errcode"`
	ErrMsg    string          `json:"errmsg"`
	Messages  []wechatMessage `json:"msgs"`
	Cursor    string          `json:"get_updates_buf"`
	TimeoutMS int             `json:"longpolling_timeout_ms"`
}

func newWeChatAPI() *wechatAPI {
	return &wechatAPI{client: &http.Client{
		Timeout:       45 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}
}

func validWeChatBase(raw string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Scheme != "https" || u.User != nil || u.Port() != "" || u.RawQuery != "" || u.Fragment != "" || u.Path != "" {
		return "", errors.New("微信服务地址无效")
	}
	host := strings.ToLower(u.Hostname())
	if host != "weixin.qq.com" && !strings.HasSuffix(host, ".weixin.qq.com") {
		return "", errors.New("微信服务地址不受信任")
	}
	return "https://" + host, nil
}

func (a *wechatAPI) call(ctx context.Context, method, base, path, token string, body any, result any) error {
	trustedBase, err := validWeChatBase(base)
	if err != nil {
		return err
	}
	var content io.Reader
	if body != nil {
		data, encodeErr := json.Marshal(body)
		if encodeErr != nil {
			return encodeErr
		}
		content = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, trustedBase+path, content)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("iLink-App-Id", "bot")
	req.Header.Set("iLink-App-ClientVersion", wechatAppClientVersion)
	if method == http.MethodPost {
		req.Header.Set("AuthorizationType", "ilink_bot_token")
		uin := make([]byte, 4)
		if _, err = rand.Read(uin); err != nil {
			return err
		}
		req.Header.Set("X-WECHAT-UIN", base64.StdEncoding.EncodeToString([]byte(strconv.FormatUint(uint64(binary.BigEndian.Uint32(uin)), 10))))
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := a.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return &wechatHTTPError{StatusCode: resp.StatusCode}
	}
	// 读取原始响应体用于调试：部分错误信息可能在 ret/errcode 之外的其他字段里。
	bodyBytes, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return err
	}
	//applog.Info("iLink HTTP 原始响应",
	//	"method", method,
	//	"path", path,
	//	"status_code", resp.StatusCode,
	//	"raw_body", string(bodyBytes),
	//)
	if err = json.Unmarshal(bodyBytes, result); err != nil {
		return err
	}
	return nil
}

func wechatBaseInfo() map[string]string {
	return map[string]string{"channel_version": wechatChannelVersion, "bot_agent": "TeamsBoard/0.1.5"}
}

func (a *wechatAPI) getQR(ctx context.Context) (wechatQRResponse, error) {
	var result wechatQRResponse
	err := a.call(ctx, http.MethodPost, wechatDefaultBaseURL, "/ilink/bot/get_bot_qrcode?bot_type=3", "", map[string]any{"local_token_list": []string{}}, &result)
	if err == nil && (result.QRCode == "" || result.ImageContent == "") {
		err = errors.New("微信服务未返回二维码")
	}
	return result, err
}

func (a *wechatAPI) qrStatus(ctx context.Context, base, qr, code string) (wechatQRStatus, error) {
	path := "/ilink/bot/get_qrcode_status?qrcode=" + url.QueryEscape(qr)
	if code != "" {
		path += "&verify_code=" + url.QueryEscape(code)
	}
	var result wechatQRStatus
	err := a.call(ctx, http.MethodGet, base, path, "", nil, &result)
	return result, err
}

func (a *wechatAPI) getUpdates(ctx context.Context, base, token, cursor string) (wechatUpdates, error) {
	var result wechatUpdates
	err := a.call(ctx, http.MethodPost, base, "/ilink/bot/getupdates", token,
		map[string]any{"get_updates_buf": cursor, "base_info": wechatBaseInfo()}, &result)
	if err == nil && (result.Ret != 0 || result.ErrCode != 0) {
		if result.Ret == -14 || result.ErrCode == -14 {
			err = errWeChatSessionExpired
		} else {
			err = fmt.Errorf("微信服务返回错误 ret=%d errcode=%d", result.Ret, result.ErrCode)
		}
	}
	return result, err
}

func (a *wechatAPI) sendText(ctx context.Context, base, token, recipient, contextToken, content string) (bool, error) {
	// 每次发送即时生成新的 client_id（参考 corespeed-io/wechatbot::BuildTextMessage），
	// 避免持久化入站 client_id 带来的 iLink 会话窗口不匹配问题。
	clientID := uuid.NewString()
	body := map[string]any{"msg": map[string]any{
		"from_user_id": "", "to_user_id": recipient, "client_id": clientID,
		"message_type": 2, "message_state": 2, "context_token": contextToken,
		"item_list": []any{map[string]any{"type": 1, "text_item": map[string]string{"text": content}}},
	}, "base_info": wechatBaseInfo()}
	var result struct {
		Ret       int             `json:"ret"`
		ErrCode   int             `json:"errcode"`
		MessageID json.RawMessage `json:"message_id"`
	}
	if err := a.call(ctx, http.MethodPost, base, "/ilink/bot/sendmessage", token, body, &result); err != nil {
		return false, err
	}
	if result.Ret != 0 || result.ErrCode != 0 {
		return false, &wechatSendError{Ret: result.Ret, ErrCode: result.ErrCode}
	}
	messageID := bytes.TrimSpace(result.MessageID)
	return len(messageID) > 0 && !bytes.Equal(messageID, []byte("null")) &&
		!bytes.Equal(messageID, []byte(`""`)) && !bytes.Equal(messageID, []byte("0")) &&
		!bytes.Equal(messageID, []byte(`"0"`)), nil
}

// notifyLifecycle only changes the upstream online state. It does not revoke a
// WeChat-side bot binding.
func (a *wechatAPI) notifyLifecycle(ctx context.Context, base, token string, starting bool) error {
	path := "/ilink/bot/msg/notifystop"
	if starting {
		path = "/ilink/bot/msg/notifystart"
	}
	var result struct {
		Ret     int `json:"ret"`
		ErrCode int `json:"errcode"`
	}
	if err := a.call(ctx, http.MethodPost, base, path, token,
		map[string]any{"base_info": wechatBaseInfo()}, &result); err != nil {
		return err
	}
	if result.Ret != 0 || result.ErrCode != 0 {
		return fmt.Errorf("微信通道状态通知失败 ret=%d errcode=%d", result.Ret, result.ErrCode)
	}
	return nil
}

// getConfig 拉取与某用户的会话配置。当前仅消费 typing_ticket，供"输入中"指示使用。
// 端点与 corespeed-io/wechatbot 的 GetConfig 对齐（/ilink/bot/getconfig）。
func (a *wechatAPI) getConfig(ctx context.Context, base, token, userID, contextToken string) (string, error) {
	var result struct {
		TypingTicket string `json:"typing_ticket,omitempty"`
		Ret          int    `json:"ret"`
		ErrCode      int    `json:"errcode"`
	}
	if err := a.call(ctx, http.MethodPost, base, "/ilink/bot/getconfig", token,
		map[string]any{
			"ilink_user_id": userID,
			"context_token": contextToken,
			"base_info":     wechatBaseInfo(),
		}, &result); err != nil {
		return "", err
	}
	if result.Ret != 0 || result.ErrCode != 0 {
		return "", fmt.Errorf("微信 getconfig 失败 ret=%d errcode=%d", result.Ret, result.ErrCode)
	}
	return result.TypingTicket, nil
}

// sendTyping 发送或取消"输入中"指示。status=1 表示正在输入，status=2 表示停止。
// 端点与 corespeed-io/wechatbot 的 SendTyping 对齐（/ilink/bot/sendtyping）。
func (a *wechatAPI) sendTyping(ctx context.Context, base, token, userID, ticket string, status int) error {
	var result struct {
		Ret     int `json:"ret"`
		ErrCode int `json:"errcode"`
	}
	if err := a.call(ctx, http.MethodPost, base, "/ilink/bot/sendtyping", token,
		map[string]any{
			"ilink_user_id": userID,
			"typing_ticket": ticket,
			"status":        status,
			"base_info":     wechatBaseInfo(),
		}, &result); err != nil {
		return err
	}
	if result.Ret != 0 || result.ErrCode != 0 {
		return fmt.Errorf("微信 sendtyping 失败 ret=%d errcode=%d", result.Ret, result.ErrCode)
	}
	return nil
}
