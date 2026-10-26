package localserver

import (
	"context"
	"database/sql"
	"errors"
	"net/http"

	"goteams-client/internal/cloud"
	"goteams-client/internal/pipeline"
)

// checkTaskScope 供非路由业务入口复用；范围外与不存在使用相同错误。
func checkTaskScope(ctx context.Context, db *sql.DB, client *cloud.Client, taskUUID string) error {
	scopeSQL, args := taskScopeSQL(client, "t")
	var found int
	err := db.QueryRowContext(ctx, "SELECT 1 FROM gt_tasks t WHERE t.uuid=? AND "+scopeSQL,
		append([]any{taskUUID}, args...)...).Scan(&found)
	if errors.Is(err, sql.ErrNoRows) {
		return newCoreStatusError(http.StatusNotFound, "localserver_task_not_found", "task_not_found", err)
	}
	return err
}

func (s *SkillSurface) currentTaskCloudClient() *cloud.Client {
	if s.taskCloudClient == nil {
		return nil
	}
	return s.taskCloudClient()
}

func (s *SkillSurface) checkTaskScope(ctx context.Context, taskUUID string) error {
	return checkTaskScope(ctx, s.db, s.currentTaskCloudClient(), taskUUID)
}

func (s *RemoteCommandService) taskScopeSQL() (string, []any) {
	var client *cloud.Client
	if s.tasks != nil {
		client = s.tasks.taskCloudClient()
	} else if s.skillSurface != nil {
		client = s.skillSurface.currentTaskCloudClient()
	}
	return taskScopeSQL(client, "t")
}

// taskScopeSQL restricts remote rows to the account that owns their sync queue.
// alias is a fixed SQL identifier supplied only by callers, never request input.
func taskScopeSQL(client *cloud.Client, alias string) (string, []any) {
	local := "COALESCE(" + alias + ".source_type,'local') <> 'cloud'"
	if client == nil || client.UserID() == "" {
		return local, nil
	}
	primary, alternate := pipeline.CloudSourceKeys(client.BaseURL())
	return "(" + local + " OR (" + alias + ".cloud_source_key IN (?, ?) AND " + alias + ".cloud_user_id=?))", []any{primary, alternate, client.UserID()}
}
