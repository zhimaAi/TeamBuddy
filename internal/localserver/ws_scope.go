package localserver

import (
	"context"
	"time"

	"github.com/gorilla/websocket"

	"goteams-client/internal/cloud"
)

// ActivateAccount 丢弃旧连接及其订阅和待发送内容，但保留应用级推送循环。
func (h *WSHub) ActivateAccount(account *cloud.Client) {
	h.mu.Lock()
	if h.account == account {
		h.mu.Unlock()
		return
	}
	h.account = account
	h.generation++
	clients := h.clients
	h.clients = make(map[*websocket.Conn]*wsClient)
	// 在新连接能加入之前关闭旧连接，旧查询和历史补发不能向新账号投递。
	for _, client := range clients {
		client.close()
	}
	h.mu.Unlock()
}

// taskAllowed 同时校验任务与 CLI 会话归属，不允许用本地任务 UUID 搭配其他账号的会话。
// SQL 在 hub 锁外执行，避免任务写入和账号切换互相等待。
func (h *WSHub) taskAllowed(taskUUID, sessionUUID string) (uint64, bool) {
	h.mu.RLock()
	account, generation := h.account, h.generation
	h.mu.RUnlock()
	if h.logDB == nil || (taskUUID == "" && sessionUUID == "") {
		return generation, false
	}
	scopeSQL, args := taskScopeSQL(account, "t")
	query := "SELECT COUNT(*) FROM gt_tasks t"
	if sessionUUID != "" {
		query += " JOIN gt_cli_sessions s ON s.task_uuid=t.uuid"
	}
	query += " WHERE " + scopeSQL
	if taskUUID != "" {
		query += " AND t.uuid=?"
		args = append(args, taskUUID)
	}
	if sessionUUID != "" {
		query += " AND s.uuid=?"
		args = append(args, sessionUUID)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	var count int
	err := h.logDB.QueryRowContext(ctx, query, args...).Scan(&count)
	return generation, err == nil && count > 0
}

func (h *WSHub) sessionSubscribed(client *wsClient, sessionUUID string) bool {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return client.generation == h.generation && client.subscribedSessions[sessionUUID]
}

func (h *WSHub) broadcastTask(taskUUID, messageType string, data interface{}) {
	generation, allowed := h.taskAllowed(taskUUID, "")
	if !allowed {
		return
	}
	h.broadcastAccount(generation, messageType, data)
}

func (h *WSHub) broadcastAccount(generation uint64, messageType string, data interface{}) {
	h.mu.RLock()
	clients := make([]*wsClient, 0, len(h.clients))
	if generation == h.generation {
		for _, client := range h.clients {
			if client.generation == generation {
				clients = append(clients, client)
			}
		}
	}
	h.mu.RUnlock()
	message := map[string]interface{}{"type": messageType, "data": data}
	for _, client := range clients {
		if err := writeClientJSON(client, message); err != nil {
			h.mu.Lock()
			delete(h.clients, client.conn)
			h.mu.Unlock()
			client.close()
		}
	}
}
