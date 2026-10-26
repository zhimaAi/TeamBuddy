package localserver

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"

	"goteams-client/internal/applog"
	"goteams-client/internal/cloud"
	"goteams-client/internal/executor"
	"goteams-client/internal/i18n"
	"goteams-client/internal/storage/log"
)

var wsUpgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool {
		// Allow all origins (local loopback service, no CSRF required)
		return true
	},
	HandshakeTimeout: 10 * time.Second,
}

// wsWriteTimeout is the timeout for a single write to the local browser connection.
const wsWriteTimeout = 10 * time.Second

const wsQueueBytes = 4 << 20

// PushFunc push function signature: returns the push type and data, and returns nil to indicate that no push is required in this round.
type PushFunc func() (pushType string, data interface{})

// AccountPushFunc 使用本轮固定的账号查询，结果只投递给同代连接。
type AccountPushFunc func(account *cloud.Client) (pushType string, data interface{})

// WSHub local WebSocket manager
type WSHub struct {
	mu         sync.RWMutex
	clients    map[*websocket.Conn]*wsClient
	logDB      *sql.DB
	apiToken   string
	account    *cloud.Client
	generation uint64

	// Universal push
	pushMu           sync.RWMutex
	pushFuncs        []PushFunc
	accountPushFuncs []AccountPushFunc
	pushCancel       context.CancelFunc
}

// wsClient local WebSocket client
type wsClient struct {
	conn       *websocket.Conn
	generation uint64
	taskUUID   string
	// subscribedSessions 本连接订阅执行事件的 CLI 会话集合。
	// 旧协议一次只订阅一个 session，动态区与执行窗口可能同时订阅不同
	// session，因此改为集合，保持互不覆盖。
	subscribedSessions map[string]bool
	queueMu            sync.Mutex
	queuedBytes        int
	send               chan []byte
	done               chan struct{}
	space              chan struct{}
	closeOnce          sync.Once
}

// NewWSHub creates a WebSocket manager
func NewWSHub(logDB *sql.DB) *WSHub {
	return &WSHub{
		clients: make(map[*websocket.Conn]*wsClient),
		logDB:   logDB,
	}
}

// SetAPIToken records the startup API token for handshake validation.
func (h *WSHub) SetAPIToken(token string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.apiToken = token
}

// RegisterPushFunc registers a periodic push function.
// Each push loop will call all registered pushFuncs, and when non-nil data is returned, it will be broadcast to all clients.
func (h *WSHub) RegisterPushFunc(fn PushFunc) {
	h.pushMu.Lock()
	h.pushFuncs = append(h.pushFuncs, fn)
	h.pushMu.Unlock()
}

// RegisterAccountPushFunc 注册随账号隔离的周期推送；查询不持有 hub 锁。
func (h *WSHub) RegisterAccountPushFunc(fn AccountPushFunc) {
	h.pushMu.Lock()
	h.accountPushFuncs = append(h.accountPushFuncs, fn)
	h.pushMu.Unlock()
}

// StartPushLoop starts a periodic push loop and broadcasts the result of registering pushFunc to all connected clients.
// Multiple calls will first stop the old loop and then start the new loop.
func (h *WSHub) StartPushLoop(interval time.Duration) {
	h.StopPushLoop()
	ctx, cancel := context.WithCancel(context.Background())
	h.pushMu.Lock()
	h.pushCancel = cancel
	h.pushMu.Unlock()

	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				h.runPushCycle()
			}
		}
	}()
}

// StopPushLoop Stops the periodic push loop.
func (h *WSHub) StopPushLoop() {
	h.pushMu.Lock()
	if h.pushCancel != nil {
		h.pushCancel()
		h.pushCancel = nil
	}
	h.pushMu.Unlock()
}

// runPushCycle executes a round of push: traverses all pushFuncs and broadcasts the results.
func (h *WSHub) runPushCycle() {
	h.pushMu.RLock()
	fns := make([]PushFunc, len(h.pushFuncs))
	copy(fns, h.pushFuncs)
	accountFns := append([]AccountPushFunc(nil), h.accountPushFuncs...)
	h.pushMu.RUnlock()

	for _, fn := range fns {
		pushType, data := fn()
		if data == nil {
			continue
		}
		h.BroadcastToAll(pushType, data)
	}
	for _, fn := range accountFns {
		h.mu.RLock()
		account, generation := h.account, h.generation
		h.mu.RUnlock()
		pushType, data := fn(account)
		if data != nil {
			h.broadcastAccount(generation, pushType, data)
		}
	}
}

// BroadcastToAll broadcasts a message to all connected clients (not limited to specific task/session subscribers).
func (h *WSHub) BroadcastToAll(msgType string, data interface{}) {
	msg := map[string]interface{}{
		"type": msgType,
		"data": data,
	}

	h.mu.RLock()
	clients := make([]*wsClient, 0, len(h.clients))
	for _, c := range h.clients {
		clients = append(clients, c)
	}
	h.mu.RUnlock()

	for _, c := range clients {
		if err := writeClientJSON(c, msg); err != nil {
			applog.Warn("ws broadcast to all failed", "error", err)
			h.mu.Lock()
			delete(h.clients, c.conn)
			h.mu.Unlock()
			c.close()
		}
	}
}

// BroadcastTaskChanged notifies task views that persisted task/step state has
// changed and should be reloaded from the HTTP API.
func (h *WSHub) BroadcastTaskChanged(taskUUID string) {
	if taskUUID == "" {
		return
	}
	h.broadcastTask(taskUUID, "task.changed", map[string]interface{}{"task_uuid": taskUUID})
}

// BroadcastExecutorCompleted notifies global app listeners that a CLI reply has
// finished successfully and can be surfaced as a desktop notification.
func (h *WSHub) BroadcastExecutorCompleted(taskUUID, stepKey, sessionUUID, status string) {
	if taskUUID == "" || sessionUUID == "" {
		return
	}
	h.broadcastTask(taskUUID, "executor.completed", map[string]interface{}{
		"task_uuid":    taskUUID,
		"step_key":     stepKey,
		"session_uuid": sessionUUID,
		"status":       status,
	})
}

// BroadcastActivity pushes the throttled latest CLI activity to all clients.
// Task views filter by task_uuid and patch the running-step subtitle locally,
// so it stays fresh without polling /progress.
func (h *WSHub) BroadcastActivity(taskUUID, sessionUUID string, sequence int, eventType, content string, at int64) {
	if taskUUID == "" || sessionUUID == "" {
		return
	}
	h.broadcastTask(taskUUID, "executor.activity", map[string]interface{}{
		"task_uuid":    taskUUID,
		"session_uuid": sessionUUID,
		"sequence":     sequence,
		"activity_id":  fmt.Sprintf("event:%s:%d", sessionUUID, sequence),
		"status":       activityStatusRunning,
		"actor":        activityActorTool,
		"kind":         eventType,
		"event_type":   eventType,
		"preview":      content,
		"content":      content,
		"at":           at,
	})
}

// Close Closes all local WebSocket client connections (browser connections).
// Called on account switch/logout: proactively sending a close frame triggers the browser-side onclose,
// so it auto-reconnects and binds to the new account session's WSHub, avoiding the old connection becoming an orphan.
func (h *WSHub) Close() {
	h.StopPushLoop()

	h.mu.Lock()
	clients := make([]*wsClient, 0, len(h.clients))
	for _, c := range h.clients {
		clients = append(clients, c)
	}
	h.clients = make(map[*websocket.Conn]*wsClient)
	h.mu.Unlock()

	// 控制帧可与唯一的数据帧写协程并发。
	for _, c := range clients {
		_ = writeClientControl(c, websocket.CloseMessage,
			websocket.FormatCloseMessage(websocket.CloseNormalClosure, "账号会话已切换"))
		c.close()
	}
}

// HandleWebSocket handles the WebSocket connection
func (h *WSHub) HandleWebSocket(c *gin.Context) {
	// Validate the startup API token passed via ?token= (browsers cannot set
	// custom headers on WebSocket handshakes).
	token := c.Query("token")
	h.mu.RLock()
	expected := h.apiToken
	h.mu.RUnlock()
	if expected != "" && (len(token) != len(expected) ||
		subtle.ConstantTimeCompare([]byte(token), []byte(expected)) != 1) {
		i18n.Error(c, http.StatusForbidden, "common_access_token_invalid", "access_token_invalid")
		return
	}

	// Validate the Session Cookie
	sessionID, err := c.Cookie("goteams_local_session")
	if err != nil || sessionID == "" {
		i18n.Error(c, http.StatusUnauthorized, "common_not_authenticated", "not_authenticated")
		return
	}

	sessionMgr.mu.RLock()
	session, ok := sessionMgr.sessions[sessionID]
	sessionMgr.mu.RUnlock()
	if !ok || session == nil || time.Now().After(session.ExpiresAt) {
		i18n.Error(c, http.StatusUnauthorized, "common_session_expired", "session_expired")
		return
	}

	// Upgrade HTTP to WebSocket
	conn, err := wsUpgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		return
	}

	client := &wsClient{conn: conn, send: make(chan []byte, 256), done: make(chan struct{}), space: make(chan struct{}, 1)}
	h.mu.Lock()
	client.generation = h.generation
	h.clients[conn] = client
	h.mu.Unlock()

	// The Ping handler must be registered before the read loop starts:
	// 1. Registering it after ReadMessage races with the read goroutine for the conn's handler field;
	// 2. Pong 使用允许并发的 WriteControl，不阻塞业务事件入队。
	conn.SetPingHandler(func(appData string) error {
		return writeClientControl(client, websocket.PongMessage, nil)
	})

	// read loop
	go client.writeLoop()
	go h.readLoop(conn, client)
}

// readLoop reads client messages
func (h *WSHub) readLoop(conn *websocket.Conn, client *wsClient) {
	defer func() {
		h.mu.Lock()
		delete(h.clients, conn)
		h.mu.Unlock()
		client.close()
	}()

	for {
		_, message, err := conn.ReadMessage()
		if err != nil {
			return
		}

		var msg struct {
			Type        string `json:"type"`
			TaskUUID    string `json:"task_uuid"`
			SessionUUID string `json:"session_uuid"`
			AfterSeq    int    `json:"after_sequence"`
		}
		if err := json.Unmarshal(message, &msg); err != nil {
			continue
		}

		switch msg.Type {
		case "executor.subscribe":
			generation, allowed := h.taskAllowed(msg.TaskUUID, msg.SessionUUID)
			if !allowed {
				continue
			}
			h.mu.Lock()
			if generation != h.generation || client.generation != generation {
				h.mu.Unlock()
				continue
			}
			client.taskUUID = msg.TaskUUID
			if client.subscribedSessions == nil {
				client.subscribedSessions = make(map[string]bool)
			}
			if msg.SessionUUID != "" {
				client.subscribedSessions[msg.SessionUUID] = true
			}
			h.mu.Unlock()
			h.backfillEvents(client, msg.SessionUUID, msg.AfterSeq)
		case "executor.unsubscribe":
			h.mu.Lock()
			// 带 session_uuid 时只退订该会话（动态区与执行窗口可能同时
			// 订阅不同 session），不带则清空全部订阅，保持旧协议语义。
			if msg.SessionUUID != "" && client.subscribedSessions != nil {
				delete(client.subscribedSessions, msg.SessionUUID)
			} else {
				client.taskUUID = ""
				client.subscribedSessions = nil
			}
			h.mu.Unlock()
		}
	}
}

// backfillEvents re-sends historical events
func (h *WSHub) backfillEvents(client *wsClient, sessionUUID string, afterSeq int) {
	if h.logDB == nil || sessionUUID == "" || !h.sessionSubscribed(client, sessionUUID) {
		return
	}

	eventStore := log.NewEventStore(h.logDB)
	events, err := eventStore.QueryAfterSequence(sessionUUID, afterSeq)
	if err != nil {
		return
	}

	for _, event := range events {
		if !h.sessionSubscribed(client, sessionUUID) {
			return
		}
		msg := map[string]interface{}{
			"type":         "executor.event",
			"session_uuid": event.SessionUUID,
			"sequence":     event.Sequence,
			"event_type":   event.EventType,
			"event":        json.RawMessage(event.Payload),
		}
		if err := queueClientJSON(client, msg, true); err != nil {
			return
		}
	}
}

// BroadcastEvent broadcasts an event to clients subscribed to the session
func (h *WSHub) BroadcastEvent(sessionUUID string, sequence int, event executor.ExecutorEvent) {
	h.mu.RLock()
	clients := make([]*wsClient, 0, len(h.clients))
	for _, c := range h.clients {
		if c.generation == h.generation && c.subscribedSessions[sessionUUID] {
			clients = append(clients, c)
		}
	}
	h.mu.RUnlock()

	msg := map[string]interface{}{
		"type":         "executor.event",
		"session_uuid": sessionUUID,
		"sequence":     sequence,
		"event_type":   event.Type,
		"event":        event,
	}

	for _, c := range clients {
		if err := writeClientJSON(c, msg); err != nil {
			// Slow browser disconnected
			h.mu.Lock()
			delete(h.clients, c.conn)
			h.mu.Unlock()
			c.close()
		}
	}
}

// BroadcastStateChanged broadcasts state changes
func (h *WSHub) BroadcastStateChanged(taskUUID, stepKey, sessionUUID, status string) {
	h.mu.RLock()
	clients := make([]*wsClient, 0, len(h.clients))
	for _, c := range h.clients {
		if c.generation == h.generation && c.taskUUID == taskUUID {
			clients = append(clients, c)
		}
	}
	h.mu.RUnlock()

	msg := map[string]interface{}{
		"type":         "executor.state_changed",
		"task_uuid":    taskUUID,
		"step_key":     stepKey,
		"session_uuid": sessionUUID,
		"status":       status,
	}

	for _, c := range clients {
		if err := writeClientJSON(c, msg); err != nil {
			h.mu.Lock()
			delete(h.clients, c.conn)
			h.mu.Unlock()
			c.close()
		}
	}

	// executor.state_changed is scoped to explicit session subscribers. Task
	// pages use the shared socket, so publish a lightweight global invalidation
	// event as well and let each page filter by task_uuid.
	h.BroadcastTaskChanged(taskUUID)
	if status == "success" {
		h.BroadcastExecutorCompleted(taskUUID, stepKey, sessionUUID, status)
	}
}

func writeClientJSON(client *wsClient, value interface{}) error {
	return queueClientJSON(client, value, false)
}

func queueClientJSON(client *wsClient, value interface{}, wait bool) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	// 每连接独立且有界的发送队列，慢窗口不能反压 CLI 或拖住其他窗口。
	var deadline <-chan time.Time
	if wait {
		timer := time.NewTimer(wsWriteTimeout)
		defer timer.Stop()
		deadline = timer.C
	}
	for {
		client.queueMu.Lock()
		select {
		case <-client.done:
			client.queueMu.Unlock()
			return fmt.Errorf("websocket closed")
		default:
		}
		if client.queuedBytes+len(data) <= wsQueueBytes {
			select {
			case client.send <- data:
				client.queuedBytes += len(data)
				client.queueMu.Unlock()
				return nil
			default:
			}
		}
		client.queueMu.Unlock()
		if !wait || len(data) > wsQueueBytes {
			break
		}
		// 历史补发在本连接读取协程等待空间，不阻塞执行事件生产者。
		select {
		case <-client.done:
			return fmt.Errorf("websocket closed")
		case <-deadline:
			client.close()
			return fmt.Errorf("websocket backfill timed out")
		case <-client.space:
		}
	}
	client.close()
	return fmt.Errorf("websocket send queue full")
}

func (client *wsClient) close() {
	client.closeOnce.Do(func() {
		close(client.done)
		_ = client.conn.Close()
	})
}

func (client *wsClient) writeLoop() {
	defer client.close()
	for {
		select {
		case <-client.done:
			return
		case data := <-client.send:
			client.queueMu.Lock()
			client.queuedBytes -= len(data)
			client.queueMu.Unlock()
			select {
			case client.space <- struct{}{}:
			default:
			}
			_ = client.conn.SetWriteDeadline(time.Now().Add(wsWriteTimeout))
			if err := client.conn.WriteMessage(websocket.TextMessage, data); err != nil {
				return
			}
		}
	}
}

// WriteControl 可与数据帧并发，Pong 不等待发送队列中的业务消息。
func writeClientControl(client *wsClient, messageType int, data []byte) error {
	return client.conn.WriteControl(messageType, data, time.Now().Add(wsWriteTimeout))
}
