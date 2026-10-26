package cloudsync

import "sync/atomic"

// 快照投递成功后的回调。上报状态写在会话 / 沟通记录行上，投递完成后必须让任务页
// 重新拉一次进度，否则界面会一直停在「待上报」，直到下一次本地变更触发刷新。
// 装配阶段由 localserver 注入 WS 广播，cloudsync 不反向依赖上层包。
var taskDeliveryListener atomic.Value

// SetTaskDeliveryListener 注册投递成功回调；传 nil 表示不注册。
func SetTaskDeliveryListener(listener func(taskUUID string)) {
	if listener == nil {
		return
	}
	taskDeliveryListener.Store(listener)
}

func notifyTaskDelivered(taskUUID string) {
	if taskUUID == "" {
		return
	}
	if listener, ok := taskDeliveryListener.Load().(func(string)); ok && listener != nil {
		listener(taskUUID)
	}
}
