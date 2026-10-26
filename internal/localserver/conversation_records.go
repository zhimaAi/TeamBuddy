package localserver

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
	"goteams-client/internal/i18n"
	"goteams-client/internal/taskruntime"
)

func (h *TasksHandler) conversationRecords(c *gin.Context) {
	title, items, err := taskruntime.NewService(h.taskDB(), h.taskRoot).ConversationRecords(c.Request.Context(), c.Param("uuid"))
	if errors.Is(err, taskruntime.ErrNotFound) {
		i18n.Error(c, 404, "localserver_task_not_found", "task_not_found")
		return
	}
	if err != nil {
		i18n.LocalServerError(c, 500, err)
		return
	}
	c.JSON(200, gin.H{"title": title, "executions": items})
}

// 参数化子串查询覆盖当前消息、会话事件和切换前快照，按任务去重并分页。
func (h *TasksHandler) searchConversations(c *gin.Context) {
	query := strings.TrimSpace(c.Query("q"))
	offset, parseErr := strconv.Atoi(c.DefaultQuery("offset", "0"))
	if parseErr != nil || offset < 0 || utf8.RuneCountInString(query) > 200 {
		i18n.Error(c, 400, "common_request_invalid", "")
		return
	}
	items := []gin.H{}
	if query == "" {
		c.JSON(200, gin.H{"items": items, "has_more": false})
		return
	}
	scopeSQL, scopeArgs := taskScopeSQL(h.taskCloudClient(), "t")
	args := append([]any{query, query}, scopeArgs...)
	args = append(args, offset)
	rows, err := h.taskDB().QueryContext(c.Request.Context(), `WITH documents AS (
 SELECT task_uuid,user_prompt AS body,created_at FROM gt_task_progress
 UNION ALL SELECT task_uuid,final_result,created_at FROM gt_task_progress
 UNION ALL SELECT s.task_uuid,j.value,e.created_at FROM gt_session_events e JOIN gt_cli_sessions s ON s.uuid=e.session_uuid, json_tree(CASE WHEN json_valid(e.payload_json) THEN e.payload_json ELSE '{}' END) j WHERE j.type='text' AND j.key IN ('text','content','message','result')
 UNION ALL SELECT h.task_uuid,j.value,h.archived_at FROM gt_task_execution_history h, json_tree(h.payload_json) j WHERE j.type='text' AND j.key IN ('user_prompt','final_result','text','content','message','result','request_summary','response_summary')
 ), matches AS (
 SELECT task_uuid,body,ROW_NUMBER() OVER (PARTITION BY task_uuid ORDER BY created_at DESC) AS position FROM documents WHERE instr(lower(body),lower(?))>0
 ) SELECT t.uuid,t.title,COALESCE(m.body,''),t.updated_at FROM gt_tasks t LEFT JOIN matches m ON m.task_uuid=t.uuid AND m.position=1
 WHERE (instr(lower(t.title),lower(?))>0 OR m.task_uuid IS NOT NULL) AND `+scopeSQL+` ORDER BY t.updated_at DESC,t.uuid LIMIT 51 OFFSET ?`, args...)
	if err != nil {
		i18n.LocalServerError(c, 500, err)
		return
	}
	defer rows.Close()
	for rows.Next() {
		var id, title, body string
		var updated int64
		if err = rows.Scan(&id, &title, &body, &updated); err != nil {
			i18n.LocalServerError(c, 500, err)
			return
		}
		runes := []rune(body)
		start := 0
		index := strings.Index(strings.ToLower(body), strings.ToLower(query))
		if index > 0 {
			start = utf8.RuneCountInString(body[:index]) - 40
			if start < 0 {
				start = 0
			}
		}
		end := start + 220
		if end > len(runes) {
			end = len(runes)
		}
		snippet := string(runes[start:end])
		if start > 0 {
			snippet = "…" + snippet
		}
		if end < len(runes) {
			snippet += "…"
		}
		items = append(items, gin.H{"task_uuid": id, "title": title, "snippet": snippet})
	}
	if err = rows.Err(); err != nil {
		i18n.LocalServerError(c, 500, err)
		return
	}
	more := len(items) > 50
	if more {
		items = items[:50]
	}
	c.JSON(http.StatusOK, gin.H{"items": items, "has_more": more})
}
