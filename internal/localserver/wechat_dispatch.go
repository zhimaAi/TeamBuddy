package localserver

// 历史入口：旧的 wechatRemote 业务方法（handleMessage/processInbox/dispatchTask/...）已全部搬到
// internal/localserver/remote_command.go 下的 RemoteCommandService。该服务由 wechatRemote.workLoop
// 通过 attachCommandService 注入。wechatTask 类型定义在 remote_command.go（与 ChannelState 同性质）。
//
// 本文件保留仅为兼容代码演进历史，无任何业务逻辑定义。后续删除需先确认无任何外部 grep 引用。
