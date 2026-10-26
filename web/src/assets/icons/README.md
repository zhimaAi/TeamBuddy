# 图标资源索引

工作空间悬浮卡片使用 `workspace-hover-directory.svg`（Figma 4094:77242），保留原始 16×16px 尺寸，展示工作目录路径。

新增或导出图标前，先检查本索引和当前目录中的现有资源，图形与语义匹配时优先复用。

| 文件 | 图形描述 | 业务语义 | 推荐复用场景 | 当前使用位置 |
| --- | --- | --- | --- | --- |
| [`task-knowledge-reference.svg`](./task-knowledge-reference.svg) | 12px 深灰书本 | 已选知识库引用 | 输入框引用胶囊 | ChatComposer、NewConversation；Figma 4001:54654 |
| [`task-composer-knowledge.svg`](./task-composer-knowledge.svg) | 14px 灰色书本 | 引用知识库 | 对话输入框工具栏 | ChatComposer；Figma I3996:52609;3998:53307 |
| [`task-composer-attachment.svg`](./task-composer-attachment.svg) | 14px 灰色回形针 | 添加附件 | 对话输入框工具栏 | ChatComposer；Figma I3996:52609;3998:53441 |
| [`task-composer-git-branch.svg`](./task-composer-git-branch.svg) | 14px 灰色 Git 分支 | 选择 Git 分支 | 对话输入框工具栏 | GitBranchPicker；Figma I3996:52609;3999:53930 |
| [`task-composer-cli-trigger.svg`](./task-composer-cli-trigger.svg) | 14px 灰色终端 | CLI 默认入口 | 对话输入框工具栏 | ChatComposer；Figma I3996:52609;3999:53751 |
| [`task-composer-documents.svg`](./task-composer-documents.svg) | 14px 灰色文件夹内带代码符号 | 查看产出文档 | 对话输入框工具栏 | ChatComposer；Figma I3996:52609;2639:11834 |
| [`task-composer-pipeline.svg`](./task-composer-pipeline.svg) | 16px 连接方框 | 选择流水线执行步骤 | 对话输入框蓝底白色标识 | ChatComposer；Figma 4000:54475，以原始 SVG 作为 CSS Mask |
| [`task-header-original-requirement.svg`](./task-header-original-requirement.svg) | 16px 灰色圆角文档内带向左箭头 | 打开原始需求 | 任务顶部操作栏 | TaskWorkspaceHeader；Figma 3996:50299 |
| [`task-header-details.svg`](./task-header-details.svg) | 16px 灰色圆角文档内带三行文字 | 打开任务详情 | 任务顶部操作栏 | TaskWorkspaceHeader；Figma 3996:50305 |
| [`task-header-directory.svg`](./task-header-directory.svg) | 16px 灰色描边文件夹 | 打开工作目录 | 任务顶部操作栏 | TaskWorkspaceHeader；Figma 4000:54175 |
| [`task-header-more.svg`](./task-header-more.svg) | 16px 灰色横向三点 | 更多任务操作 | 任务顶部操作栏 | TaskWorkspaceHeader；Figma 4005:36159 |
| [`common-more-actions.svg`](./common-more-actions.svg) | 横向排列的三个实心圆点 | 更多操作、打开操作菜单 | 列表项、卡片等内容区域的更多操作按钮 | 任务会话列表、流水线列表 |
| [`agent-cli-reload.svg`](./agent-cli-reload.svg) | 圆形箭头（刷新） | 重新检测本机 CLI 运行状态 | Agent 编辑弹窗 CLI 行的重新检测按钮 | AgentEditorModal |
| [`task-documents-open-directory.svg`](./task-documents-open-directory.svg) | 文件夹内带向右箭头 | 在系统文件夹中打开任务目录 | 产出文档抽屉标题栏 | TaskDocumentsDrawer |
| [`task-documents-toggle-directory.svg`](./task-documents-toggle-directory.svg) | 面板矩形内带竖线和向左箭头 | 收起或展开产出文档目录 | 产出文档内容区工具栏左侧 | TaskDocumentsDrawer |
| [`common-sidebar-toggle.svg`](./common-sidebar-toggle.svg) | 20px 分栏面板矩形，左侧带竖向分割线 | 通用侧栏／目录展开与收起，展开状态旋转 180° | 应用侧栏、知识库目录和任务详情目录 | AppSidebar / MainLayout / KnowledgeBase / KnowledgeFolderDetail / WorkItemDetailModal / TaskDetailInfoModal |
| [`knowledge-trash-restore.svg`](./knowledge-trash-restore.svg) | 16px 逆时针环形恢复箭头 | 恢复回收站文档 | 知识库回收站文件操作 | KnowledgeBase |
| [`knowledge-folder-tree.svg`](./knowledge-folder-tree.svg) | 16px 双层描边文件夹 | 文件夹与聚合目录 | 知识库目录树和文件夹列表 | KnowledgeBase / KnowledgeFolderDetail |
| [`knowledge-folder-heading.svg`](./knowledge-folder-heading.svg) | 20px 双层描边文件夹 | 当前文件夹标题 | 知识库文件夹详情标题 | KnowledgeFolderDetail |
| [`knowledge-document.svg`](./knowledge-document.svg) | 16px 带三行文字的文档 | 普通知识库文档 | 知识库目录树和文件夹列表 | KnowledgeBase / KnowledgeFolderDetail |
| [`knowledge-document-trash.svg`](./knowledge-document-trash.svg) | 20px 灰色带三行文字的文档 | 已删除文档 | 知识库回收站列表 | KnowledgeBase |
| [`knowledge-add-document.svg`](./knowledge-add-document.svg) | 文档内加号 | 新增文档 | 知识库新增菜单 | KnowledgeBase / KnowledgeFolderDetail |
| [`knowledge-add-folder.svg`](./knowledge-add-folder.svg) | 文件夹内加号 | 新增文件夹 | 知识库新增菜单 | KnowledgeBase / KnowledgeFolderDetail |
| [`knowledge-root-menu.svg`](./knowledge-root-menu.svg) | 双层描边目录 | 设置知识库目录 | 知识库设置菜单 | KnowledgeBase |
| [`knowledge-root-current.svg`](./knowledge-root-current.svg) | 24px 灰黑色目录 | 标识当前知识库目录 | 知识库目录设置弹窗 | KnowledgeBase |
| [`knowledge-root-select.svg`](./knowledge-root-select.svg) | 16px 蓝色目录 | 选择新的本地目录 | 知识库目录设置弹窗 | KnowledgeBase |
| [`knowledge-trash-menu.svg`](./knowledge-trash-menu.svg) | 16px 垃圾桶 | 打开回收站 | 知识库设置菜单 | KnowledgeBase |
| [`knowledge-settings.svg`](./knowledge-settings.svg) | 16px 八向齿轮 | 打开知识库设置菜单 | 知识库标题栏 | KnowledgeBase |
| [`knowledge-add.svg`](./knowledge-add.svg) | 16px 蓝色加号 | 打开知识库新增菜单 | 知识库标题栏 | KnowledgeBase |
| [`knowledge-copy-document.svg`](./knowledge-copy-document.svg) | 两个重叠的圆角方框 | 复制知识库文档全文 | 知识库文档面包屑操作区 | KnowledgeBase |
| [`knowledge-quick-add.svg`](./knowledge-quick-add.svg) | 16px 灰色加号 | 快速新增内容 | 知识库文件夹详情 | KnowledgeFolderDetail |
| [`knowledge-more.svg`](./knowledge-more.svg) | 三个横向实心圆点 | 打开目录项更多操作 | 知识库目录树 | KnowledgeBase |
| [`knowledge-tree-toggle.svg`](./knowledge-tree-toggle.svg) | 16px 实心右向三角 | 展开或收起目录节点 | 知识库目录树 | KnowledgeBase |
| [`knowledge-rename.svg`](./knowledge-rename.svg) | 下划线铅笔 | 重命名文件夹或文档 | 知识库目录项菜单 | KnowledgeBase |
| [`knowledge-delete.svg`](./knowledge-delete.svg) | 16px 垃圾桶 | 删除文件夹或文档 | 知识库目录项菜单 | KnowledgeBase |
| [`knowledge-delete-permanent.svg`](./knowledge-delete-permanent.svg) | 16px 灰色垃圾桶 | 永久删除文档 | 知识库回收站列表 | KnowledgeBase |
| [`knowledge-refresh.svg`](./knowledge-refresh.svg) | 双向环形刷新箭头 | 刷新回收站 | 知识库回收站标题栏 | KnowledgeBase |
| [`knowledge-outline.svg`](./knowledge-outline.svg) | 三条分层横线 | 打开文档目录 | 知识库文档编辑区 | KnowledgeBase |
| [`knowledge-source-mode.svg`](./knowledge-source-mode.svg) | 16px 灰色尖括号与斜杠 | 切换 Markdown 源码模式 | 知识库文档详情模式按钮，直接加载 SVG，源码模式以按钮背景高亮 | KnowledgeBase |
| [`markdown-task-list.svg`](./markdown-task-list.svg) | 圆角方框内带对勾 | Markdown 任务列表 | 文档段落类型操作栏 | MarkdownEditor |
| [`markdown-highlight-block.svg`](./markdown-highlight-block.svg) | 高亮内容块及两行正文 | Markdown 高亮块 | 文档段落类型操作栏 | MarkdownEditor |
| [`markdown-quote.svg`](./markdown-quote.svg) | 成对实心引号 | Markdown 引用 | 文档段落类型操作栏与类型菜单 | MarkdownEditor |
| [`markdown-text-cursor.svg`](./markdown-text-cursor.svg) | 白色描边的深色 I 形指针 | Windows 文本编辑鼠标指针 | Windows 桌面端可编辑 Markdown 正文 | TiptapMarkdownEditor |
| [`task-composer-cli.svg`](./task-composer-cli.svg) | 终端窗口内带命令提示符 | 未匹配到具体厂家时的 CLI 回退图标，也用于打开 CLI / 模型选择器 | CLI 选择器没有对应品牌标识时，以及任务会话输入框左侧触发按钮 | useCliLogo、ChatComposer |
| [`cli-cursor.svg`](./cli-cursor.svg) | Cursor 立方体轮廓 | Cursor CLI | 输入框和 CLI 列表中的 Cursor | useCliLogo |
| [`cli-kimi.svg`](./cli-kimi.svg) | Kimi 标识轮廓 | Kimi Code | 输入框和 CLI 列表中的 Kimi | useCliLogo |
| [`cli-copilot.svg`](./cli-copilot.svg) | GitHub Copilot 头像轮廓 | GitHub Copilot CLI | 输入框和 CLI 列表中的 Copilot | useCliLogo |
| [`cli-opencode.svg`](./cli-opencode.svg) | OpenCode 方框标识 | OpenCode | 输入框和 CLI 列表中的 OpenCode | useCliLogo |
| [`cli-qwen.svg`](./cli-qwen.svg) | Qwen 花形标识 | Qwen Code | 输入框和 CLI 列表中的 Qwen | useCliLogo |
| [`cli-qoder.svg`](./cli-qoder.svg) | Qoder 标识轮廓 | Qoder CLI | 输入框和 CLI 列表中的 Qoder / Qoder CN | useCliLogo |
| [`cli-codebuddy.svg`](./cli-codebuddy.svg) | CodeBuddy 标识轮廓 | CodeBuddy Code | 输入框和 CLI 列表中的 CodeBuddy | useCliLogo |
| [`cli-picker-check.svg`](./cli-picker-check.svg) | 对勾 | 当前已选中的模型 | CLI / 模型双栏选择弹层的选中态 | CliRuntimePicker |
| [`batch-config.svg`](./batch-config.svg) | 三条横线旁带圆点（灰色） | 进入批量配置 CLI | Agent 编排页头批量配置按钮 | PipelineStepsPane / ExpertGroupsWorkspace |
| [`batch-cancel.svg`](./batch-cancel.svg) | 三条横线旁带圆点 | 退出批量配置 CLI | Agent 编排页头取消批量配置按钮 | PipelineStepsPane / ExpertGroupsWorkspace |
| [`batch-selected-count.svg`](./batch-selected-count.svg) | 圆圈内对勾 | 已选 Agent 数量 | 批量配置底部操作条计数 | AgentBatchConfigBar |
| [`codex-logo.svg`](./codex-logo.svg) | Codex 品牌标识（紫蓝渐变） | 标识 Codex 应用 | Codex 应用入口、Codex 任务提示 | CreateLocalTaskModal、AssignExecutionModeModal、TaskDetail、VibeCodingConversationNotice |
| [`vibe-codex-cli.svg`](./vibe-codex-cli.svg) | Codex 官方单色标识（近黑 #0D0D0D） | 标识 Codex CLI，与 Codex 应用的彩色标识区分 | Vibe Coding 的 Codex CLI 选项和对应任务头像 | useVibeCoding |
| [`vibe-antigravity.svg`](./vibe-antigravity.svg) | 官方彩色拱形标识 | 标识 Antigravity CLI | Vibe Coding 和 CLI 列表中的 Antigravity | useVibeCoding、useCliLogo |
| [`vibe-coding-logo.svg`](./vibe-coding-logo.svg) | 星光加圆角方框（深灰 #595959） | Vibe Coding 执行方式标识 | Vibe Coding 执行方式入口、浅色背景上的图标 | CreateLocalTaskModal、AssignExecutionModeModal |
| [`vibe-coding-logo-blue.svg`](./vibe-coding-logo-blue.svg) | 星光加圆角方框（蓝 #318CE2） | Vibe Coding 执行方式标识（蓝色变体） | 蓝底容器中的 Vibe Coding 标识 | VibeCodingConversationNotice |
| [`team-login-feature-sync.svg`](./team-login-feature-sync.svg) | 方框对角带外扩箭头（蓝 #5B8DEF） | 团队连接能力：任务同步 | 团队工作登录引导卡片的能力标签 | TeamWorkLoginCard |
| [`team-login-feature-collaborate.svg`](./team-login-feature-collaborate.svg) | 两个并排人像（蓝 #5B8DEF） | 团队连接能力：团队协作 | 团队工作登录引导卡片的能力标签 | TeamWorkLoginCard |
| [`team-login-feature-agent.svg`](./team-login-feature-agent.svg) | 带天线的机器人头部（蓝 #5B8DEF） | 团队连接能力：Agent 协同 | 团队工作登录引导卡片的能力标签 | TeamWorkLoginCard |
| [`onboarding-spark.svg`](./onboarding-spark.svg) | 四角星光（#262626） | 新手指引开场标题标记 | 新增对话新手指引的开场卡片 | OnboardingGuide |
| [`sidebar-nav-light-new-conversation.svg`](./sidebar-nav-light-new-conversation.svg) | 方框加笔（#262626） | 侧栏「新对话」 | 浅色侧栏主导航 | AppSidebar |
| [`sidebar-nav-light-dashboard.svg`](./sidebar-nav-light-dashboard.svg) | 双列看板（#262626） | 侧栏「看板」 | 侧栏主导航 | SidebarMenuIcon |
| [`sidebar-nav-light-agent.svg`](./sidebar-nav-light-agent.svg) | 带天线的机器人（#262626） | 侧栏「Agent」 | 侧栏主导航 | SidebarMenuIcon |
| [`sidebar-nav-light-knowledge.svg`](./sidebar-nav-light-knowledge.svg) | 书本轮廓（#262626） | 侧栏「知识库」 | 侧栏主导航 | SidebarMenuIcon |
| [`sidebar-nav-remote.svg`](./sidebar-nav-remote.svg) | 圆角屏幕左下角带信号弧线（#262626） | 侧栏「远程通道」 | 侧栏主导航 | SidebarMenuIcon |
| [`sidebar-nav-more.svg`](./sidebar-nav-more.svg) | 20px 图标框内三个圆点（#262626） | 侧栏「更多」 | 侧栏主导航 | SidebarMenuIcon |
| [`sidebar-conversation-add.svg`](./sidebar-conversation-add.svg) | 对话框内加号（#595959） | 在工作目录中新增对话 | 对话分组操作按钮 | AppSidebar |
| [`sidebar-conversation-more.svg`](./sidebar-conversation-more.svg) | 16px 图标框内三个圆点（#595959） | 对话与目录的更多操作 | 对话列表与工作目录操作按钮 | AppSidebar / WorkspaceActions |
| [`sidebar-conversation-chevron.svg`](./sidebar-conversation-chevron.svg) | 14px 向右箭头（#90939A），展开时旋转向下 | 展开或收起归档区域 | 归档区域折叠控件 | AppSidebar |

侧栏新图标使用 Figma 原始 SVG 的固有尺寸居中呈现；深色侧栏以同一资源作为 CSS Mask，使用 #94A3B8，保证两种主题的图形一致。

## 侧边栏导航与会话目录（Figma 4094:77225）

导航图标使用 20px 容器，保留设计导出 SVG 的原始尺寸和 1.2px 描边，不缩放路径。浅色资源在深色侧栏通过现有 CSS Mask 着色。

| 文件 | 用途 | 设计节点 |
| --- | --- | --- |
| `sidebar-nav-light-new-conversation.svg` | 新对话 | I4094:77225;14998:39255;14944:36900 |
| `sidebar-nav-light-dashboard.svg` | 看板 | I4094:77225;14998:39256;14944:36871 |
| `sidebar-nav-light-agent.svg` | Agent 员工 | I4094:77225;14998:39257;14944:36871 |
| `sidebar-nav-light-knowledge.svg` | 知识库 | I4094:77225;14998:39260;14944:36871 |
| `sidebar-nav-remote.svg` | 远程通道 | I4094:77225;14998:39261;14944:36871 |
| `sidebar-nav-more.svg` | 更多 | I4094:77225;14998:39262;14944:36896 |
| `sidebar-search.svg` | 顶部搜索 | I4094:77225;15052:37739 |
| `sidebar-workspace-add.svg` | 添加工作空间 | I4094:77225;15052:37719 |
| `sidebar-footer-settings.svg` | 底部设置 | I4094:77225;14998:41286 |
| `sidebar-conversation-folder-open.svg` | 目录展开，普通与归档列表共用 | I4094:77225;14998:41759;14944:36896 |
| `sidebar-conversation-folder-closed.svg` | 目录收起，普通与归档列表共用 | I4094:77225;14998:44374;14944:36871 |

目录图标由 `isGroupCollapsed` 选择，归档状态不决定文件夹形态。

## 会话操作菜单（Figma 4094:77260）

以下图标用于 16px 容器，保留原始 SVG 尺寸；普通与已归档会话共用 `ConversationActionsMenu`。

| 文件 | 用途 | 设计节点 |
| --- | --- | --- |
| `conversation-action-read.svg` | 切换已读／未读 | 4094:77262 |
| `conversation-action-archive.svg` | 归档／取消归档 | 4094:77889 |
| `conversation-action-rename.svg` | 重命名 | 4094:77946 |
| `conversation-action-delete.svg` | 删除，保留确认流程 | 4094:77954 |

## 对话搜索弹窗（Figma 4095:78037）

| 文件 | 用途 | 设计节点 |
| --- | --- | --- |
| `conversation-search-input.svg` | 搜索输入框前缀，原始 16×16px | 4095:78040 |
| `conversation-search-close.svg` | 关闭弹窗，原始 16×16px，置于 32px 按钮内 | 4095:78046 |

## 会话悬浮信息（Figma 4094:77248／77252／77256）

| 文件 | 用途 | 设计节点 |
| --- | --- | --- |
| `conversation-status-running.svg` | 运行中，蓝色循环箭头，原始 16×16px | I4094:77251;3435:31535 |
| `conversation-status-success.svg` | 执行成功，绿色对勾，原始 16×16px | I4094:77255;3425:30754 |
| `conversation-status-error.svg` | 执行异常，橙色警示灯，原始 16×16px | I4094:77259;3487:19298 |

普通与已归档会话共用 `ConversationHoverCard`，保留图标固有尺寸。
