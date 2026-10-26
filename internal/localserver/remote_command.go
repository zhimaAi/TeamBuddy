package localserver

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"

	"goteams-client/internal/applog"
	"goteams-client/internal/executor"
	"goteams-client/internal/i18n"
	builtinskills "goteams-client/internal/skills"
	"goteams-client/internal/taskruntime"
	"goteams-client/internal/workflow"

	"github.com/google/uuid"
)

// wechatTask 是 gt_remote_task_aliases / gt_tasks 关联行的投影；core.Handle 各分支使用。
// 名称沿用历史；后续有需要再统一改为 RemoteTask（计划 §十六）。
type wechatTask struct {
	UUID            string
	ShortID         int64
	Title           string
	Status          string
	Mode            string
	Tool            string
	CurrentStepUUID string
	SessionJSON     string
}

// RemoteInboundMessage represents a normalized inbound message from any channel.
// transport 负责把平台消息映射成本结构后交给 core.Handle 处理。
type RemoteInboundMessage struct {
	ChannelType string // wechat/wecom/feishu/dingtalk
	MessageID   string // 平台去重 id（用于日志/辨识）
	InboxKey    string // inbox 主键（=uuid+":"+MessageID，transport 从 EnqueueInbox 返回值获得）
	SenderID    string // 发起者（用于单聊回复目标）
	ChatID      string // 会话/群 id（可选）
	ReplyTarget string // 主动发送目标（chatid 或 sessionWebhook 等）
	Content     string
}

// RemoteEnqueuer is the interface for enqueueing replies; transport 实现它，
// 负责把 reply 写到对应通道的 outbox。
type RemoteEnqueuer interface {
	Enqueue(ctx context.Context, channelType, sourceID, taskUUID, content string) error
}

// RemoteCommandService 持有各通道通用（无状态）的指令处理逻辑。
// 按方案 §七 必炸点 2：不得持有任何随消息变化的字段；可变状态留在 per-channel transport。
type RemoteCommandService struct {
	pinAccount         func(context.Context) (context.Context, func())
	db                 *sql.DB
	skillSurface       *SkillSurface
	tasks              *TasksHandler
	orchestratorGetter func() *workflow.Orchestrator
	taskRoot           string
	enqueuer           RemoteEnqueuer
	store              *channelStateStore
	// codexMonitors 等可变状态由 transport 持有，不在本结构内。
}

// NewRemoteCommandService 构造一个共享核心层实例。
func NewRemoteCommandService(
	db *sql.DB,
	store *channelStateStore,
	skillSurface *SkillSurface,
	tasks *TasksHandler,
	orchestratorGetter func() *workflow.Orchestrator,
	taskRoot string,
	enqueuer RemoteEnqueuer,
) *RemoteCommandService {
	return &RemoteCommandService{
		db:                 db,
		store:              store,
		skillSurface:       skillSurface,
		tasks:              tasks,
		orchestratorGetter: orchestratorGetter,
		taskRoot:           taskRoot,
		enqueuer:           enqueuer,
	}
}

// 远程指令匹配优先级（唯一权威说明；新增命令 / 意图 / 关键词时按此顺序落位）：
//
//	P1 有参数且参数合法 > 无参数。同一命令的带参形式优先（`/task -a` 优于 `/task`、`/ti -id=3` 优于 `/ti`）；
//	   参数不合法时不降级成无参形式，按「未匹配」处理（`/task -x` → CLI 兜底）。
//	P2 单项操作 > 单项查看 > 列表 > 帮助 > 未匹配。
//	   单项操作 = 对某一个任务发起动作：/c /r /d /n /swm /swcli /swr /ct /ctt /bind /unbind、取消 / 退出 / 暂停 / 恢复 / 停止 / 完成
//	   单项查看 = 查看某一个任务：/ti -id=<任务>、@<任务ID>、任务详情、当前绑定任务
//	   列表     = 不针对单个任务的列举：/task /tt /pro /pls /egs /clis /clim /vcl、列出所有任务
//	   帮助     = /help /h、帮助 / 怎么用、问候语（你好 / 在吗 / hello / hi，见 remoteIsGreeting）
//	   未匹配   = 以上都不命中：未绑定时转 CLI 兜底，已绑定时作为消息发给绑定任务
//
// 落点：本函数（快路径 → 帮助 → 二次确认 → @ 快捷 → 自然语言）、handleFastPathCommand（按精确命令名分派，
// 无交叉）、matchRound1Strict / matchRound2Keywords（分支按 P2 顺序排列）、splitAttachedCommandArgs（P1 的带参优先）。

// Handle 处理一条入站消息的主入口。
// 返回值给 transport：reply 写 outbox；taskUUID/sessionUUID 用于状态机推进。
// 算法与原 wechatRemote.handleMessage 等价，逐分支搬迁并按通道参数化。
func (s *RemoteCommandService) Handle(ctx context.Context, msg RemoteInboundMessage) (reply, taskUUID, sessionUUID string, err error) {
	if s.pinAccount != nil {
		var unlock func()
		ctx, unlock = s.pinAccount(ctx)
		defer unlock()
	}
	content := strings.TrimSpace(msg.Content)
	command, params := content, ""
	if split := strings.IndexByte(content, ' '); split >= 0 {
		command, params = content[:split], strings.TrimSpace(content[split+1:])
	}
	// 容错：命令与短参数之间漏写空格时按规范形式还原（/task-a → /task -a、/ti-id=3 → /ti -id=3）。
	// 只有命中已知命令 + 白名单参数才切分；否则原样返回，content 不变，消息仍走原有路径。
	command, params = s.splitAttachedCommandArgs(command, params)
	// 快路径指令（/task /t /T、/exit /q、/c /cancel、/r /resume、/d /done、/bind、/unbind、/tt 等）
	// 对应 P2 的单项操作 / 单项查看 / 列表，优先于「帮助」。
	if handled, r := s.handleFastPathCommand(ctx, msg.ChannelType, command, params); handled {
		return r, "", "", nil
	}
	// 帮助（P2 中次低一级，仅高于「未匹配」）：/help 显示完整命令清单（本地快路径 + 共享命令）。
	if command == "/help" || command == "/h" || command == "/H" {
		return s.tr(ctx, msg.ChannelType, "remote_channel_help_full", nil), "", "", nil
	}
	if err = s.ensureAliases(ctx); err != nil {
		return
	}
	// 二次确认优先于其他处理路径。
	var state ChannelState
	if sErr := s.readStateInto(ctx, msg.ChannelType, &state); sErr != nil && !errors.Is(sErr, sql.ErrNoRows) {
		err = sErr
		return
	}
	if handled, reply, rhTask, rhSession, rhErr := s.handlePendingConfirmations(ctx, &state, content); handled {
		return reply, rhTask, rhSession, rhErr
	}
	var task wechatTask
	if strings.HasPrefix(content, "@") {
		h, replyOut, taskOut, nextContent, errOut := s.handleAtShortcut(ctx, msg.ChannelType, content, &state)
		if errOut != nil {
			err = errOut
			return
		}
		if h {
			return replyOut, taskOut.UUID, "", nil
		}
		// handleAtShortcut 已写入 state.BoundTaskUUID 并把真实 task 透传过来；
		// 这里需要把 task 交给下游 dispatchTask，并把 content 替换为去掉 @id 前缀的指令，
		// 否则 dispatchTask 会用 zero value 的 task 触发 wechatConfigFailure。
		task = taskOut
		content = nextContent
	} else {
		if matched, intent, intentParam := s.matchNaturalLanguageIntent(content, state.BoundTaskUUID); matched {
			force := remoteContainsForceKeyword(content)
			return s.handleNaturalLanguageIntent(ctx, msg.ChannelType, msg.MessageID, state, intent, intentParam, force)
		}
		// 未绑定任务且未命中自然语言意图：尝试 CLI 兜底。
		if state.BoundTaskUUID == "" {
			return s.dispatchUnboundToCLIOrSkill(ctx, msg.ChannelType, msg.MessageID, content, "")
		}
		// 已绑定任务：直接派发到该任务。
		task, err = s.taskByUUID(ctx, state.BoundTaskUUID)
		if errors.Is(err, sql.ErrNoRows) {
			_ = s.clearBinding(ctx, msg.ChannelType)
			return s.dispatchUnboundToCLIOrSkill(ctx, msg.ChannelType, msg.MessageID, content, "")
		}
		if err != nil {
			return
		}
	}
	// 任务派发拦截
	if block, blockErr := remoteExecutionBlock(ctx, s.db, task.UUID); blockErr != nil {
		err = blockErr
		return
	} else if block == "uncertain" {
		return s.tr(ctx, msg.ChannelType, "remote_channel_uncertain_blocked", nil), task.UUID, "", nil
	}
	busy, busyErr := s.taskBusy(ctx, task.UUID)
	if busyErr != nil {
		err = busyErr
		return
	}
	if busy {
		return s.tr(ctx, msg.ChannelType, "remote_channel_busy", i18n.Params{"Task": task.Title}), task.UUID, "", nil
	}
	if err = s.markProcessingTask(ctx, msg.ChannelType, msg.InboxKey, task.UUID); err != nil {
		return "", task.UUID, "", err
	}
	sessionUUID, err = s.dispatchTask(ctx, msg.ChannelType, msg.MessageID, task, content)
	if errors.Is(err, errCodexOutcomeUnknown) {
		applog.Warn("远程指令下发结果不确定", "task_uuid", task.UUID, "channel_type", msg.ChannelType)
		return "", task.UUID, "", err
	}
	if err != nil {
		applog.Warn("远程指令下发失败", "task_uuid", task.UUID, "channel_type", msg.ChannelType, "error", err.Error())
		return s.dispatchErrorReply(ctx, msg.ChannelType, task, err), task.UUID, "", nil
	}
	return s.tr(ctx, msg.ChannelType, "remote_channel_send_ack", i18n.Params{"Task": task.Title, "Message": content}), task.UUID, sessionUUID, nil
}

// handleFastPathCommand 是本地快路径指令的开关。
// 该实现与历史 wechatRemote.handleFastPathCommand 对齐：覆盖计划 §五的命令表，
// 命中后立即同步返回，不创建任务、不拉 CLI。命令别名（/teamstask→/tt、/projects→/pro 等）
// 与参数解析（-key=value / -flag / 裸数字）通过 normalizeCommandAlias + parseArgs 处理。
func (s *RemoteCommandService) handleFastPathCommand(ctx context.Context, channelType, command, params string) (bool, string) {
	if s.skillSurface == nil {
		return false, ""
	}
	command = s.normalizeCommandAlias(command)
	args, rest := s.parseArgs(params)
	execCtx := s.channelCtx(ctx, channelType)
	switch command {
	case "/task", "/t", "/T":
		// 任务列表：active(默认/-r) / -a 全部 / -f 已完成 / -e 已阻塞。
		// params 不是空也不是这四种之一时（包含 -id= / 裸 ID），落到 Handle 的 @ 流程，
		// 这里返回 false 让上层继续走 handleAtShortcut。
		status := "active"
		switch params {
		case "", "-r":
		case "-a":
			status = ""
		case "-f":
			status = "done"
		case "-e":
			status = "blocked"
		default:
			return false, ""
		}
		out, err := s.skillSurface.WechatListTasks(execCtx, status)
		if err != nil {
			return true, s.wechatSkillFailure(ctx, channelType, out, err)
		}
		return true, out
	case "/exit", "/q", "/quit":
		reply, _, _, _ := s.handleExitTask(ctx, channelType)
		return true, reply
	case "/cancel", "/c":
		// -f 跳过二次确认；不带 -f 走 pending 状态由 handlePendingConfirmations 接力。
		reply, _, _, _ := s.handleCancelTask(ctx, channelType, params == "-f")
		return true, reply
	case "/resume", "/r":
		reply, _, _, _ := s.handleResumeTask(ctx, channelType, params == "-f")
		return true, reply
	case "/done", "/d":
		reply, _, _, _ := s.handleCompleteTask(ctx, channelType, params == "-f")
		return true, reply
	case "/bind", "/b":
		return true, s.handleBindCommand(ctx, channelType, params)
	case "/unbind", "/ub":
		return true, s.handleUnbindCommand(ctx, channelType)
	case "/today", "/tt":
		// -a: 全量（已含 done），不带 -a: 仅未完成。
		showAll := args.get("a") == "true" || args.get("a") == "1"
		out, err := s.skillSurface.WechatFormatTeamWork(execCtx, showAll)
		if err != nil {
			return true, s.wechatSkillFailure(ctx, channelType, out, err)
		}
		return true, out
	case "/projects", "/pro":
		out, err := s.skillSurface.WechatFormatProjects(execCtx)
		if err != nil {
			return true, s.wechatSkillFailure(ctx, channelType, out, err)
		}
		return true, out
	case "/cli-discovery", "/clis":
		out, err := s.skillSurface.WechatFormatCLIs(execCtx)
		if err != nil {
			return true, s.wechatSkillFailure(ctx, channelType, out, err)
		}
		return true, out
	case "/cli-models", "/clim":
		// /clim -cid=N：按 CLI 序号列出该 CLI 的可用模型；不带 -cid 直接拒参。
		idx, ok := args.intValue("cid")
		if !ok {
			return true, s.tr(ctx, channelType, "remote_channel_skill_missing_cli_index", nil)
		}
		out, err := s.skillSurface.WechatFormatModels(execCtx, idx)
		if err != nil {
			return true, s.wechatSkillFailure(ctx, channelType, out, err)
		}
		return true, out
	case "/pipelines", "/pls":
		out, err := s.skillSurface.WechatFormatPipelines(execCtx)
		if err != nil {
			return true, s.wechatSkillFailure(ctx, channelType, out, err)
		}
		return true, out
	case "/expert-groups", "/egs":
		out, err := s.skillSurface.WechatFormatExpertGroups(execCtx)
		if err != nil {
			return true, s.wechatSkillFailure(ctx, channelType, out, err)
		}
		return true, out
	case "/vibe-list", "/vcl":
		out, err := s.skillSurface.WechatFormatVibeTools(execCtx)
		if err != nil {
			return true, s.wechatSkillFailure(ctx, channelType, out, err)
		}
		return true, out
	case "/task-info", "/ti":
		// /ti -id=<任务>：查看任务详情；ref 缺失走 handleAtShortcut 兜底。
		ref := s.firstTaskRef(args, rest)
		if ref == "" {
			return true, s.tr(ctx, channelType, "remote_channel_skill_missing_task_ref", nil)
		}
		out, err := s.skillSurface.WechatFormatTaskDetail(execCtx, ref)
		if err != nil {
			return true, s.wechatSkillFailure(ctx, channelType, out, err)
		}
		return true, out
	case "/create-task", "/ct":
		// /ct -t=<标题> [-d=<描述>] [-w=<目录>] [-pid=<项目序号>] [-plid=<流水线序号>]
		//     [-egid=<专家团序号>] [-em=<执行方式>] [-et=<CLI>] [-emdl=<模型>]
		// 裸数字 token 当作 SubprojectIndex。
		body := createTaskBody{
			Title:       args.get("t"),
			Description: args.get("d"),
			WorkDirs:    s.splitDirs(args.get("w")),
		}
		if idx, ok := args.intValue("pid"); ok {
			body.ProjectIndex = idx
		}
		for _, tok := range strings.Fields(rest) {
			if n, err := strconv.Atoi(tok); err == nil && n > 0 {
				body.SubprojectIndex = append(body.SubprojectIndex, n)
			}
		}
		if idx, ok := args.intValue("plid"); ok {
			body.PipelineIndex = idx
		}
		if idx, ok := args.intValue("egid"); ok {
			body.ExpertGroupIdx = idx
		}
		if v := args.get("em"); v != "" {
			body.ExecutionMode = v
		}
		if v := args.get("et"); v != "" {
			body.ExecutionTool = v
		}
		if v := args.get("emdl"); v != "" {
			body.ModelName = v
		}
		if body.Title == "" {
			return true, s.tr(ctx, channelType, "remote_channel_skill_missing_title", nil)
		}
		out, err := s.skillSurface.WechatCreateTask(execCtx, body)
		if err != nil {
			return true, s.wechatSkillFailure(ctx, channelType, out, err)
		}
		return true, out
	case "/create-team-task", "/ctt":
		// /ctt -tid=<本地任务短 id> -ttid=<我的工作 ttid> [-t=<标题>]
		// -ttid 取的是 /tt 输出里 <...> 的一段（与显示端同源），与列表序号解耦。
		templateRef := args.get("tid")
		if templateRef == "" {
			return true, s.tr(ctx, channelType, "remote_channel_skill_missing_template_ref", nil)
		}
		workItemID, ok := s.firstTeamWorkID(args, rest)
		if !ok {
			return true, s.tr(ctx, channelType, "remote_channel_skill_missing_team_work_index", nil)
		}
		out, err := s.skillSurface.WechatCreateTeamTaskFromTemplate(execCtx, templateRef, workItemID, args.get("t"))
		if err != nil {
			return true, s.wechatSkillFailure(ctx, channelType, out, err)
		}
		return true, out
	case "/n":
		// /n -id=<任务>：推进任务到下一步；末步返回「已经是最后一步了」，不会自动完成。
		ref := s.firstTaskRef(args, rest)
		if ref == "" {
			return true, s.tr(ctx, channelType, "remote_channel_skill_missing_task_ref", nil)
		}
		out, err := s.skillSurface.WechatNextStepByRef(execCtx, ref)
		if err != nil {
			return true, s.wechatSkillFailure(ctx, channelType, out, err)
		}
		return true, out
	case "/switch-model", "/swm":
		// /swm -id=<任务> [-cid=<CLI序号|cli_type>] [-mid=<模型序号>] [-emdl=<模型名>]
		// 三者必有其一；只给 -mid 时 CLI 沿用任务当前执行方式。
		in := WechatSwitchInput{
			Ref:       s.firstTaskRef(args, rest),
			Mode:      taskruntime.ExecutionModeCLI,
			ModelName: args.get("emdl"),
		}
		mid, midOK, midErr := s.parseModelIndexArg(args.get("mid"))
		if midErr != nil {
			return true, s.trSkillErr(ctx, channelType, midErr)
		}
		if v := args.get("cid"); v != "" {
			// -cid= 直接传 cli_type 时按值形式；序号才走解析。
			if n, err := strconv.Atoi(v); err == nil && n > 0 {
				cliType, modelName, mErr := s.resolveModelFromArgs(ctx, channelType, n, args.get("mid"))
				if mErr != nil {
					return true, s.trSkillErr(ctx, channelType, mErr)
				}
				in.Tool = cliType
				if in.ModelName == "" {
					in.ModelName = modelName
				}
			} else {
				in.Tool = strings.ToLower(strings.TrimSpace(v))
			}
		} else if midOK {
			// 独立 -mid：CLI 由任务当前执行态决定。
			in.ModelIndex = mid
		} else if in.ModelName == "" {
			return true, s.tr(ctx, channelType, "remote_channel_skill_missing_model_index", nil)
		}
		if in.Ref == "" {
			return true, s.tr(ctx, channelType, "remote_channel_skill_missing_task_ref", nil)
		}
		out, err := s.skillSurface.WechatSwitchExecution(execCtx, in)
		if err != nil {
			return true, s.wechatSkillFailure(ctx, channelType, out, err)
		}
		return true, out
	case "/switch-run", "/swr":
		// /swr -id=<任务> -t=<mode> [-emdl=<模型>] [-plid=<流水线序号>] [-egid=<专家团序号>] [-vid=<vibe 序号>]
		// -t 接收 vcl/cli/pls/egs 等短名（mapSwitchMode 映射）。
		mode := s.mapSwitchMode(args.get("t"))
		if mode == "" {
			return true, s.tr(ctx, channelType, "remote_channel_skill_missing_mode", nil)
		}
		in := WechatSwitchInput{
			Ref:             s.firstTaskRef(args, rest),
			Mode:            mode,
			ModelName:       args.get("emdl"),
			ReplaceExisting: true,
		}
		if idx, ok := args.intValue("plid"); ok {
			in.PipelineIdx = idx
		}
		if idx, ok := args.intValue("egid"); ok {
			in.ExpertGroupIdx = idx
		}
		if idx, ok := args.intValue("vid"); ok {
			in.VibeIdx = idx
		}
		if in.Ref == "" {
			return true, s.tr(ctx, channelType, "remote_channel_skill_missing_task_ref", nil)
		}
		out, err := s.skillSurface.WechatSwitchExecution(execCtx, in)
		if err != nil {
			return true, s.wechatSkillFailure(ctx, channelType, out, err)
		}
		return true, out
	case "/switch-cli", "/swcli":
		// /swcli -id=<任务> -cid=<CLI序号|cli_type> [-mid=<模型序号>] [-emdl=<模型名>]
		// -cid 与 -mid/-emdl 必须显式给出，避免静默补全导致落 CLI。
		in := WechatSwitchInput{
			Ref:       s.firstTaskRef(args, rest),
			Mode:      taskruntime.ExecutionModeCLI,
			ModelName: args.get("emdl"),
		}
		cidRaw := args.get("cid")
		if cidRaw == "" {
			return true, s.tr(ctx, channelType, "remote_channel_skill_missing_cli_index", nil)
		}
		if cliIdx, cidErr := strconv.Atoi(cidRaw); cidErr == nil && cliIdx > 0 {
			switch {
			case in.ModelName != "":
				// -emdl 显式给模型名：只借 CLI 序号取 tool，模型名以显式值为准。
				cliDef, defErr := s.skillSurface.resolveCLIIndex(execCtx, cliIdx)
				if defErr != nil {
					return true, s.trSkillErr(ctx, channelType, defErr)
				}
				in.Tool = cliDef.Type
			case args.get("mid") == "":
				return true, s.tr(ctx, channelType, "remote_channel_skill_missing_model_index", nil)
			default:
				cliType, modelName, mErr := s.resolveModelFromArgs(ctx, channelType, cliIdx, args.get("mid"))
				if mErr != nil {
					return true, s.trSkillErr(ctx, channelType, mErr)
				}
				in.Tool = cliType
				in.ModelName = modelName
			}
		} else if in.ModelName != "" {
			// -cid= 直接是 cli_type 名称：模型名必须显式给出。
			in.Tool = strings.ToLower(strings.TrimSpace(cidRaw))
		} else {
			return true, s.tr(ctx, channelType, "remote_channel_skill_missing_cli_index", nil)
		}
		if in.Ref == "" {
			return true, s.tr(ctx, channelType, "remote_channel_skill_missing_task_ref", nil)
		}
		out, err := s.skillSurface.WechatSwitchExecution(execCtx, in)
		if err != nil {
			return true, s.wechatSkillFailure(ctx, channelType, out, err)
		}
		return true, out
	}
	return false, ""
}

// handleBindCommand / /unbind 命令的统一实现（参数化 channelType）。
func (s *RemoteCommandService) handleBindCommand(ctx context.Context, channelType, args string) string {
	trimmed := strings.TrimSpace(args)
	var shortIDStr string
	if strings.HasPrefix(trimmed, "@") {
		shortIDStr = trimmed[1:]
	} else if id := remoteExtractTaskID(trimmed); id != "" {
		shortIDStr = id
	}
	if shortIDStr == "" {
		return s.tr(ctx, channelType, "remote_channel_bind_usage", nil)
	}
	taskUUID, err := s.taskByShortID(ctx, shortIDStr)
	if err != nil {
		return s.tr(ctx, channelType, "remote_channel_task_not_found", i18n.Params{"ID": shortIDStr})
	}
	state, _ := s.store.ReadState(ctx, channelType)
	state.ChannelType = channelType
	state.BoundTaskUUID = taskUUID
	state.PendingCancelTaskUUID = ""
	state.PendingResumeTaskUUID = ""
	state.PendingDoneTaskUUID = ""
	if err := s.store.WriteState(ctx, state); err != nil {
		return s.tr(ctx, channelType, "remote_channel_processing_failed", i18n.Params{
			"Detail": err.Error(),
			"Help":   s.tr(ctx, channelType, "remote_channel_help", nil),
		})
	}
	task, _ := s.taskByUUID(ctx, taskUUID)
	return s.tr(ctx, channelType, "remote_channel_bind_success", i18n.Params{"Task": task.Title})
}

func (s *RemoteCommandService) handleUnbindCommand(ctx context.Context, channelType string) string {
	state, _ := s.store.ReadState(ctx, channelType)
	state.ChannelType = channelType
	state.BoundTaskUUID = ""
	state.PendingCancelTaskUUID = ""
	state.PendingResumeTaskUUID = ""
	state.PendingDoneTaskUUID = ""
	if err := s.store.WriteState(ctx, state); err != nil {
		return s.tr(ctx, channelType, "remote_channel_processing_failed", i18n.Params{
			"Detail": err.Error(),
			"Help":   s.tr(ctx, channelType, "remote_channel_help", nil),
		})
	}
	return s.tr(ctx, channelType, "remote_channel_unbind_success", nil)
}

// handleAtShortcut @ 开头的快捷切换 / 列出非完成任务。
// 返回 handled=false 表示没有 @ 前缀，让主流程处理其他分支。
func (s *RemoteCommandService) handleAtShortcut(ctx context.Context, channelType, content string, state *ChannelState) (handled bool, reply string, task wechatTask, nextContent string, err error) {
	payload := strings.TrimPrefix(content, "@")
	if payload == "" || strings.TrimSpace(payload) == "" {
		execCtx := s.channelCtx(ctx, channelType)
		out, listErr := s.skillSurface.WechatListTasks(execCtx, "not_done")
		if listErr != nil || strings.TrimSpace(out) == "" {
			errOut, _ := s.listTasks(ctx, "not_done")
			out = errOut
		}
		return true, out, task, "", nil
	}
	idText, instruction := payload, ""
	if split := strings.IndexFunc(payload, unicode.IsSpace); split >= 0 {
		idText, instruction = payload[:split], strings.TrimSpace(payload[split:])
	}
	shortID, parseErr := strconv.ParseInt(idText, 10, 64)
	if parseErr != nil || shortID < 1 {
		return true, s.tr(ctx, channelType, "remote_channel_id_invalid", i18n.Params{"Help": s.tr(ctx, channelType, "remote_channel_help", nil)}), task, "", nil
	}
	t, lookupErr := s.taskByShortIDFull(ctx, shortID)
	if errors.Is(lookupErr, sql.ErrNoRows) {
		return true, s.tr(ctx, channelType, "remote_channel_task_missing", i18n.Params{"Help": s.tr(ctx, channelType, "remote_channel_help", nil)}), task, "", nil
	}
	if lookupErr != nil {
		return false, "", task, "", lookupErr
	}
	st, _ := s.store.ReadState(ctx, channelType)
	st.ChannelType = channelType
	st.BoundTaskUUID = t.UUID
	st.PendingCancelTaskUUID = ""
	st.PendingResumeTaskUUID = ""
	st.PendingDoneTaskUUID = ""
	if writeErr := s.store.WriteState(ctx, st); writeErr != nil {
		return false, "", task, "", writeErr
	}
	if instruction == "" {
		last, _ := s.latestFinal(ctx, t)
		return true, s.tr(ctx, channelType, "remote_channel_switch", i18n.Params{"Task": t.Title, "Result": last}), t, "", nil
	}
	// 形如「@29 xxx」的指令。master 版本的 wechatRemote.handleMessage 在同函数内一气完成
	// `task, err = w.taskByShortID(...)` + `content = instruction`，所以派发可
	// 拿到正确的 task 与去掉 @id 前缀后的真实指令。重构把 @ 分支抽到 handleAtShortcut 后，
	// 必须把 task 与 instruction 同时回传，由 Handle 继续派发；否则 dispatchTask 会收到
	// zero value 的 task，task.Mode 为空触发 wechatConfigFailure("execution_mode", ...)，
	// 错误文案为「任务执行配置未完成：请在 TeamBuddy 中选择受支持的执行方式。」
	*state = st
	return false, "", t, instruction, nil
}

// dispatchUnboundToCLIOrSkill 处理未绑定任务且无自然语言意图时的兜底：
// - 已配置且启用的 CLI → 走 dispatchToCLI 创建兜底任务
// - 已配置但关闭 → 引导开启
// - 未配置 → 引导配置
func (s *RemoteCommandService) dispatchUnboundToCLIOrSkill(ctx context.Context, channelType, messageID, content, _ string) (reply, taskUUID, sessionUUID string, err error) {
	cliConfig := s.readChannelCLIConfig(ctx, channelType)
	if cliConfig.CLICType == "" {
		return s.tr(ctx, channelType, "remote_channel_cli_unconfigured_for_dispatch",
			i18n.Params{"Help": s.tr(ctx, channelType, "remote_channel_help", nil)}), "", "", nil
	}
	if !cliConfig.Enabled {
		return s.tr(ctx, channelType, "remote_channel_cli_disabled_for_dispatch",
			i18n.Params{"Help": s.tr(ctx, channelType, "remote_channel_help", nil)}), "", "", nil
	}
	cliTaskUUID, cliSessionUUID, cliErr := s.dispatchToCLI(ctx, channelType, messageID, content, "", cliConfig.CLICType, cliConfig.CLIModel)
	if cliErr != nil {
		applog.Warn("远程消息转发CLI失败", "message_id", messageID, "channel", channelType, "error", cliErr.Error())
		return s.tr(ctx, channelType, "remote_channel_processing_failed",
			i18n.Params{
				"Detail": cliErr.Error(),
				"Help":   s.tr(ctx, channelType, "remote_channel_help", nil),
			}), "", "", nil
	}
	if cliSessionUUID != "" {
		_ = s.ensureAliases(ctx)
		var shortID int64
		_ = s.db.QueryRowContext(ctx, `SELECT short_id FROM gt_remote_task_aliases WHERE task_uuid=?`, cliTaskUUID).Scan(&shortID)
		ackReply := s.tr(ctx, channelType, "remote_channel_cli_fallback_ack", i18n.Params{"ID": shortID, "CLI": cliConfig.CLICType})
		return ackReply, cliTaskUUID, cliSessionUUID, nil
	}
	return "", "", "", nil
}

// ============================================================================
// 状态 / 任务查找
// ============================================================================

func (s *RemoteCommandService) readStateInto(ctx context.Context, channelType string, out *ChannelState) error {
	state, err := s.store.ReadState(ctx, channelType)
	if err != nil {
		return err
	}
	*out = state
	return nil
}

func (s *RemoteCommandService) clearBinding(ctx context.Context, channelType string) error {
	state, err := s.store.ReadState(ctx, channelType)
	if err != nil {
		return err
	}
	state.BoundTaskUUID = ""
	state.PendingCancelTaskUUID = ""
	state.PendingResumeTaskUUID = ""
	state.PendingDoneTaskUUID = ""
	return s.store.WriteState(ctx, state)
}

func (s *RemoteCommandService) ensureAliases(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `INSERT OR IGNORE INTO gt_remote_task_aliases(task_uuid, created_at)
		SELECT t.uuid, ? FROM gt_tasks t
		WHERE NOT EXISTS (SELECT 1 FROM gt_remote_task_aliases a WHERE a.task_uuid=t.uuid)
		ORDER BY t.created_at, t.rowid`, time.Now().UnixMilli())
	return err
}

func (s *RemoteCommandService) taskByShortIDFull(ctx context.Context, shortID int64) (wechatTask, error) {
	scopeSQL, args := s.taskScopeSQL()
	var task wechatTask
	err := s.db.QueryRowContext(ctx, `SELECT t.uuid, a.short_id, t.title, t.status, t.execution_mode,
		t.execution_tool, t.current_step_uuid, t.vibe_coding_session_json
		FROM gt_remote_task_aliases a JOIN gt_tasks t ON t.uuid=a.task_uuid WHERE a.short_id=? AND `+scopeSQL, append([]any{shortID}, args...)...).
		Scan(&task.UUID, &task.ShortID, &task.Title, &task.Status, &task.Mode, &task.Tool, &task.CurrentStepUUID, &task.SessionJSON)
	return task, err
}

func (s *RemoteCommandService) taskByUUID(ctx context.Context, taskUUID string) (wechatTask, error) {
	scopeSQL, args := s.taskScopeSQL()
	var task wechatTask
	err := s.db.QueryRowContext(ctx, `SELECT t.uuid, a.short_id, t.title, t.status, t.execution_mode,
		t.execution_tool, t.current_step_uuid, t.vibe_coding_session_json
		FROM gt_remote_task_aliases a JOIN gt_tasks t ON t.uuid=a.task_uuid WHERE t.uuid=? AND `+scopeSQL, append([]any{taskUUID}, args...)...).
		Scan(&task.UUID, &task.ShortID, &task.Title, &task.Status, &task.Mode, &task.Tool, &task.CurrentStepUUID, &task.SessionJSON)
	return task, err
}

// taskByShortID 是 plan §十六 中的字符串版（保持与 wechatRemote 一致）。
func (s *RemoteCommandService) taskByShortID(ctx context.Context, shortID string) (string, error) {
	id, err := strconv.ParseInt(shortID, 10, 64)
	if err != nil {
		return "", fmt.Errorf("invalid short ID: %s", shortID)
	}
	task, err := s.taskByShortIDFull(ctx, id)
	if err != nil {
		return "", err
	}
	return task.UUID, nil
}

func (s *RemoteCommandService) listTasks(ctx context.Context, status string) (string, error) {
	if err := s.ensureAliases(ctx); err != nil {
		return "", err
	}
	query := `SELECT a.short_id, t.title FROM gt_remote_task_aliases a JOIN gt_tasks t ON t.uuid=a.task_uuid`
	scopeSQL, args := s.taskScopeSQL()
	query += ` WHERE ` + scopeSQL
	if status == "not_done" {
		query += ` AND t.status != 'done'`
	} else if status != "" {
		query += ` AND t.status=?`
		args = append(args, status)
	}
	query += ` ORDER BY a.short_id`
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	var lines []string
	for rows.Next() {
		var id int64
		var title string
		if err = rows.Scan(&id, &title); err != nil {
			return "", err
		}
		lines = append(lines, fmt.Sprintf("@%d %s", id, title))
	}
	if err = rows.Err(); err != nil {
		return "", err
	}
	params := i18n.Params{"Count": len(lines), "Tasks": strings.Join(lines, "\n\n")}
	if len(lines) == 0 {
		return s.tr(ctx, ChannelTypeWechat, "remote_channel_empty_tasks", params), nil
	}
	return s.tr(ctx, ChannelTypeWechat, "remote_channel_list", params), nil
}

func (s *RemoteCommandService) latestFinal(ctx context.Context, task wechatTask) (string, error) {
	var result string
	var err error
	if task.Mode == taskruntime.ExecutionModeVibeCoding {
		err = s.db.QueryRowContext(ctx, `SELECT final_result FROM gt_task_progress WHERE task_uuid=?
			AND execution_mode='vibe_coding' AND record_type<>'user_question' AND final_result<>''
			AND activity_kind<>'assignment' AND uuid<>?
			ORDER BY created_at DESC, rowid DESC LIMIT 1`, task.UUID, vibeCodingAssignmentProgressUUID(task.UUID)).Scan(&result)
	} else {
		err = s.db.QueryRowContext(ctx, `SELECT final_result FROM gt_cli_sessions WHERE task_uuid=?
			AND status IN ('success','failed','stopped','interrupted') AND final_result<>''
			ORDER BY created_at DESC, rowid DESC LIMIT 1`, task.UUID).Scan(&result)
	}
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return strings.TrimSpace(result), err
}

func (s *RemoteCommandService) taskBusy(ctx context.Context, taskUUID string) (bool, error) {
	var active int
	err := s.db.QueryRowContext(ctx, `SELECT
		(SELECT COUNT(*) FROM gt_cli_sessions WHERE task_uuid=? AND status IN ('created','running','waiting_input','stop_requested'))
		+ (SELECT COUNT(*) FROM gt_task_progress WHERE task_uuid=? AND dispatch_status='routing_pending')
		+ (SELECT COUNT(*) FROM `+remoteInboxTable+` WHERE task_uuid=? AND status IN ('dispatched','uncertain'))`, taskUUID, taskUUID, taskUUID).Scan(&active)
	return active > 0, err
}

func (s *RemoteCommandService) expertChainBusy(ctx context.Context, taskUUID, chain string) (bool, error) {
	var active int
	err := s.db.QueryRowContext(ctx, `SELECT
		(SELECT COUNT(*) FROM gt_cli_sessions WHERE task_uuid=? AND expert_chain_uuid=?
			AND status IN ('created','running','waiting_input','stop_requested'))
		+ (SELECT COUNT(*) FROM gt_task_progress p JOIN gt_cli_sessions s ON s.uuid=p.session_uuid
			WHERE s.task_uuid=? AND s.expert_chain_uuid=? AND p.dispatch_status='routing_pending')`, taskUUID, chain, taskUUID, chain).Scan(&active)
	return active > 0, err
}

// ============================================================================
// 队列状态机 / 任务派发
// ============================================================================

func (s *RemoteCommandService) markProcessingTask(ctx context.Context, channelType, messageID, taskUUID string) error {
	// 接受 pending（transport 回调直调 Handle）和 processing（workLoop 经 ClaimInbox 后）两种来源；
	// 统一把状态推进到 processing 并写入 task_uuid。
	result, err := s.db.ExecContext(ctx, `UPDATE `+remoteInboxTable+` SET status='processing', task_uuid=?, updated_at=?
		WHERE message_id=? AND channel_type=? AND status IN ('pending','processing')`, taskUUID, time.Now().UnixMilli(), messageID, channelType)
	if err != nil {
		return err
	}
	updated, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if updated != 1 {
		return errors.New("远程指令已取消")
	}
	return nil
}

func (s *RemoteCommandService) finishProcessing(ctx context.Context, channelType, id, status, taskUUID, sessionUUID, reply string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `UPDATE `+remoteInboxTable+` SET status=?, task_uuid=?, session_uuid=?, updated_at=?
		WHERE message_id=? AND channel_type=? AND status='processing'`, status, taskUUID, sessionUUID, time.Now().UnixMilli(), id, channelType); err != nil {
		return err
	}
	if reply != "" {
		// 从 inbox 读 sender_id / session_webhook，用于构造 reply_context。
		// feishu/wecom/dingtalk 的 envelope 路径在 FinishInbound 里直接传入 reply_context，
		// finishProcessing 是 ProcessInbox → Handle 后唯一收尾入口（wechat 走它，其他通道的
		// envelope 已先于 ProcessInbox 改写过 inbox 状态，这里再 INSERT 时 reply_context 必须
		// 按 inbox.sender_id 重建，否则像 wechat 一样会因目标 user 缺失导致投递失败。
		var senderID, sessionWebhook string
		_ = tx.QueryRowContext(ctx, `SELECT sender_id, session_webhook FROM `+remoteInboxTable+`
			WHERE message_id=? AND channel_type=?`, id, channelType).Scan(&senderID, &sessionWebhook)
		replyCtx := buildReplyContext(channelType, senderID, sessionWebhook)
		sourceID := "reply:" + id
		if _, err = tx.ExecContext(ctx, `INSERT INTO `+remoteOutboxTable+`
			(id, source_id, channel_type, task_uuid, content, status, blocked_context_revision, attempts, next_attempt_at, created_at, reply_context)
			VALUES (?, ?, ?, ?, ?, 'pending', -1, 0, ?, ?, ?)`,
			uuid.NewString(),
			sourceID, channelType, taskUUID, reply,
			time.Now().UnixMilli(), time.Now().UnixMilli(), replyCtx,
		); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// FinishInbound 是 transport 回调（onChatbotMessage / onMessageReceive / onIncoming）在 Handle 返回后调用的统一收尾入口。
// 与 finishProcessing 的差异：finishProcessing 仅作用于 status='processing' 的行（来自 ProcessInbox/ClaimInbox 路径）；
// FinishInbound 同时接受 'pending'（来自 EnqueueInbox 的直接回调路径）和 'processing' 两种来源。
// 若 handleErr 非空且 reply 为空，会写入翻译后的兜底回复（remote_channel_processing_failed / remote_channel_dispatch_uncertain），
// 保证 outbox 一定有可发送的回复，避免出现「inbox 有记录但 outbox 为空、用户收不到回复」的故障。
// replyContext 写入 outbox.reply_context（通道私有目标，如 session_webhook / chat_id / req_id），为空时按通道默认逻辑处理。
func (s *RemoteCommandService) FinishInbound(ctx context.Context, channelType, messageID, taskUUID, sessionUUID, reply, replyContext string, handleErr error) error {
	if handleErr != nil {
		applog.Warn("远程指令处理失败", "channel_type", channelType, "message_id", messageID, "error", handleErr.Error())
		if reply == "" {
			if errors.Is(handleErr, errCodexOutcomeUnknown) {
				reply = s.tr(ctx, channelType, "remote_channel_dispatch_uncertain", nil)
			} else {
				reply = s.tr(ctx, channelType, "remote_channel_processing_failed", i18n.Params{
					"Detail": handleErr.Error(),
					"Help":   s.tr(ctx, channelType, "remote_channel_help", nil),
				})
			}
		}
	}
	status := "done"
	if errors.Is(handleErr, errCodexOutcomeUnknown) {
		status = "uncertain"
	}
	if sessionUUID != "" {
		status = "dispatched"
	}
	return s.finishInbound(ctx, channelType, messageID, status, taskUUID, sessionUUID, reply, replyContext)
}

func (s *RemoteCommandService) finishInbound(ctx context.Context, channelType, id, status, taskUUID, sessionUUID, reply, replyContext string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	// inbox 表的 message_id 列存的就是 inboxKey (uuid:originalMessageID)，按 id 命中。
	if _, err = tx.ExecContext(ctx, `UPDATE `+remoteInboxTable+` SET status=?, task_uuid=?, session_uuid=?, updated_at=?
		WHERE message_id=? AND channel_type=?`, status, taskUUID, sessionUUID, time.Now().UnixMilli(), id, channelType); err != nil {
		return err
	}
	if reply != "" {
		sourceID := "reply:" + id
		if _, err = tx.ExecContext(ctx, `INSERT INTO `+remoteOutboxTable+`
			(id, source_id, channel_type, task_uuid, content, status, blocked_context_revision, attempts, next_attempt_at, created_at, reply_context)
			VALUES (?, ?, ?, ?, ?, 'pending', -1, 0, ?, ?, ?)`,
			uuid.NewString(),
			sourceID, channelType, taskUUID, reply,
			time.Now().UnixMilli(), time.Now().UnixMilli(), replyContext,
		); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *RemoteCommandService) finishDispatched(ctx context.Context, channelType, id, taskUUID, reply string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `UPDATE `+remoteInboxTable+` SET status='done', updated_at=?
		WHERE message_id=? AND channel_type=? AND status='dispatched'`, time.Now().UnixMilli(), id, channelType)
	if err != nil {
		return err
	}
	updated, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if updated == 0 {
		return nil
	}

	// 从 inbox 读 sender_id / session_webhook，用于构造 reply_context。
	// finishInbound 路径从 transport 拿 reply_context，这里 finishDispatched 无外部调用方，
	// 必须自行从 inbox 重建目标标识，否则 sendOutbox 会因空 reply_context 拒绝投递。
	var senderID, sessionWebhook string
	_ = tx.QueryRowContext(ctx, `SELECT sender_id, session_webhook FROM `+remoteInboxTable+`
		WHERE message_id=? AND channel_type=?`, id, channelType).Scan(&senderID, &sessionWebhook)
	replyCtx := buildReplyContext(channelType, senderID, sessionWebhook)

	sourceID := "result:" + id
	if _, err = tx.ExecContext(ctx, `INSERT INTO `+remoteOutboxTable+`
		(id, source_id, channel_type, task_uuid, content, status, blocked_context_revision, attempts, next_attempt_at, created_at, reply_context)
		VALUES (?, ?, ?, ?, ?, 'pending', -1, 0, ?, ?, ?)`,
		uuid.NewString(),
		sourceID, channelType, taskUUID, reply,
		time.Now().UnixMilli(), time.Now().UnixMilli(), replyCtx,
	); err != nil {
		return err
	}
	return tx.Commit()
}

// buildReplyContext 按通道编码 reply_context，供 finishDispatched 在无 transport 上下文时重建目标。
// 各通道 sendOutbox 通过 splitReplyContext 解析本函数输出的格式。
func buildReplyContext(channelType, senderID, sessionWebhook string) string {
	switch channelType {
	case ChannelTypeFeishu:
		// 飞书 p2p 用 open_id，群聊（feishuReplyContext 写入 chat_id:xxx）暂以 sender_id 回退；
		// 若历史行 sender_id 也是 open_id 则能正常投递；若为群聊则需要 inbox 加 chat_id 列。
		if senderID != "" {
			return "open_id:" + senderID
		}
	case ChannelTypeWecom:
		// 企微单聊用 single:userid，群聊（wecomReplyContext 写入 group:chatid）暂以 sender_id 回退。
		if senderID != "" {
			return "single:" + senderID
		}
	case ChannelTypeDingtalk:
		// 钉钉优先复用 session_webhook（chatbot 回调携带，最可靠），失效时回退 staff_id。
		if sessionWebhook != "" {
			return "session_webhook:" + sessionWebhook
		}
		if senderID != "" {
			return "user_id:" + senderID
		}
	case ChannelTypeWechat:
		// 微信单聊把入站消息对应的 sender_id 直接作为回复目标；
		// wechatRemote.sendOutboxContent 按 "sender_id:USERID" 前缀解析并作为 to_user_id 回传 iLink。
		// 发送侧 client_id 由 wechatAPI.sendText 每次即时生成新的 UUID（参考
		// corespeed-io/wechatbot::BuildTextMessage），与本字段解耦，因此 reply_context
		// 不需要再携带 client_id / context_token 这类会话路由信息。
		if senderID != "" {
			return "sender_id:" + senderID
		}
	}
	// 空时由 sendOutbox 自身报错（reply_context 为空 → 拒绝投递），避免静默吞掉消息。
	return ""
}

func (s *RemoteCommandService) deferCLIResult(ctx context.Context, channelType, messageID string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE `+remoteInboxTable+` SET next_recovery_at=?
		WHERE message_id=? AND channel_type=? AND status='dispatched'`, time.Now().Add(5*time.Second).UnixMilli(), messageID, channelType)
	return err
}

// CollectResults 回收 dispatched 状态的消息结果（按通道 claim）；exported for transport 工作循环。
func (s *RemoteCommandService) CollectResults(ctx context.Context, channelType string) error {
	rows, err := s.db.QueryContext(ctx, `SELECT message_id, task_uuid, session_uuid FROM `+remoteInboxTable+`
		WHERE channel_type=? AND status='dispatched' AND session_uuid NOT LIKE 'codex:%' AND next_recovery_at<=?
		ORDER BY next_recovery_at, received_at LIMIT 16`, channelType, time.Now().UnixMilli())
	if err != nil {
		return err
	}
	defer rows.Close()
	type item struct{ id, task, session string }
	var pending []item
	for rows.Next() {
		var v item
		if err = rows.Scan(&v.id, &v.task, &v.session); err != nil {
			return err
		}
		pending = append(pending, v)
	}
	if err = rows.Err(); err != nil {
		return err
	}
	for _, v := range pending {
		var status, final string
		err = s.db.QueryRowContext(ctx, `SELECT status, final_result FROM gt_cli_sessions WHERE uuid=?`, v.session).Scan(&status, &final)
		if errors.Is(err, sql.ErrNoRows) {
			if err := s.finishDispatched(ctx, channelType, v.id, v.task, s.tr(ctx, channelType, "remote_channel_result_unavailable", nil)); err != nil {
				return err
			}
			continue
		}
		if err != nil {
			return err
		}
		if !workflow.IsTerminalStatus(status) {
			if err := s.deferCLIResult(ctx, channelType, v.id); err != nil {
				return err
			}
			continue
		}
		var mode, title string
		err = s.db.QueryRowContext(ctx, `SELECT execution_mode,title FROM gt_tasks WHERE uuid=?`, v.task).Scan(&mode, &title)
		if errors.Is(err, sql.ErrNoRows) {
			title = s.tr(ctx, channelType, "remote_channel_task_deleted_title", nil)
		} else if err != nil {
			return err
		}
		if mode == taskruntime.ExecutionModeExpertGroup {
			var chain string
			if err = s.db.QueryRowContext(ctx, `SELECT expert_chain_uuid FROM gt_cli_sessions WHERE uuid=?`, v.session).Scan(&chain); err != nil {
				return err
			}
			busy, busyErr := s.expertChainBusy(ctx, v.task, chain)
			if busyErr != nil {
				return busyErr
			}
			if busy {
				if err := s.deferCLIResult(ctx, channelType, v.id); err != nil {
					return err
				}
				continue
			}
			err = s.db.QueryRowContext(ctx, `SELECT final_result FROM gt_cli_sessions WHERE task_uuid=?
				AND expert_chain_uuid=? AND status IN ('success','failed','stopped','interrupted')
				ORDER BY expert_chain_round DESC,created_at DESC,rowid DESC LIMIT 1`, v.task, chain).Scan(&final)
			if err != nil {
				return err
			}
		}
		var shortID int64
		_ = s.db.QueryRowContext(ctx, `SELECT short_id FROM gt_remote_task_aliases WHERE task_uuid=?`, v.task).Scan(&shortID)
		body := s.tr(ctx, channelType, "remote_channel_result", i18n.Params{"ID": shortID, "Task": title, "Result": strings.TrimSpace(final)})
		if err = s.finishDispatched(ctx, channelType, v.id, v.task, body); err != nil {
			return err
		}
	}
	return nil
}

// dispatchTask 派发按 task 模式分支；与原 wechatRemote.dispatchTask 等价。
func (s *RemoteCommandService) dispatchTask(ctx context.Context, channelType, messageID string, task wechatTask, content string) (string, error) {
	orch := s.orchestratorGetter()
	if orch == nil {
		return "", errors.New("任务执行服务不可用")
	}
	if task.Mode == taskruntime.ExecutionModeVibeCoding {
		if task.Tool != taskruntime.ExecutionToolCodex {
			return "", wechatConfigFailure("execution_mode", "remote_wechat_config_mode")
		}
		// Codex 委托给 transport 持有 codex monitor；这里返回特殊错误让上层决定如何处理。
		// 实际派发由 wechatRemote.dispatchCodex 完成；企微/飞书/钉钉本阶段不接 vibe coding。
		return "", errors.New("vibe coding 由通道 transport 处理")
	}
	if task.Mode != taskruntime.ExecutionModeCLI && task.Mode != taskruntime.ExecutionModePipeline &&
		task.Mode != taskruntime.ExecutionModeExpertGroup {
		return "", wechatConfigFailure("execution_mode", "remote_wechat_config_mode")
	}
	if err := s.validateDispatchWorkspace(ctx, task.UUID); err != nil {
		return "", err
	}
	if task.Mode == taskruntime.ExecutionModeExpertGroup {
		var stepUUID string
		err := s.db.QueryRowContext(ctx, `SELECT leader_task_step_uuid FROM gt_task_expert_group_snapshots WHERE task_uuid=?`,
			task.UUID).Scan(&stepUUID)
		if errors.Is(err, sql.ErrNoRows) || (err == nil && stepUUID == "") {
			return "", wechatConfigFailure("expert_group", "remote_wechat_config_expert")
		}
		if err != nil {
			return "", err
		}
		if err := s.validateDispatchStep(ctx, task.UUID, stepUUID); err != nil {
			return "", err
		}
		return orch.StartExpertMessage(ctx, task.UUID, "", content, content, messageID)
	}
	if task.CurrentStepUUID == "" {
		if task.Mode != taskruntime.ExecutionModePipeline {
			return "", wechatConfigFailure("step", "remote_wechat_config_step")
		}
		stepUUID, err := s.preparePipelineMessage(ctx, task)
		if err != nil {
			return "", err
		}
		task.CurrentStepUUID = stepUUID
	}
	if err := s.validateDispatchStep(ctx, task.UUID, task.CurrentStepUUID); err != nil {
		return "", err
	}
	var parentUUID, parentStatus string
	err := s.db.QueryRowContext(ctx, `SELECT uuid, status FROM gt_cli_sessions WHERE task_uuid=? AND step_uuid=?
		ORDER BY created_at DESC, rowid DESC LIMIT 1`, task.UUID, task.CurrentStepUUID).Scan(&parentUUID, &parentStatus)
	if errors.Is(err, sql.ErrNoRows) {
		return orch.RunStep(ctx, workflow.RunStepOptions{TaskUUID: task.UUID, StepUUID: task.CurrentStepUUID,
			UserPrompt: content, DisplayPrompt: content, RecordType: "user_question", RequestID: messageID})
	}
	if err != nil {
		return "", err
	}
	if !workflow.IsTerminalStatus(parentStatus) {
		return "", errors.New("任务仍在执行")
	}
	return orch.ContinueConversation(ctx, workflow.ContinueConversationOptions{
		ParentSessionUUID: parentUUID, Prompt: content, DisplayPrompt: content, RequestID: messageID})
}

func (s *RemoteCommandService) validateDispatchWorkspace(ctx context.Context, taskUUID string) error {
	var workDir string
	if err := s.db.QueryRowContext(ctx, `SELECT work_dir FROM gt_tasks WHERE uuid=?`, taskUUID).Scan(&workDir); err != nil {
		return err
	}
	if strings.TrimSpace(workDir) == "" {
		return errors.New("任务缺少工作区")
	}
	return nil
}

func (s *RemoteCommandService) validateDispatchStep(ctx context.Context, taskUUID, stepUUID string) error {
	if stepUUID == "" {
		return errors.New("任务缺少当前步骤")
	}
	return nil
}

func (s *RemoteCommandService) preparePipelineMessage(ctx context.Context, task wechatTask) (string, error) {
	// 与 wechatRemote 原行为等价；为不破坏既有逻辑复用 orchestrator 路径。
	_ = task
	return "", errors.New("流水线初始步骤为空，请先在客户端启动流水线")
}

func (s *RemoteCommandService) dispatchErrorReply(ctx context.Context, channelType string, task wechatTask, err error) string {
	if errors.Is(err, errCodexBusy) {
		return s.tr(ctx, channelType, "remote_channel_busy", i18n.Params{"Task": task.Title})
	}
	key := "remote_channel_dispatch_failed"
	var failure *wechatDispatchFailure
	if errors.As(err, &failure) {
		key = failure.key
	}
	// 非结构化错误（dispatchTask 内部的 fmt.Errorf 链路，如「只能继续已开始或已完成的 Agent 编排」、
	// 「任务已有活动 Session」、「历史会话不存在」等）原本被吞到 remote_channel_dispatch_failed
	// 的固定文案里，定位时根本看不到原因。临时把 err.Error() 一起透传给用户，便于排障。
	if key == "remote_channel_dispatch_failed" {
		applog.Warn("远程指令下发失败", "task_uuid", task.UUID, "channel_type", channelType, "error", err.Error())
		return s.tr(ctx, channelType, "remote_channel_dispatch_failed", i18n.Params{"Detail": err.Error()})
	}
	return s.tr(ctx, channelType, key, nil)
}

// ============================================================================
// CLI 兜底 + skill prompt
// ============================================================================

func (s *RemoteCommandService) dispatchToCLI(ctx context.Context, channelType, messageID, message, taskUUID, cliType, cliModel string) (string, string, error) {
	orch := s.orchestratorGetter()
	if orch == nil {
		return "", "", errors.New("任务执行服务不可用")
	}
	execPath, execErr := executor.ResolveCLIExecutable(cliType)
	if execErr != nil || execPath == "" {
		return "", "", fmt.Errorf("未找到 %s CLI，请先在客户端配置正确的 CLI 类型", cliType)
	}
	if _, ok := workflow.NewAdapter(cliType); !ok {
		return "", "", fmt.Errorf("不支持的CLI类型: %s", cliType)
	}
	// 工作区按通道命名（微信保持 wechat_workspace 不变）
	cliConfig := s.readChannelCLIConfig(ctx, channelType)
	workDir := strings.TrimSpace(cliConfig.Workspace)
	if workDir == "" {
		home, _ := os.UserHomeDir()
		workspaceName := channelType + "_workspace"
		if channelType == ChannelTypeWechat {
			workspaceName = "wechat_workspace"
		}
		workDir = filepath.Join(home, "goteams", workspaceName)
	}
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		return "", "", fmt.Errorf("创建兜底工作区失败: %w", err)
	}
	var taskTitle, taskStatus string
	if taskUUID != "" {
		_ = s.db.QueryRowContext(ctx, `SELECT title, status FROM gt_tasks WHERE uuid=?`, taskUUID).Scan(&taskTitle, &taskStatus)
	}
	title := strings.TrimSpace(message)
	if runes := []rune(title); len(runes) > 30 {
		title = string(runes[:30]) + "…"
	}
	if title == "" {
		titleFallback := s.tr(ctx, channelType, "remote_channel_fallback_title", nil)
		if titleFallback == "" {
			titleFallback = "远程指令"
		}
		title = titleFallback
	}
	taskService := taskruntime.NewService(s.db, s.taskRoot)
	newTaskUUID, err := taskService.Create(ctx, taskruntime.CreateInput{
		SourceType:     "local",
		Title:          title,
		Content:        message,
		WorkDirs:       []string{workDir},
		Status:         "active",
		ExecutionMode:  taskruntime.ExecutionModeCLI,
		ExecutionTool:  cliType,
		ExecutionModel: cliModel,
	})
	if err != nil {
		return "", "", fmt.Errorf("创建兜底任务失败: %w", err)
	}
	var stepUUID string
	if err = s.db.QueryRowContext(ctx, `SELECT current_step_uuid FROM gt_tasks WHERE uuid=?`, newTaskUUID).Scan(&stepUUID); err != nil {
		return "", "", fmt.Errorf("读取任务步骤失败: %w", err)
	}
	if stepUUID == "" {
		return "", "", errors.New("新建任务缺少初始步骤")
	}
	userPrompt := message
	displayPrompt := message
	if taskUUID != "" {
		userPrompt = s.buildCLISkillPrompt(ctx, channelType, cliType, taskUUID, taskTitle, taskStatus, message)
	}
	sessionUUID, err := orch.RunStep(ctx, workflow.RunStepOptions{
		TaskUUID:      newTaskUUID,
		StepUUID:      stepUUID,
		UserPrompt:    userPrompt,
		DisplayPrompt: displayPrompt,
		RecordType:    "user_question",
		RequestID:     messageID,
		CLIType:       cliType,
		Model:         cliModel,
		WorkDir:       workDir,
	})
	if err != nil {
		return "", "", fmt.Errorf("启动兜底任务失败: %w", err)
	}
	return newTaskUUID, sessionUUID, nil
}

func (s *RemoteCommandService) buildCLISkillPrompt(ctx context.Context, channelType, cliType, taskUUID, taskTitle, taskStatus, message string) string {
	locale := GetChannelLocale(ctx, s.db, channelType)
	linkPath := s.imBotCLISkillPath(cliType)
	params := i18n.Params{
		"ChannelType": channelType,
		"TaskUUID":    taskUUID,
		"TaskTitle":   taskTitle,
		"TaskStatus":  taskStatus,
		"Message":     message,
	}
	if linkPath == "" {
		params["SkillPath"] = builtinskills.ImBotCLISkillName
		return i18n.FormatWithLocale(locale, "localserver_cli_skill_prompt", params)
	}
	params["SkillPath"] = encodeMarkdownLocalPath(linkPath)
	return i18n.FormatWithLocale(locale, "localserver_cli_skill_prompt", params)
}

func (s *RemoteCommandService) imBotCLISkillPath(cliType string) string {
	// 计划 §十六 B4：Vibe ImBot Skill 状态由 wechatRemote 自身持有；core 不依赖该结构，
	// 仅返回空字符串让 prompt 走「按名称引导」分支。微信专属行为在 wechatRemote.buildCLISkillPath 保留。
	_ = cliType
	return ""
}

// channelCtx 把 channel 的 locale 注入 ctx，便于 SkillSurface.trFromCtx 使用。
func (s *RemoteCommandService) channelCtx(ctx context.Context, channelType string) context.Context {
	locale := GetChannelLocale(ctx, s.db, channelType)
	if locale == "" {
		locale = "zh-CN"
	}
	return i18n.ContextWithLocale(ctx, locale)
}

// ============================================================================
// 自然语言意图：参数化 channelType 的匹配函数（保留原算法）
// ============================================================================

type remoteNLIntent string

const (
	remoteNLIntentListTasks        remoteNLIntent = "list_tasks"
	remoteNLIntentListActive       remoteNLIntent = "list_active_tasks"
	remoteNLIntentListDone         remoteNLIntent = "list_done_tasks"
	remoteNLIntentListBlocked      remoteNLIntent = "list_blocked"
	remoteNLIntentTaskDetail       remoteNLIntent = "task_detail"
	remoteNLIntentHelp             remoteNLIntent = "help"
	remoteNLIntentCurrentTask      remoteNLIntent = "current_task"
	remoteNLIntentSwitchTask       remoteNLIntent = "switch_task"
	remoteNLIntentPauseTask        remoteNLIntent = "pause_task"
	remoteNLIntentResumeTask       remoteNLIntent = "resume_task"
	remoteNLIntentStopTask         remoteNLIntent = "stop_task"
	remoteNLIntentCancelTask       remoteNLIntent = "cancel_task"
	remoteNLIntentExitTask         remoteNLIntent = "exit_task"
	remoteNLIntentCompleteTask     remoteNLIntent = "complete_task"
	remoteNLIntentTaskSummary      remoteNLIntent = "task_summary"
	remoteNLIntentListTeamWork     remoteNLIntent = "list_team_work"
	remoteNLIntentListProjects     remoteNLIntent = "list_projects"
	remoteNLIntentListPipelines    remoteNLIntent = "list_pipelines"
	remoteNLIntentListExpertGroups remoteNLIntent = "list_expert_groups"
	remoteNLIntentListCLIs         remoteNLIntent = "list_clis"
	remoteNLIntentListVibeTools    remoteNLIntent = "list_vibe_tools"
)

// containsForceKeyword/withinIntentLengthLimit/extractTaskID/matchTaskIDAfterKeyword/matchBareTaskID/normalizeCommandAlias 在 remote_nlp_helpers.go 中定义。

func (s *RemoteCommandService) matchNaturalLanguageIntent(content string, boundTaskUUID string) (matched bool, intent remoteNLIntent, param string) {
	content = strings.TrimSpace(content)
	if content == "" {
		return false, "", ""
	}
	hasID := remoteExtractTaskID(content) != ""
	if !remoteWithinIntentLengthLimit(content, hasID) {
		return false, "", ""
	}
	lower := strings.ToLower(content)
	if rIntent, rParam, ok := s.matchRound1Strict(lower, content); ok {
		if s.validateMatch(rIntent, rParam) {
			return true, rIntent, rParam
		}
	}
	if boundTaskUUID != "" {
		return false, "", ""
	}
	if rIntent, rParam, ok := s.matchRound2Keywords(lower, content); ok {
		if s.validateMatch(rIntent, rParam) {
			return true, rIntent, rParam
		}
	}
	return false, "", ""
}

func (s *RemoteCommandService) validateMatch(intent remoteNLIntent, param string) bool {
	switch intent {
	case remoteNLIntentTaskDetail, remoteNLIntentSwitchTask, remoteNLIntentPauseTask,
		remoteNLIntentResumeTask, remoteNLIntentStopTask, remoteNLIntentCompleteTask, remoteNLIntentCancelTask:
		if param == "" {
			return true
		}
		n, err := strconv.ParseInt(strings.TrimSuffix(strings.TrimSpace(param), "号"), 10, 64)
		return err == nil && n > 0
	}
	return true
}

// matchRound1Strict 第一轮：整串精确匹配（大小写不敏感，pre-lower 由调用方完成）。
// 分支按匹配优先级 P2 排列：单项操作 → 单项查看 → 列表 → 帮助（问候语放最后）→ 未匹配（返回 false）。
func (s *RemoteCommandService) matchRound1Strict(lower, raw string) (intent remoteNLIntent, param string, ok bool) {
	switch lower {
	case "帮助", "help", "/help", "怎么用", "使用说明", "可以做什么", "指令说明", "功能说明",
		"usage", "instructions", "how to use", "how to", "command list", "commands", "what can you do", "what can i do":
		return remoteNLIntentHelp, "", true
	case "退出", "退出任务", "退出当前任务", "离开任务", "离开当前任务", "离开", "我要离开", "切出去", "结束对话", "结束当前对话", "不再关注", "取消绑定", "解绑", "清除绑定", "不绑定了",
		"exit", "exit task", "exit current task", "leave task", "leave current task", "leave", "quit", "quit task", "end chat", "end conversation", "end session", "close chat", "stop tracking", "unbind", "disconnect", "detach", "remove binding", "done with this", "all done here":
		return remoteNLIntentExitTask, "", true
	case "取消", "取消任务", "取消当前任务", "取消执行", "取消执行任务", "取消这个任务", "取消那个任务",
		"cancel", "cancel task", "cancel this task", "cancel current task", "cancel that task", "cancel execution", "abort", "abort task", "abort execution", "stop execution":
		return remoteNLIntentCancelTask, "", true
	case "当前任务", "当前绑定", "绑定哪个", "绑定状态", "绑定到哪个", "正在做啥", "现在做啥",
		"current task", "current binding", "bound task", "what is current", "which task", "what task", "what is bound", "what's bound", "which one", "what is selected", "currently":
		return remoteNLIntentCurrentTask, "", true
	case "最新结果", "任务结果", "当前结果", "任务最新", "任务摘要", "任务输出",
		"latest result", "last result", "task result", "current result", "task output", "task summary", "show result", "result please":
		return remoteNLIntentTaskSummary, "", true
	}
	for _, pattern := range []string{
		"我的工作", "团队工作", "看下我的工作", "看看我的工作", "查看我的工作", "我的工作列表", "我有什么工作",
		"my work", "team work", "my tasks cloud", "show my work",
	} {
		if lower == pattern {
			return remoteNLIntentListTeamWork, "", true
		}
	}
	for _, pattern := range []string{
		"本地项目", "项目列表", "列出项目", "查看项目", "所有项目",
		"local projects", "list projects", "show projects", "projects list",
	} {
		if lower == pattern {
			return remoteNLIntentListProjects, "", true
		}
	}
	for _, pattern := range []string{
		"流水线", "流水线列表", "列出流水线", "查看流水线", "所有流水线",
		"pipelines", "list pipelines", "show pipelines", "pipeline list",
	} {
		if lower == pattern {
			return remoteNLIntentListPipelines, "", true
		}
	}
	for _, pattern := range []string{
		"专家团", "专家团列表", "列出专家团", "查看专家团", "所有专家团",
		"expert groups", "list expert groups", "show expert groups", "expert group list",
	} {
		if lower == pattern {
			return remoteNLIntentListExpertGroups, "", true
		}
	}
	for _, pattern := range []string{
		"已安装 cli", "已安装的 cli", "cli 列表", "列出 cli", "查看 cli",
		"installed clis", "list clis", "show clis", "cli list",
	} {
		if lower == pattern {
			return remoteNLIntentListCLIs, "", true
		}
	}
	for _, pattern := range []string{
		"vibe 工具", "vibe coding 工具", "vibe 工具列表", "列出 vibe 工具", "vibe coding tools",
	} {
		if lower == pattern {
			return remoteNLIntentListVibeTools, "", true
		}
	}
	for _, pattern := range []string{"列出所有任务", "列出任务", "查看所有任务", "任务列表", "所有任务", "我的任务",
		"list tasks", "list task", "show tasks", "show all tasks", "tasks", "list all tasks", "view tasks", "view all tasks", "show task list", "all tasks", "my tasks"} {
		if lower == pattern {
			return remoteNLIntentListTasks, "", true
		}
	}
	if ok, idText := remoteMatchBareTaskID(raw); ok {
		return remoteNLIntentSwitchTask, idText, true
	}
	if ok, idText := remoteMatchTaskIDAfterKeyword(lower, []string{"进入任务", "切换到任务", "切换任务", "绑定任务", "绑定到任务", "选择任务", "到任务", "选任务", "进入",
		"enter task", "switch to task", "switch task", "bind task", "bind to task", "select task", "go to task", "choose task", "pick task", "open task", "enter", "switch"}); ok {
		if idText != "" {
			return remoteNLIntentSwitchTask, idText, true
		}
	}
	if ok, idText := remoteMatchTaskIDAfterKeyword(lower, []string{"暂停任务", "暂停",
		"pause task", "pause", "suspend task", "suspend", "hold task", "hold"}); ok {
		if idText != "" {
			return remoteNLIntentPauseTask, idText, true
		}
		return remoteNLIntentPauseTask, "", true
	}
	if ok, idText := remoteMatchTaskIDAfterKeyword(lower, []string{"恢复任务", "继续任务", "继续", "恢复",
		"resume task", "continue task", "resume", "continue"}); ok {
		if idText != "" {
			return remoteNLIntentResumeTask, idText, true
		}
		return remoteNLIntentResumeTask, "", true
	}
	if ok, idText := remoteMatchTaskIDAfterKeyword(lower, []string{"停止任务", "停止", "终止任务", "终止",
		"stop task", "stop", "abort task", "abort", "terminate task", "terminate", "halt task", "halt", "kill task", "kill"}); ok {
		if idText != "" {
			return remoteNLIntentStopTask, idText, true
		}
		return remoteNLIntentStopTask, "", true
	}
	if ok, idText := remoteMatchTaskIDAfterKeyword(lower, []string{"完成任务", "标记完成", "done", "finish",
		"complete task", "mark complete", "mark done", "finish task"}); ok {
		if idText != "" {
			return remoteNLIntentCompleteTask, idText, true
		}
		return remoteNLIntentCompleteTask, "", true
	}
	// 帮助（问候语）是次低一级，仅高于「未匹配」，所以放在精确匹配的最后：
	// 上面的单项操作 / 单项查看 / 列表只要命中就优先。问候语的精确匹配规则见 remoteIsGreeting，
	// 宽松匹配（你好，在吗 / hello, are you there）在 matchRound2Keywords 末尾。
	if remoteIsGreeting(lower) {
		return remoteNLIntentHelp, "", true
	}
	return "", "", false
}

// matchRound2Keywords 简化版（保留核心意图的关键词匹配），仅在未绑定任务时调用。
// 分支按匹配优先级 P2 排列：单项操作（退出/解绑 → 完成/暂停/恢复/停止/切换(带引用) → 取消）
// → 单项查看（带任务引用的详情 → 当前绑定任务）→ 列表（进行中/阻塞/已完成/全部）→ 帮助（问候语）→ 未匹配。
func (s *RemoteCommandService) matchRound2Keywords(lower, raw string) (intent remoteNLIntent, param string, ok bool) {
	hasActive := strings.Contains(lower, "进行中") || strings.Contains(lower, "正在") || strings.Contains(lower, "活跃") || strings.Contains(lower, "active") || strings.Contains(lower, "running")
	hasDone := strings.Contains(lower, "已完成") || strings.Contains(lower, "done") || strings.Contains(lower, "finished") || strings.Contains(lower, "complete") || strings.Contains(lower, "completed")
	hasBlocked := strings.Contains(lower, "阻塞") || strings.Contains(lower, "blocked") || strings.Contains(lower, "paused") || strings.Contains(lower, "stuck")
	hasCurrent := strings.Contains(lower, "当前") || strings.Contains(lower, "正在做") || strings.Contains(lower, "现在") || strings.Contains(lower, "绑定哪个") || strings.Contains(lower, "current") || strings.Contains(lower, "bound") || strings.Contains(lower, "active one") || strings.Contains(lower, "selected")
	hasView := strings.Contains(lower, "查看") || strings.Contains(lower, "看看") || strings.Contains(lower, "看下") || strings.Contains(lower, "look") || strings.Contains(lower, "show") || strings.Contains(lower, "list") || strings.Contains(lower, "view") || strings.Contains(lower, "browse")
	hasEnter := strings.Contains(lower, "进入") || strings.Contains(lower, "切换") || strings.Contains(lower, "绑定") || strings.Contains(lower, "选择") || strings.Contains(lower, "enter") || strings.Contains(lower, "switch") || strings.Contains(lower, "bind") || strings.Contains(lower, "select") || strings.Contains(lower, "choose") || strings.Contains(lower, "open") || strings.Contains(lower, "pick") || strings.Contains(lower, "go to")
	hasComplete := strings.Contains(lower, "完成") || strings.Contains(lower, "complete") || strings.Contains(lower, "mark done") || strings.Contains(lower, "mark complete")
	hasPause := strings.Contains(lower, "暂停") || strings.Contains(lower, "挂起") || strings.Contains(lower, "pause") || strings.Contains(lower, "suspend") || strings.Contains(lower, "hold")
	hasResume := strings.Contains(lower, "恢复") || strings.Contains(lower, "继续") || strings.Contains(lower, "resume") || strings.Contains(lower, "continue")
	hasStop := strings.Contains(lower, "停止") || strings.Contains(lower, "终止") || strings.Contains(lower, "stop") || strings.Contains(lower, "terminate") || strings.Contains(lower, "abort") || strings.Contains(lower, "halt") || strings.Contains(lower, "kill")
	hasExit := strings.Contains(lower, "退出") || strings.Contains(lower, "离开") || strings.Contains(lower, "结束对话") || strings.Contains(lower, "切出去") || strings.Contains(lower, "结束会话") || strings.Contains(lower, "exit") || strings.Contains(lower, "leave") || strings.Contains(lower, "quit") || strings.Contains(lower, "end chat") || strings.Contains(lower, "end conversation") || strings.Contains(lower, "end session") || strings.Contains(lower, "close chat")
	hasUnbind := strings.Contains(lower, "取消绑定") || strings.Contains(lower, "解绑") || strings.Contains(lower, "解除") || strings.Contains(lower, "unbind") || strings.Contains(lower, "disconnect") || strings.Contains(lower, "detach") || strings.Contains(lower, "remove binding")
	hasCancel := strings.Contains(lower, "取消任务") || strings.Contains(lower, "取消执行") || strings.Contains(lower, "取消当前") || strings.Contains(lower, "取消这个") || strings.Contains(lower, "取消那个") || strings.Contains(lower, "取消一下") || lower == "取消" ||
		strings.Contains(lower, "cancel task") || strings.Contains(lower, "cancel this task") || strings.Contains(lower, "cancel current task") || strings.Contains(lower, "abort task") || strings.Contains(lower, "cancel execution") || strings.Contains(lower, "abort execution") || strings.Contains(lower, "stop execution")

	idText := remoteExtractTaskID(raw)
	if idText == "" {
		if matched, t := remoteMatchBareTaskID(raw); matched {
			idText = t
		}
	}

	if hasExit || hasUnbind {
		return remoteNLIntentExitTask, "", true
	}
	if hasComplete && idText != "" {
		return remoteNLIntentCompleteTask, idText, true
	}
	if hasPause && idText != "" {
		return remoteNLIntentPauseTask, idText, true
	}
	if hasResume && idText != "" {
		return remoteNLIntentResumeTask, idText, true
	}
	if hasStop && idText != "" {
		return remoteNLIntentStopTask, idText, true
	}
	if hasEnter && idText != "" {
		return remoteNLIntentSwitchTask, idText, true
	}
	if hasCancel {
		return remoteNLIntentCancelTask, idText, true
	}
	if hasView {
		// 单项查看 > 列表：带任务引用的详情最优先，其次当前绑定任务，最后才是各列表变体。
		if idText != "" {
			return remoteNLIntentTaskDetail, idText, true
		}
		if hasCurrent {
			return remoteNLIntentCurrentTask, "", true
		}
		if hasActive {
			return remoteNLIntentListActive, "", true
		}
		if hasBlocked {
			return remoteNLIntentListBlocked, "", true
		}
		if hasDone {
			return remoteNLIntentListDone, "", true
		}
		if remoteContainsAllKeyword(raw) {
			return remoteNLIntentListTasks, "", true
		}
	}
	// 问候语（你好 / 在吗 / hello / hi ...）按「帮助」处理，是匹配优先级的次低一级（仅高于「未匹配」），
	// 所以放在所有具体意图之后：保证「你好，取消」「你好，退出任务」这类消息仍由具体意图优先。
	// 宽松匹配规则见 remoteContainsGreeting。
	if remoteContainsGreeting(lower) {
		return remoteNLIntentHelp, "", true
	}
	return "", "", false
}

// 命名空间中的 6 个 NLP 辅助函数（withinIntentLengthLimit/extractTaskID/matchTaskIDAfterKeyword/matchBareTaskID）已在 remote_nlp_helpers.go 顶层定义；保留本地别名以兼容老调用点。

// ============================================================================
// 自然语言意图处理（参数化 channelType）
// ============================================================================

func (s *RemoteCommandService) handleNaturalLanguageIntent(ctx context.Context, channelType, messageID string, state ChannelState, intent remoteNLIntent, param string, force bool) (reply, taskUUID, sessionUUID string, err error) {
	switch intent {
	case remoteNLIntentHelp:
		return s.tr(ctx, channelType, "remote_channel_help_full", nil), "", "", nil
	case remoteNLIntentListTasks:
		return s.callSkillListTasksForChannel(ctx, channelType, ""), "", "", nil
	case remoteNLIntentListActive:
		return s.callSkillListTasksForChannel(ctx, channelType, "active"), "", "", nil
	case remoteNLIntentListDone:
		return s.callSkillListTasksForChannel(ctx, channelType, "done"), "", "", nil
	case remoteNLIntentListBlocked:
		return s.callSkillListTasksForChannel(ctx, channelType, "blocked"), "", "", nil
	case remoteNLIntentListTeamWork, remoteNLIntentListProjects, remoteNLIntentListPipelines,
		remoteNLIntentListExpertGroups, remoteNLIntentListCLIs, remoteNLIntentListVibeTools:
		return s.handleSkillLookupIntent(ctx, channelType, intent)
	case remoteNLIntentCurrentTask:
		if state.BoundTaskUUID == "" {
			return s.tr(ctx, channelType, "remote_channel_no_binding",
				i18n.Params{"Help": s.tr(ctx, channelType, "remote_channel_help", nil)}), "", "", nil
		}
		task, taskErr := s.taskByUUID(ctx, state.BoundTaskUUID)
		if errors.Is(taskErr, sql.ErrNoRows) {
			_ = s.clearBinding(ctx, channelType)
			return s.tr(ctx, channelType, "remote_channel_task_missing",
				i18n.Params{"Help": s.tr(ctx, channelType, "remote_channel_help", nil)}), "", "", nil
		}
		if taskErr != nil {
			err = taskErr
			return
		}
		last, _ := s.latestFinal(ctx, task)
		return s.tr(ctx, channelType, "remote_channel_switch",
			i18n.Params{"Task": task.Title, "Result": last}), task.UUID, "", nil
	case remoteNLIntentTaskSummary:
		if state.BoundTaskUUID == "" {
			return s.tr(ctx, channelType, "remote_channel_no_binding",
				i18n.Params{"Help": s.tr(ctx, channelType, "remote_channel_help", nil)}), "", "", nil
		}
		return s.handleTaskSummary(ctx, channelType, state.BoundTaskUUID)
	case remoteNLIntentSwitchTask:
		return s.switchTaskByShortID(ctx, channelType, param)
	case remoteNLIntentPauseTask:
		return s.changeTaskStatusByShortID(ctx, channelType, param, "blocked", s.tr(ctx, channelType, "remote_channel_paused", nil))
	case remoteNLIntentResumeTask:
		return s.changeTaskStatusByShortID(ctx, channelType, param, "active", s.tr(ctx, channelType, "remote_channel_resumed", nil))
	case remoteNLIntentStopTask:
		return s.changeTaskStatusByShortID(ctx, channelType, param, "blocked", s.tr(ctx, channelType, "remote_channel_stopped", nil))
	case remoteNLIntentCancelTask:
		return s.handleCancelTask(ctx, channelType, force)
	case remoteNLIntentExitTask:
		return s.handleExitTask(ctx, channelType)
	case remoteNLIntentCompleteTask:
		return s.changeTaskStatusByShortID(ctx, channelType, param, "done", s.tr(ctx, channelType, "remote_channel_completed", nil))
	case remoteNLIntentTaskDetail:
		return s.showTaskDetailByShortID(ctx, channelType, param)
	}
	return "", "", "", nil
}

func (s *RemoteCommandService) callSkillListTasksForChannel(ctx context.Context, channelType, status string) string {
	if s.skillSurface == nil {
		return s.tr(ctx, channelType, "remote_channel_tt_empty", nil)
	}
	execCtx := s.channelCtx(ctx, channelType)
	out, err := s.skillSurface.WechatListTasks(execCtx, status)
	if err != nil || out == "" {
		return s.tr(ctx, channelType, "remote_channel_tt_empty", nil)
	}
	return out
}

func (s *RemoteCommandService) handleSkillLookupIntent(ctx context.Context, channelType string, intent remoteNLIntent) (reply, taskUUID, sessionUUID string, err error) {
	if s.skillSurface == nil {
		return s.tr(ctx, channelType, "remote_channel_skill_call_failed",
			i18n.Params{"Detail": "skill surface not ready"}), "", "", nil
	}
	execCtx := s.channelCtx(ctx, channelType)
	var out string
	var ferr error
	switch intent {
	case remoteNLIntentListTeamWork:
		out, ferr = s.skillSurface.WechatFormatTeamWork(execCtx, false)
	case remoteNLIntentListProjects:
		out, ferr = s.skillSurface.WechatFormatProjects(execCtx)
	case remoteNLIntentListPipelines:
		out, ferr = s.skillSurface.WechatFormatPipelines(execCtx)
	case remoteNLIntentListExpertGroups:
		out, ferr = s.skillSurface.WechatFormatExpertGroups(execCtx)
	case remoteNLIntentListCLIs:
		out, ferr = s.skillSurface.WechatFormatCLIs(execCtx)
	case remoteNLIntentListVibeTools:
		out, ferr = s.skillSurface.WechatFormatVibeTools(execCtx)
	default:
		return "", "", "", nil
	}
	if ferr != nil {
		return s.tr(ctx, channelType, "remote_channel_skill_call_failed",
			i18n.Params{"Detail": ferr.Error()}), "", "", nil
	}
	return out, "", "", nil
}

func (s *RemoteCommandService) handleTaskSummary(ctx context.Context, channelType, taskUUID string) (reply, taskUUIDOut, sessionUUID string, err error) {
	task, taskErr := s.taskByUUID(ctx, taskUUID)
	if errors.Is(taskErr, sql.ErrNoRows) {
		return s.tr(ctx, channelType, "remote_channel_task_missing",
			i18n.Params{"Help": s.tr(ctx, channelType, "remote_channel_help", nil)}), "", "", nil
	}
	if taskErr != nil {
		err = taskErr
		return
	}
	last, _ := s.latestFinal(ctx, task)
	return s.tr(ctx, channelType, "remote_channel_switch",
		i18n.Params{"Task": task.Title, "Result": last}), task.UUID, "", nil
}

func (s *RemoteCommandService) switchTaskByShortID(ctx context.Context, channelType, param string) (reply, taskUUID, sessionUUID string, err error) {
	if param == "" {
		return s.tr(ctx, channelType, "remote_channel_id_invalid",
			i18n.Params{"Help": s.tr(ctx, channelType, "remote_channel_help", nil)}), "", "", nil
	}
	shortID, parseErr := strconv.ParseInt(param, 10, 64)
	if parseErr != nil || shortID < 1 {
		return s.tr(ctx, channelType, "remote_channel_id_invalid",
			i18n.Params{"Help": s.tr(ctx, channelType, "remote_channel_help", nil)}), "", "", nil
	}
	if err = s.ensureAliases(ctx); err != nil {
		return
	}
	task, taskErr := s.taskByShortIDFull(ctx, shortID)
	if errors.Is(taskErr, sql.ErrNoRows) {
		return s.tr(ctx, channelType, "remote_channel_task_missing",
			i18n.Params{"Help": s.tr(ctx, channelType, "remote_channel_help", nil)}), "", "", nil
	}
	if taskErr != nil {
		err = taskErr
		return
	}
	state, _ := s.store.ReadState(ctx, channelType)
	state.ChannelType = channelType
	state.BoundTaskUUID = task.UUID
	if err = s.store.WriteState(ctx, state); err != nil {
		return
	}
	return fmt.Sprintf("%s @%d: %s", s.tr(ctx, channelType, "remote_channel_switched", nil), task.ShortID, task.Title), task.UUID, "", nil
}

func (s *RemoteCommandService) showTaskDetailByShortID(ctx context.Context, channelType, param string) (reply, taskUUID, sessionUUID string, err error) {
	if param == "" {
		return s.tr(ctx, channelType, "remote_channel_id_invalid",
			i18n.Params{"Help": s.tr(ctx, channelType, "remote_channel_help", nil)}), "", "", nil
	}
	shortID, parseErr := strconv.ParseInt(param, 10, 64)
	if parseErr != nil || shortID < 1 {
		return s.tr(ctx, channelType, "remote_channel_id_invalid",
			i18n.Params{"Help": s.tr(ctx, channelType, "remote_channel_help", nil)}), "", "", nil
	}
	if err = s.ensureAliases(ctx); err != nil {
		return
	}
	task, taskErr := s.taskByShortIDFull(ctx, shortID)
	if errors.Is(taskErr, sql.ErrNoRows) {
		return s.tr(ctx, channelType, "remote_channel_task_missing",
			i18n.Params{"Help": s.tr(ctx, channelType, "remote_channel_help", nil)}), "", "", nil
	}
	if taskErr != nil {
		err = taskErr
		return
	}
	last, _ := s.latestFinal(ctx, task)
	return s.tr(ctx, channelType, "remote_channel_switch",
		i18n.Params{"Task": task.Title, "Result": last}), task.UUID, "", nil
}

func (s *RemoteCommandService) changeTaskStatusByShortID(ctx context.Context, channelType, param, targetStatus, successMessage string) (reply, taskUUID, sessionUUID string, err error) {
	if err = s.ensureAliases(ctx); err != nil {
		return
	}
	var task wechatTask
	if param == "" {
		state, readErr := s.store.ReadState(ctx, channelType)
		if readErr != nil {
			err = readErr
			return
		}
		if state.BoundTaskUUID == "" {
			return s.tr(ctx, channelType, "remote_channel_no_binding",
				i18n.Params{"Help": s.tr(ctx, channelType, "remote_channel_help", nil)}), "", "", nil
		}
		task, err = s.taskByUUID(ctx, state.BoundTaskUUID)
		if errors.Is(err, sql.ErrNoRows) {
			_ = s.clearBinding(ctx, channelType)
			return s.tr(ctx, channelType, "remote_channel_task_missing",
				i18n.Params{"Help": s.tr(ctx, channelType, "remote_channel_help", nil)}), "", "", nil
		}
		if err != nil {
			return
		}
	} else {
		shortID, parseErr := strconv.ParseInt(param, 10, 64)
		if parseErr != nil || shortID < 1 {
			return s.tr(ctx, channelType, "remote_channel_id_invalid",
				i18n.Params{"Help": s.tr(ctx, channelType, "remote_channel_help", nil)}), "", "", nil
		}
		task, err = s.taskByShortIDFull(ctx, shortID)
		if errors.Is(err, sql.ErrNoRows) {
			return s.tr(ctx, channelType, "remote_channel_task_missing",
				i18n.Params{"Help": s.tr(ctx, channelType, "remote_channel_help", nil)}), "", "", nil
		}
		if err != nil {
			return
		}
	}
	now := time.Now().UnixMilli()
	if task.Mode == taskruntime.ExecutionModeVibeCoding {
		return s.tr(ctx, channelType, "localserver_vibe_status_readonly", nil), task.UUID, "", nil
	}
	if _, execErr := s.db.ExecContext(ctx, `UPDATE gt_tasks SET status=?, updated_at=? WHERE uuid=?`, targetStatus, now, task.UUID); execErr != nil {
		err = execErr
		return
	}
	last, _ := s.latestFinal(ctx, task)
	if last != "" {
		return fmt.Sprintf("%s\n@%d %s\n%s", successMessage, task.ShortID, task.Title, last), task.UUID, "", nil
	}
	return fmt.Sprintf("%s @%d %s", successMessage, task.ShortID, task.Title), task.UUID, "", nil
}

// ============================================================================
// 二次确认：参数化 channelType 处理 pending_*_task_uuid
// ============================================================================

func (s *RemoteCommandService) handlePendingConfirmations(ctx context.Context, state *ChannelState, content string) (handled bool, reply, taskUUIDOut, sessionUUIDOut string, err error) {
	type pending struct {
		uuid  string
		match func(string) bool
		exec  func(ctx context.Context, pendingUUID string) (reply, taskUUID string, sessionUUID string, err error)
	}
	items := []pending{
		{state.PendingCancelTaskUUID, isCancelConfirmKeyword,
			func(ctx context.Context, pendingUUID string) (string, string, string, error) {
				return s.executeCancelTask(ctx, state.ChannelType, pendingUUID)
			}},
		{state.PendingResumeTaskUUID, isConfirmKeyword,
			func(ctx context.Context, pendingUUID string) (string, string, string, error) {
				return s.executeResumeTask(ctx, state.ChannelType, pendingUUID)
			}},
		{state.PendingDoneTaskUUID, isConfirmKeyword,
			func(ctx context.Context, pendingUUID string) (string, string, string, error) {
				return s.executeCompleteTask(ctx, state.ChannelType, pendingUUID)
			}},
	}
	anyPending := false
	for _, p := range items {
		if p.uuid == "" {
			continue
		}
		anyPending = true
		if p.match(content) {
			reply, taskUUIDOut, sessionUUIDOut, err = p.exec(ctx, p.uuid)
			return true, reply, taskUUIDOut, sessionUUIDOut, err
		}
	}
	if !anyPending {
		return false, "", "", "", nil
	}
	st, _ := s.store.ReadState(ctx, state.ChannelType)
	st.ChannelType = state.ChannelType
	st.PendingCancelTaskUUID = ""
	st.PendingResumeTaskUUID = ""
	st.PendingDoneTaskUUID = ""
	if err = s.store.WriteState(ctx, st); err != nil {
		return
	}
	return false, "", "", "", nil
}

func isCancelConfirmKeyword(content string) bool {
	switch strings.ToLower(strings.TrimSpace(content)) {
	case "yes", "y", "是", "对", "取消":
		return true
	}
	return false
}

func isConfirmKeyword(content string) bool {
	switch strings.ToLower(strings.TrimSpace(content)) {
	case "yes", "y", "是", "对":
		return true
	}
	return false
}

func (s *RemoteCommandService) handleCancelTask(ctx context.Context, channelType string, force bool) (reply, taskUUID, sessionUUID string, err error) {
	if err = s.ensureAliases(ctx); err != nil {
		return
	}
	state, err := s.store.ReadState(ctx, channelType)
	if err != nil {
		return
	}
	if state.BoundTaskUUID == "" {
		execCtx := s.channelCtx(ctx, channelType)
		listText, _ := s.skillSurface.WechatListTasks(execCtx, "active")
		return s.tr(ctx, channelType, "remote_channel_no_task_bound", i18n.Params{"Tasks": listText}), "", "", nil
	}
	task, taskErr := s.taskByUUID(ctx, state.BoundTaskUUID)
	title := task.Title
	err = taskErr
	if errors.Is(err, sql.ErrNoRows) {
		_ = s.clearBinding(ctx, channelType)
		return s.tr(ctx, channelType, "remote_channel_task_missing",
			i18n.Params{"Help": s.tr(ctx, channelType, "remote_channel_help", nil)}), "", "", nil
	}
	if err != nil {
		return
	}
	if force {
		return s.executeCancelTask(ctx, channelType, state.BoundTaskUUID)
	}
	state.PendingCancelTaskUUID = state.BoundTaskUUID
	if err = s.store.WriteState(ctx, state); err != nil {
		return
	}
	return s.tr(ctx, channelType, "remote_channel_cancel_confirm", i18n.Params{"Task": title}), state.BoundTaskUUID, "", nil
}

func (s *RemoteCommandService) executeCancelTask(ctx context.Context, channelType, taskUUID string) (reply, taskUUIDOut, sessionUUID string, err error) {
	if err = s.ensureAliases(ctx); err != nil {
		return
	}
	state, _ := s.store.ReadState(ctx, channelType)
	state.ChannelType = channelType
	state.BoundTaskUUID = ""
	state.PendingCancelTaskUUID = ""
	state.PendingResumeTaskUUID = ""
	state.PendingDoneTaskUUID = ""
	if err = s.store.WriteState(ctx, state); err != nil {
		return
	}
	execCtx := s.channelCtx(ctx, channelType)
	text, err := s.skillSurface.WechatCancelTask(execCtx, taskUUID)
	if err != nil {
		return "", "", "", err
	}
	return text, "", "", nil
}

func (s *RemoteCommandService) handleResumeTask(ctx context.Context, channelType string, force bool) (reply, taskUUID, sessionUUID string, err error) {
	state, err := s.store.ReadState(ctx, channelType)
	if err != nil {
		return
	}
	if state.BoundTaskUUID == "" {
		execCtx := s.channelCtx(ctx, channelType)
		listText, _ := s.skillSurface.WechatListTasks(execCtx, "active")
		return s.tr(ctx, channelType, "remote_channel_no_task_bound", i18n.Params{"Tasks": listText}), "", "", nil
	}
	task, taskErr := s.taskByUUID(ctx, state.BoundTaskUUID)
	title := task.Title
	err = taskErr
	if errors.Is(err, sql.ErrNoRows) {
		_ = s.clearBinding(ctx, channelType)
		return s.tr(ctx, channelType, "remote_channel_task_missing",
			i18n.Params{"Help": s.tr(ctx, channelType, "remote_channel_help", nil)}), "", "", nil
	}
	if err != nil {
		return
	}
	if force {
		return s.executeResumeTask(ctx, channelType, state.BoundTaskUUID)
	}
	state.PendingResumeTaskUUID = state.BoundTaskUUID
	if err = s.store.WriteState(ctx, state); err != nil {
		return
	}
	return s.tr(ctx, channelType, "remote_channel_resume_confirm", i18n.Params{"Task": title}), state.BoundTaskUUID, "", nil
}

func (s *RemoteCommandService) executeResumeTask(ctx context.Context, channelType, taskUUID string) (reply, taskUUIDOut, sessionUUID string, err error) {
	state, _ := s.store.ReadState(ctx, channelType)
	state.ChannelType = channelType
	state.PendingResumeTaskUUID = ""
	if err = s.store.WriteState(ctx, state); err != nil {
		return
	}
	execCtx := s.channelCtx(ctx, channelType)
	text, err := s.skillSurface.WechatResumeTask(execCtx, taskUUID)
	if err != nil {
		return "", "", "", err
	}
	return text, taskUUID, "", nil
}

func (s *RemoteCommandService) handleCompleteTask(ctx context.Context, channelType string, force bool) (reply, taskUUID, sessionUUID string, err error) {
	if err = s.ensureAliases(ctx); err != nil {
		return
	}
	state, err := s.store.ReadState(ctx, channelType)
	if err != nil {
		return
	}
	if state.BoundTaskUUID == "" {
		execCtx := s.channelCtx(ctx, channelType)
		listText, _ := s.skillSurface.WechatListTasks(execCtx, "active")
		return s.tr(ctx, channelType, "remote_channel_no_task_bound", i18n.Params{"Tasks": listText}), "", "", nil
	}
	task, taskErr := s.taskByUUID(ctx, state.BoundTaskUUID)
	title := task.Title
	err = taskErr
	if errors.Is(err, sql.ErrNoRows) {
		_ = s.clearBinding(ctx, channelType)
		return s.tr(ctx, channelType, "remote_channel_task_missing",
			i18n.Params{"Help": s.tr(ctx, channelType, "remote_channel_help", nil)}), "", "", nil
	}
	if err != nil {
		return
	}
	if force {
		return s.executeCompleteTask(ctx, channelType, state.BoundTaskUUID)
	}
	state.PendingDoneTaskUUID = state.BoundTaskUUID
	if err = s.store.WriteState(ctx, state); err != nil {
		return
	}
	return s.tr(ctx, channelType, "remote_channel_done_confirm", i18n.Params{"Task": title}), state.BoundTaskUUID, "", nil
}

func (s *RemoteCommandService) executeCompleteTask(ctx context.Context, channelType, taskUUID string) (reply, taskUUIDOut, sessionUUID string, err error) {
	if err = s.ensureAliases(ctx); err != nil {
		return
	}
	state, _ := s.store.ReadState(ctx, channelType)
	state.ChannelType = channelType
	state.BoundTaskUUID = ""
	state.PendingDoneTaskUUID = ""
	state.PendingCancelTaskUUID = ""
	if err = s.store.WriteState(ctx, state); err != nil {
		return
	}
	execCtx := s.channelCtx(ctx, channelType)
	text, err := s.skillSurface.WechatCompleteTask(execCtx, taskUUID)
	if err != nil {
		return "", "", "", err
	}
	return text, "", "", nil
}

func (s *RemoteCommandService) handleExitTask(ctx context.Context, channelType string) (reply, taskUUID, sessionUUID string, err error) {
	state, readErr := s.store.ReadState(ctx, channelType)
	if readErr != nil && !errors.Is(readErr, sql.ErrNoRows) {
		err = readErr
		return
	}
	var title string
	if state.BoundTaskUUID != "" {
		t, taskErr := s.taskByUUID(ctx, state.BoundTaskUUID)
		if taskErr == nil {
			title = t.Title
		}
	}
	if err = s.clearBinding(ctx, channelType); err != nil {
		return
	}
	execCtx := s.channelCtx(ctx, channelType)
	out, _ := s.skillSurface.WechatListTasks(execCtx, "active")
	replyText := s.tr(ctx, channelType, "remote_channel_exited", i18n.Params{"Task": title, "Tasks": out})
	return replyText, "", "", nil
}

// ============================================================================
// 翻译与 CLI 配置
// ============================================================================

// tr 走核心层的简化翻译入口（channelType 决定 locale）。
func (s *RemoteCommandService) tr(ctx context.Context, channelType, key string, params i18n.Params) string {
	locale := GetChannelLocale(ctx, s.db, channelType)
	if locale == "" {
		locale = "zh-CN"
	}
	return i18n.FormatWithLocale(locale, key, params)
}

type remoteCLIConfigResult struct {
	CLICType  string
	CLIModel  string
	Workspace string
	Enabled   bool
}

func (s *RemoteCommandService) readChannelCLIConfig(ctx context.Context, channelType string) remoteCLIConfigResult {
	var cfg remoteCLIConfigResult
	err := s.db.QueryRowContext(ctx, `
		SELECT cli_type, cli_model, workspace, enabled FROM gt_remote_channel_cli_configs WHERE channel_type=?`, channelType).Scan(
		&cfg.CLICType, &cfg.CLIModel, &cfg.Workspace, &cfg.Enabled)
	if err != nil {
		cfg.Enabled = true
	}
	if !cfg.Enabled && cfg.CLICType == "" {
		cfg.Enabled = false
	}
	return cfg
}

// normalizeCommandAlias 命令别名标准化（与原 wechatRemote 等价，详见 remote_nlp_helpers.go 中的 remoteNormalizeCommandAlias）。

// ============================================================================
// transport 编排入口（按通道调用）
// ============================================================================

// ProcessInbox 是 transport 工作循环的入站编排入口。
// transport 在每秒钟调一次：从 inbox 取出 pending 消息，调 Handle，根据结果写回。
// ProcessInbox 不持任何 per-message 状态，符合方案 §七 必炸点 2。
func (s *RemoteCommandService) ProcessInbox(ctx context.Context, channelType string) error {
	if strings.TrimSpace(channelType) == "" {
		return errors.New("ProcessInbox 需要指定 channelType")
	}
	messageID, content, err := ClaimInbox(ctx, s.db, channelType)
	if err != nil {
		return err
	}
	if messageID == "" {
		return nil
	}
	if len([]rune(content)) > 16000 {
		return s.finishProcessing(ctx, channelType, messageID, "done", "", "",
			s.tr(ctx, channelType, "remote_channel_msg_too_long", nil))
	}
	state, _ := s.store.ReadState(ctx, channelType)
	reply, taskUUID, sessionUUID, handleErr := s.Handle(ctx, RemoteInboundMessage{
		ChannelType: channelType,
		MessageID:   messageID,
		InboxKey:    messageID,
		SenderID:    state.SenderID,
		Content:     content,
	})
	status := "done"
	if errors.Is(handleErr, errCodexOutcomeUnknown) {
		status = "uncertain"
		reply = s.tr(ctx, channelType, "remote_channel_dispatch_uncertain", nil)
		handleErr = nil
	}
	if handleErr != nil {
		applog.Warn("远程指令处理失败", "channel_type", channelType, "message_id", messageID, "error", handleErr.Error())
		reply = s.tr(ctx, channelType, "remote_channel_processing_failed", i18n.Params{
			"Detail": handleErr.Error(),
			"Help":   s.tr(ctx, channelType, "remote_channel_help", nil),
		})
	}
	if sessionUUID != "" {
		status = "dispatched"
	}
	return s.finishProcessing(ctx, channelType, messageID, status, taskUUID, sessionUUID, reply)
}

// DeliverOutbox 调用 transport 提供的发送器投递一条 pending outbox 消息。
// transport 启动时把 deliver 函数挂到 RemoteCommandService 上（详见 NewRemoteCommandService 之后的 setter）。
// 投递成功调 MarkOutboxDelivered；失败调 MarkOutboxFailed。
func (s *RemoteCommandService) DeliverOutbox(ctx context.Context, channelType string, deliver OutboxDeliverFunc) error {
	if deliver == nil {
		return errors.New("DeliverOutbox 需要 deliver OutboxDeliverFunc")
	}
	outboxID, content, replyContext, attempts, err := ClaimOutbox(ctx, s.db, channelType, deliver.BlockedContextRevision(ctx))
	if errors.Is(err, sql.ErrNoRows) || outboxID == "" {
		return nil
	}
	if err != nil {
		return err
	}
	err = deliver.DoSend(ctx, content, replyContext)
	if err != nil {
		return MarkOutboxFailed(ctx, s.db, channelType, outboxID, attempts+1)
	}
	return MarkOutboxDelivered(ctx, s.db, channelType, outboxID)
}

// OutboxDeliverFunc 把一条 outbox 消息投递到通道，replyContext 是平台私有上下文（如 req_id/chatid/sessionWebhook）。
type OutboxDeliverFunc interface {
	// BlockedContextRevision 返回当前通道的 context_revision，用于跳过已被 context 切换抵消的消息。
	BlockedContextRevision(ctx context.Context) int64
	// DoSend 在调用方提供的平台通道上把 content 发送出去；replyContext 由 transport 解析。
	DoSend(ctx context.Context, content, replyContext string) error
}

// OutboxDeliverFuncAdapter 允许 transport 用普通方法实现 OutboxDeliverFunc，避免定义新接口时多写样板。
type OutboxDeliverFuncAdapter struct {
	Revision func(ctx context.Context) int64
	Send     func(ctx context.Context, content, replyContext string) error
}

func (a OutboxDeliverFuncAdapter) BlockedContextRevision(ctx context.Context) int64 {
	if a.Revision == nil {
		return 0
	}
	return a.Revision(ctx)
}

func (a OutboxDeliverFuncAdapter) DoSend(ctx context.Context, content, replyContext string) error {
	if a.Send == nil {
		return errors.New("OutboxDeliverFuncAdapter.Send 未实现")
	}
	return a.Send(ctx, content, replyContext)
}

// ============================================================================
// 计划 §五：本地快路径命令的参数解析与错误渲染。
// 这些辅助函数与原 wechatRemote 的同名函数等价；channelType 由调用方传入，
// 不再依赖 wechatRemote 单例状态。
// ============================================================================

// normalizeCommandAlias 把命令别名标准化为内部处理名称。
// 例如："/teamstask" → "/tt"、"/switch-model" → "/swm"、"/create-task" → "/ct"。
// 与历史 wechatRemote.normalizeCommandAlias 保持一致，避免新增别名时两侧漂移。
func (s *RemoteCommandService) normalizeCommandAlias(cmd string) string {
	switch cmd {
	case "/teamstask":
		return "/tt"
	case "/projects":
		return "/pro"
	case "/cli-discovery":
		return "/clis"
	case "/cli-models":
		return "/clim"
	case "/pipelines":
		return "/pls"
	case "/expert-groups":
		return "/egs"
	case "/vibecodinglist":
		return "/vcl"
	case "/codex-capability":
		return "/vcl"
	case "/switch-model":
		return "/swm"
	case "/switch-run":
		return "/swr"
	case "/switch-cli":
		return "/swcli"
	case "/task-info":
		return "/ti"
	case "/create-task":
		return "/ct"
	// /ctt 才是「按参考任务创建团队工作」的入口；微信侧一直缺这条快路径。
	case "/create-team-task":
		return "/ctt"
	}
	return cmd
}

// fastPathCommandArgFlags 记录每个快路径命令（使用 normalizeCommandAlias 之后的规范名）
// 允许的 `-key` 参数白名单，服务于「命令与参数之间漏写空格」的容错解析。
// nil / 空切片表示该命令不接受 `-key` 参数，此时不允许粘连写法被切分（如 /c-anything 不会被当成 /c）。
// 新增快路径命令或参数时需同步维护本表，否则粘连写法（/xxx-y）不会被还原；
// 新增参数名时还要避免与既有命令名的后缀撞车（例：给 /task 加 -info 后，/task-info 会被拆成 /task -info）。
var fastPathCommandArgFlags = map[string][]string{
	"/task":      {"a", "r", "f", "e"},
	"/t":         {"a", "r", "f", "e"},
	"/T":         {"a", "r", "f", "e"},
	"/exit":      nil,
	"/q":         nil,
	"/quit":      nil,
	"/cancel":    {"f"},
	"/c":         {"f"},
	"/resume":    {"f"},
	"/r":         {"f"},
	"/done":      {"f"},
	"/d":         {"f"},
	"/bind":      nil,
	"/b":         nil,
	"/unbind":    nil,
	"/ub":        nil,
	"/today":     {"a"},
	"/tt":        {"a"},
	"/pro":       nil,
	"/clis":      nil,
	"/clim":      {"cid"},
	"/pls":       nil,
	"/egs":       nil,
	"/vibe-list": nil,
	"/vcl":       nil,
	"/ti":        {"id"},
	"/ct":        {"t", "d", "w", "pid", "plid", "egid", "em", "et", "emdl"},
	"/ctt":       {"tid", "ttid", "t"},
	"/n":         {"id"},
	"/swm":       {"id", "cid", "mid", "emdl"},
	"/swr":       {"id", "t", "emdl", "plid", "egid", "vid"},
	"/swcli":     {"id", "cid", "mid", "emdl"},
}

// splitAttachedCommandArgs 容忍「命令与参数之间漏写空格」的写法，将其还原为命令 + 参数：
//
//	/task-a      → ("/task", "-a")
//	/ti-id=3     → ("/ti", "-id=3")
//	/swm-mid=2   → ("/swm", "-mid=2")
//	/task-info-id=3 → ("/task-info", "-id=3")
//
// 遵循优先级 P1「有参数且参数合法 > 无参数」：先尝试拆出合法参数，拆不出再按整串命令名处理。约束：
//  1. 从右往左找连字符，取最长前缀切分，保证 /task-info-id=3 切在 /task-info 之后而非 /task 之后；
//  2. 切出的参数 key 必须在该命令的参数白名单内——这同时保护了 /task-info、/create-task、/cli-models、
//     /vibe-list 这类自带连字符的命令名（"-info" 等都不在白名单里，永远不会被拆开）；
//  3. 切分不出合法结果时原样返回，content 不变，消息按原路径（自然语言 / CLI 兜底）处理，
//     即参数不合法时不降级成无参形式，直接算「未匹配」。
func (s *RemoteCommandService) splitAttachedCommandArgs(command, params string) (string, string) {
	if !strings.Contains(command, "-") {
		return command, params
	}
	for i := len(command) - 1; i > 1; i-- {
		if command[i] != '-' {
			continue
		}
		head, tail := command[:i], command[i:]
		flags, known := fastPathCommandArgFlags[s.normalizeCommandAlias(head)]
		if !known {
			continue
		}
		key, ok := attachedArgKey(tail)
		if !ok || !flagAllowed(flags, key) {
			continue
		}
		if params == "" {
			return head, tail
		}
		return head, tail + " " + params
	}
	return command, params
}

// attachedArgKey 校验 `-key` / `-key=value` 形式的粘连参数并取出 key。
// key 只允许字母、数字、下划线，避免把 `/n 1-2`、`/swcli-id=1-cid=2` 这类内容当成参数。
func attachedArgKey(tail string) (string, bool) {
	if len(tail) < 2 || tail[0] != '-' || tail[1] == '-' {
		return "", false
	}
	key, _, _ := strings.Cut(tail[1:], "=")
	if key == "" {
		return "", false
	}
	for _, r := range key {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_':
		default:
			return "", false
		}
	}
	return key, true
}

// flagAllowed 判断参数 key 是否在该命令的白名单内。
func flagAllowed(flags []string, key string) bool {
	for _, f := range flags {
		if f == key {
			return true
		}
	}
	return false
}

// remoteArgs 是 -key=value 形式的参数解析结果。
type remoteArgs map[string]string

// parseArgs 同时支持 -key=value 与 -flag 形式，并把非 flag 文本放在 rest。
// `-key value`（空格分隔）也识别为同一 key；非 - 开头的 token 视为裸 ID。
func (s *RemoteCommandService) parseArgs(input string) (remoteArgs, string) {
	args := remoteArgs{}
	restTokens := []string{}
	parts := strings.Fields(input)
	for i := 0; i < len(parts); i++ {
		tok := parts[i]
		if !strings.HasPrefix(tok, "-") {
			restTokens = append(restTokens, tok)
			continue
		}
		key := strings.TrimPrefix(tok, "-")
		if eq := strings.IndexByte(key, '='); eq >= 0 {
			args[key[:eq]] = key[eq+1:]
			continue
		}
		if i+1 < len(parts) && !strings.HasPrefix(parts[i+1], "-") {
			args[key] = parts[i+1]
			i++
			continue
		}
		args[key] = "true"
	}
	return args, strings.Join(restTokens, " ")
}

func (a remoteArgs) get(key string) string {
	if v, ok := a[key]; ok {
		return strings.TrimSpace(v)
	}
	return ""
}

func (a remoteArgs) intValue(key string) (int, bool) {
	v := a.get(key)
	if v == "" {
		return 0, false
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 1 {
		return 0, false
	}
	return n, true
}

// firstTaskRef 优先取 -id=，其次裸数字（兼容 `/n 1` / `/ti 1`）。
func (s *RemoteCommandService) firstTaskRef(args remoteArgs, rest string) string {
	if v := args.get("id"); v != "" {
		return v
	}
	if rest != "" {
		return strings.TrimSpace(rest)
	}
	return ""
}

// firstTeamWorkID 读取 `-ttid=<我的工作 ttid>`，未传 / 全空白时返回 ("", false)。
// 与显示端 WechatFormatTeamWork 严格同源：-ttid 取的就是 /tt 输出里 <...> 这一段。
func (s *RemoteCommandService) firstTeamWorkID(args remoteArgs, rest string) (string, bool) {
	_ = rest
	if v := strings.TrimSpace(args.get("ttid")); v != "" {
		return v, true
	}
	return "", false
}

// parseModelIndexArg 解析 -mid=<N>。
// ok=false 表示参数缺失；err 非 nil 表示参数存在但不合法（非正整数）。
func (s *RemoteCommandService) parseModelIndexArg(raw string) (int, bool, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, false, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 1 {
		return 0, false, newSkillBadRequest("localserver_skill_model_index_invalid")
	}
	return n, true, nil
}

// splitDirs 把 `-w=<dirs>` 拆成目录列表。支持 `,` 与 `;` 两种分隔；全空白返回 nil。
func (s *RemoteCommandService) splitDirs(raw string) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	for _, sep := range []string{",", ";"} {
		if strings.Contains(raw, sep) {
			parts := strings.Split(raw, sep)
			out := make([]string, 0, len(parts))
			for _, p := range parts {
				if p = strings.TrimSpace(p); p != "" {
					out = append(out, p)
				}
			}
			return out
		}
	}
	return []string{raw}
}

// mapSwitchMode 把 -t=<mode> 短名映射到 ExecutionMode 常量。
// 解析失败返回 ""，由调用方给出明确的「缺 mode」文案。
func (s *RemoteCommandService) mapSwitchMode(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "vcl", "vibe", "vibe_coding":
		return taskruntime.ExecutionModeVibeCoding
	case "cli":
		return taskruntime.ExecutionModeCLI
	case "pls", "pipeline":
		return taskruntime.ExecutionModePipeline
	case "egs", "expert_group":
		return taskruntime.ExecutionModeExpertGroup
	}
	return ""
}

// resolveModelFromArgs 把 -cid / -mid 序号解析为 cli_type / model。
// -mid 缺失时硬失败返回 errSkillBadRequest，避免 resolveModelIndex 按 0 抛「模型序号无效」这种与真实原因不符的裸错误。
func (s *RemoteCommandService) resolveModelFromArgs(ctx context.Context, channelType string, cliIndex int, modelIndex string) (string, string, error) {
	mid, ok, err := s.parseModelIndexArg(modelIndex)
	if err != nil {
		return "", "", err
	}
	if !ok {
		return "", "", newSkillBadRequest("remote_channel_skill_missing_model_index")
	}
	execCtx := s.channelCtx(ctx, channelType)
	return s.skillSurface.resolveModelIndex(execCtx, cliIndex, mid)
}

// wechatSkillFailure 渲染「SkillSurface 调用失败」时给用户的文案。
// SkillSurface 的错误返回是双通道：第 1 返回值已经是本地化文案，第 2 返回值是原始 error。
// 统一以「已算好的本地化文案」为准，绝不放弃它；仅在返回整段裸翻译键时回退到 trSkillErr。
func (s *RemoteCommandService) wechatSkillFailure(ctx context.Context, channelType, localized string, err error) string {
	if err == nil {
		return localized
	}
	if strings.TrimSpace(localized) != "" && !bareI18nKeyPattern.MatchString(strings.TrimSpace(localized)) {
		return localized
	}
	return s.trSkillErr(ctx, channelType, err)
}

// bareI18nKeyPattern 识别「整段文本就是一个裸翻译键」——正常用户文案不会长这样。
var bareI18nKeyPattern = regexp.MustCompile(`^(localserver|remote_wechat|remote_channel|common)_[a-z0-9_]+$`)

// localizedDetail 保证填入 {{.Detail}} 的内容不会是一个裸翻译键。
func (s *RemoteCommandService) localizedDetail(ctx context.Context, channelType, detail string) string {
	trimmed := strings.TrimSpace(detail)
	if bareI18nKeyPattern.MatchString(trimmed) {
		return s.tr(ctx, channelType, trimmed, nil)
	}
	return detail
}

// trSkillErr 把 SkillSurface 返回的错误渲染成给用户看的文案。
// errSkillBadRequest / coreStatusError 的 Error() 返回的是 i18n key 本身，
// 这里按 key 走一次本地化，避免把裸 key 吐给用户。
func (s *RemoteCommandService) trSkillErr(ctx context.Context, channelType string, err error) string {
	if err == nil {
		return ""
	}
	var badRequest *errSkillBadRequest
	if errors.As(err, &badRequest) {
		return s.tr(ctx, channelType, badRequest.key, nil)
	}
	var statusErr *coreStatusError
	if errors.As(err, &statusErr) {
		if statusErr.key != "" {
			return s.tr(ctx, channelType, statusErr.key, nil)
		}
		return s.tr(ctx, channelType, "localserver_internal_error", nil)
	}
	var invalidInput *errSkillInvalidInput
	if errors.As(err, &invalidInput) {
		if key, _ := i18n.Key(invalidInput.err); key != "" && key != "localserver_internal_error" {
			return s.tr(ctx, channelType, key, nil)
		}
		return s.tr(ctx, channelType, "localserver_internal_error", nil)
	}
	detail := strings.TrimSpace(err.Error())
	if bareI18nKeyPattern.MatchString(detail) {
		return s.tr(ctx, channelType, detail, nil)
	}
	return s.tr(ctx, channelType, "remote_channel_skill_call_failed", i18n.Params{"Detail": detail})
}
