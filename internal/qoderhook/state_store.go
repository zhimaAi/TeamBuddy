package qoderhook

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"

	"goteams-client/internal/taskskillclient"
)

// sessionStore 持有单次回调的文件锁；离开处理流程后必须释放。
type sessionStore struct {
	file    string
	state   qoderState
	release func()
}

func openSession(root, sessionID string, bindRequested bool) (*sessionStore, error) {
	file := filepath.Join(root, sessionID+".json")
	// 普通未绑定聊天不创建目录、不访问本机 API。
	data, err := os.ReadFile(file)
	if err != nil && !os.IsNotExist(err) {
		return nil, errors.New("Qoder session state unavailable")
	}
	if len(data) == 0 && !bindRequested {
		return nil, nil
	}
	if err := os.MkdirAll(root, 0700); err != nil {
		return nil, errors.New("Qoder hook storage unavailable")
	}
	release, err := lockQoderFile(file + ".lock")
	if err != nil {
		return nil, errors.New("Qoder session callback is busy; retry after the current callback")
	}
	state, err := loadQoderState(file)
	if err != nil {
		release()
		return nil, err
	}
	return &sessionStore{file: file, state: state, release: release}, nil
}

func loadQoderState(file string) (qoderState, error) {
	data, err := os.ReadFile(file)
	state := qoderState{StopTurn: -1}
	if err != nil && !os.IsNotExist(err) {
		return state, errors.New("Qoder session state unavailable")
	}
	if len(data) > qoderMaxStateBytes || (len(data) > 0 && json.Unmarshal(data, &state) != nil) {
		return state, errors.New("invalid Qoder session state")
	}
	if state.TaskUUID != "" {
		if id, err := taskskillclient.NormalizeTaskID(state.TaskUUID); err != nil || id != state.TaskUUID || state.ExecutionIndex < 0 {
			return state, errors.New("invalid Qoder task binding state")
		}
	}
	return state, nil
}

func saveQoderState(file string, state *qoderState) error {
	data, err := json.Marshal(state)
	if err != nil {
		return err
	}
	base := *state
	base.OverflowReply = nil
	baseData, baseErr := json.Marshal(&base)
	if baseErr != nil {
		return baseErr
	}
	// 旧版状态可能恰好占满 2 MiB，Stop 新增哈希及轮次仍需少量元数据空间。
	if len(baseData) > maxQueueBytes+metadataReserve || len(data) > qoderMaxStateBytes {
		return errors.New("Qoder callback storage limit reached")
	}
	temp, err := os.CreateTemp(filepath.Dir(file), ".qoder-state-*")
	if err != nil {
		return errors.New("Qoder callback state could not be saved")
	}
	defer os.Remove(temp.Name())
	if err = temp.Chmod(0600); err == nil {
		_, err = temp.Write(data)
	}
	if err == nil {
		err = temp.Sync()
	}
	closeErr := temp.Close()
	if err == nil {
		err = closeErr
	}
	if err == nil {
		err = os.Rename(temp.Name(), file)
	}
	if err != nil {
		return errors.New("Qoder callback state could not be saved")
	}
	return nil
}
