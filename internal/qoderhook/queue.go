package qoderhook

import (
	"encoding/json"
)

const (
	maxQueueBytes     = 2 << 20
	maxPending        = 32
	maxReportsPerHook = 8
	metadataReserve   = 1024
	// JSON 转义每个字符最多六字节，另留事件 ID、哈希及轮次空间。
	qoderReplyReserve  = 6*maxContentRunes + metadataReserve
	qoderMaxStateBytes = maxQueueBytes + qoderReplyReserve
)

type qoderActivity struct {
	ID      string `json:"id"`
	Type    string `json:"type"`
	Content string `json:"content"`
}
type qoderState struct {
	qoderBinding
	CWD           string          `json:"cwd"`
	Disabled      bool            `json:"disabled"`
	Turn          int             `json:"turn"`
	StopTurn      int             `json:"stop_turn"`
	StopHash      string          `json:"stop_hash,omitempty"`
	Pending       []qoderActivity `json:"pending"`
	OverflowReply *qoderActivity  `json:"overflow_reply,omitempty"`
}

// enqueue 只计算下一份状态；容量不足时保持原状态，补传由调用方安排。
func (state qoderState) enqueue(event qoderEvent, activity *qoderActivity, stopHash string) (qoderState, bool, error) {
	// 先计算落盘后的完整状态，包含 JSON 转义和 UTF-8 字节；腾出空间前不推进轮次。
	next := state
	if event.Name == "UserPromptSubmit" {
		next.Turn++
	}
	if activity != nil {
		next.Pending = append(append([]qoderActivity(nil), state.Pending...), *activity)
		if event.Name == "Stop" {
			next.StopTurn = next.Turn
			next.StopHash = stopHash
		}
	}
	data, err := json.Marshal(&next)
	if err != nil {
		return state, false, err
	}
	limit, reserve := maxPending, 0
	if event.Name == "UserPromptSubmit" {
		limit, reserve = maxPending-1, qoderReplyReserve
	}
	if state.OverflowReply == nil && len(next.Pending) <= limit && len(data)+reserve <= maxQueueBytes {
		return next, true, nil
	}
	if event.Name == "Stop" && activity != nil {
		// Stop 不能要求用户重发答复。旧版满队列使用独立槽先落盘，
		// 同轮离线修正保留最新最终文本，并使用新 ID 避免改写已发送事件。
		next = state
		next.OverflowReply = activity
		next.StopTurn, next.StopHash = next.Turn, stopHash
		return next, true, nil
	}
	if activity == nil && event.Name != "UserPromptSubmit" {
		return state, true, nil
	}
	return state, false, nil
}

func (state *qoderState) nextActivity() (qoderActivity, bool) {
	if len(state.Pending) > 0 {
		return state.Pending[0], true
	}
	if state.OverflowReply != nil {
		return *state.OverflowReply, true
	}
	return qoderActivity{}, false
}

// 只有服务端确认成功后才能移除队首，补传超时仍保留原事件 ID。
func (state *qoderState) acknowledgeActivity() {
	if len(state.Pending) == 0 {
		state.OverflowReply = nil
	} else {
		state.Pending = state.Pending[1:]
	}
	if state.OverflowReply != nil && len(state.Pending) < maxPending {
		next := *state
		next.Pending = append(append([]qoderActivity(nil), state.Pending...), *state.OverflowReply)
		next.OverflowReply = nil
		if data, err := json.Marshal(&next); err == nil && len(data) <= maxQueueBytes {
			*state = next
		}
	}
}
