import apiClient from '@/api/client'

export type SkillSource = 'github' | 'skillhub' | 'clawhub'
export interface Skill {
  id: number
  name: string
  remark: string
  icon: string
  description: string
  sharer: string
  source_url: string
  source: SkillSource
  review_status: 'pending' | 'approved' | 'rejected'
  created_at: number
  updated_at: number
}
export interface SkillPage {
  list: Skill[]
  total: number
  page: number
  size: number
}
export interface CreateSkillInput {
  name: string
  icon: string
  description: string
  sharer: string
  source_url: string
  source: SkillSource
}
export const skillApi = {
  list: (page: number, size: number, name: string) =>
    apiClient.get<SkillPage>('/skills', { page, size, name, review_status: 'approved' }),
  create: (input: CreateSkillInput) =>
    apiClient.post<{ id: number }>('/skills', { ...input, review_status: 'pending' }),
}

