/**
 * 知识库引用（编辑器 → 对话输入区）的共享类型。
 * 依据：`02-架构师/output_design.md` §3.3.3。
 */

/**
 * 选中片段及其前后文定位。
 * CX-2 / D3-H2：不做真实行号，改用「选中文本 + DOM 级前后各 ≤32 字定位」表达引用范围。
 */
export interface KnowledgeReferenceFragment {
  /** 选中文本 */
  text: string
  /** 选中内容之前的前文片段（用于定位） */
  prefix: string
  /** 选中内容之后的后文片段（用于定位） */
  suffix: string
}

/** 输入框已登记的知识库引用，随任务草稿保留。 */
export interface KnowledgeDocumentReference {
  id: string
  marker: string
  name: string
  kind: 'knowledge'
  knowledge: {
    uuid: string
    filePath: string
    folderPath: string
    /** null 表示整篇文档 */
    fragment: KnowledgeReferenceFragment | null
    invalid: boolean
  }
}

/**
 * 待投递的知识库引用草稿（编辑器选中内容 → 对话输入区）。
 * `fragment` 恒为编辑器选中片段；整篇文档引用（T5 的「从资料库中选择」）由对话侧的引用类型承载。
 */
export interface KnowledgeReferenceDraft {
  /** 文档 uuid */
  uuid: string
  /** 含扩展名的文件名（取自 file_path 的真实文件名） */
  title: string
  /** 相对 KR 的 file_path（S-DA-06：换目录迁移后语义不变） */
  filePath: string
  /** 所属文件夹路径，根为 teamsboard */
  folderPath: string
  fragment: KnowledgeReferenceFragment
  /** 用户输入的指令，可为空串 */
  instruction: string
}

/** S-IN-09 / S-UI-22：引用有效性校验结果（文档已入回收站或磁盘文件不存在即失效） */
export interface KnowledgeReferenceValidation {
  valid: boolean
  /** 失效原因，仅用于定位问题，不直接展示给用户 */
  reason: string
}

/**
 * S-IN-08 / S-DA-12：超长引用内容落盘结果。
 * 字段与 `output_openapi.yaml` 的 `ReferenceSpillResponse` 逐字一致（`api/knowledge.ts`
 * 用契约推导出的响应类型赋值给本类型，契约改动会在类型层暴露）。
 */
export interface ReferenceSpillResult {
  /** 落盘文件相对任务产出目录的路径（唯一，不覆盖既有文件） */
  path: string
  /** 落盘文件绝对路径 */
  absolute_path: string
  /** 写入字节数 */
  size: number
}
