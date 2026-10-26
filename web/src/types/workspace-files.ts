export interface WorkspaceRoot {
  id: string
  name: string
  path: string
  primary: boolean
  available: boolean
  error_code?: string
}

export interface WorkspaceFileEntry {
  name: string
  path: string
  is_dir: boolean
  file_type?: string
  size?: number
  modified_at?: number
  hidden?: boolean
  ignored?: boolean
}

export interface WorkspaceRootsResponse {
  roots: WorkspaceRoot[]
}

export interface WorkspaceEntriesResponse {
  root_id: string
  path: string
  entries: WorkspaceFileEntry[]
  truncated: boolean
  limit: number
}

export interface WorkspaceSearchResponse {
  root_id: string
  query: string
  entries: WorkspaceFileEntry[]
  truncated: boolean
  truncation_reason?: 'result_limit' | 'visit_limit' | 'timeout' | ''
  partial?: boolean
  limit: number
}

export interface WorkspaceFileContent {
  root_id: string
  path: string
  name: string
  file_type: string
  mime_type: string
  size: number
  modified_at: number
  encoding: 'text' | 'base64'
  content: string
  read_only: true
}
