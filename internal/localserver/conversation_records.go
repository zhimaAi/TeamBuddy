package localserver

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
	"goteams-client/internal/codexreply"
	"goteams-client/internal/i18n"
	"goteams-client/internal/taskruntime"
)

func newCodexReplyView(ctx context.Context) codexreply.View {
	locale := i18n.LocaleFromContext(ctx)
	return codexreply.NewView(i18n.WithLocale(locale, "codex_reply_hidden"), i18n.WithLocale(locale, "codex_reply_unavailable"))
}

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
	for i := range items {
		items[i].ProjectCodexReplies(newCodexReplyView(c.Request.Context()))
	}
	c.JSON(200, gin.H{"title": title, "executions": items})
}

func codexReplyPreview(ctx context.Context, text string, explicit bool) string {
	if r := codexreply.Parse(text, explicit); r != nil {
		locale := i18n.LocaleFromContext(ctx)
		return r.Text(i18n.WithLocale(locale, "codex_reply_hidden"), i18n.WithLocale(locale, "codex_reply_unavailable"))
	}
	return text
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
	rows, err := h.taskDB().QueryContext(c.Request.Context(), `WITH current_progress AS (
 SELECT p.*,COALESCE(p.external_session_id<>'' AND p.cli_type='codex'
   AND json_extract(b.raw,'$.explicit_privacy')=1 AND json_extract(b.raw,'$.reporting_owner')='hooks'
   AND p.external_session_id=json_extract(b.raw,'$.thread_id'),0) AS explicit
 FROM gt_task_progress p JOIN (SELECT uuid,CASE WHEN json_valid(vibe_coding_session_json) THEN vibe_coding_session_json END AS raw FROM gt_tasks) b ON b.uuid=p.task_uuid
 ), documents AS (
 SELECT task_uuid,user_prompt AS body,created_at,explicit FROM current_progress
 UNION ALL SELECT p.task_uuid,p.final_result,p.created_at,p.explicit FROM current_progress p WHERE p.explicit OR NOT (p.cli_type='codex' AND p.execution_mode='vibe_coding' AND EXISTS (SELECT 1 FROM gt_task_progress q WHERE q.task_uuid=p.task_uuid AND q.external_session_id=p.external_session_id AND instr(q.user_prompt,'<send_user_message_question_reply')>0))
 UNION ALL SELECT s.task_uuid,j.value,e.created_at,0 FROM gt_session_events e JOIN gt_cli_sessions s ON s.uuid=e.session_uuid, json_tree(CASE WHEN json_valid(e.payload_json) THEN e.payload_json ELSE '{}' END) j WHERE j.type='text' AND j.key IN ('text','content','message','result')
 UNION ALL SELECT h.task_uuid,j.value,h.archived_at,COALESCE(j.path LIKE '$.progress[%'
  AND json_extract(h.payload_json,'$.codex_explicit_thread')<>''
  AND json_extract(h.payload_json,'$.codex_explicit_thread')=json_extract(h.payload_json,j.path||'.external_session_id'),0) AS explicit FROM gt_task_execution_history h, json_tree(h.payload_json) j WHERE j.type='text' AND j.key IN ('user_prompt','final_result','text','content','message','result','request_summary','response_summary')
 AND (explicit OR j.key='user_prompt' OR h.execution_mode<>'vibe_coding' OR h.execution_tool<>'codex' OR instr(h.payload_json,'send_user_message_question_reply')=0)
 ), matches AS (
 SELECT task_uuid,body,explicit,ROW_NUMBER() OVER (PARTITION BY task_uuid ORDER BY created_at DESC) AS position FROM documents WHERE instr(lower(body),lower(?))>0
 ) SELECT t.uuid,t.title,COALESCE(m.body,''),t.updated_at,COALESCE(m.explicit,0) FROM gt_tasks t LEFT JOIN matches m ON m.task_uuid=t.uuid AND m.position=1
 WHERE (instr(lower(t.title),lower(?))>0 OR m.task_uuid IS NOT NULL) AND `+scopeSQL+` ORDER BY t.updated_at DESC,t.uuid LIMIT 51 OFFSET ?`, args...)
	if err != nil {
		i18n.LocalServerError(c, 500, err)
		return
	}
	defer rows.Close()
	for rows.Next() {
		var id, title, body string
		var updated int64
		var explicit bool
		if err = rows.Scan(&id, &title, &body, &updated, &explicit); err != nil {
			i18n.LocalServerError(c, 500, err)
			return
		}
		body = codexReplyPreview(c.Request.Context(), body, explicit)
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
