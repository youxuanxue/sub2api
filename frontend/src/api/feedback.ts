import { apiClient } from './client'

export type FeedbackCategory = 'api' | 'billing' | 'technical' | 'other'
export type FeedbackStatus = 'open' | 'processing' | 'resolved' | 'closed'

export interface FeedbackTicket {
  id: number
  ticket_no: string
  user_id: number
  category: FeedbackCategory
  subject: string
  content: string
  request_id?: string
  status: FeedbackStatus
  created_at: string
  updated_at: string
}

export interface CreateFeedbackRequest {
  category: FeedbackCategory
  subject: string
  content: string
  request_id?: string
}

export async function list(): Promise<FeedbackTicket[]> {
  const { data } = await apiClient.get<FeedbackTicket[]>('/feedback')
  return data
}

export async function create(payload: CreateFeedbackRequest): Promise<FeedbackTicket> {
  const { data } = await apiClient.post<FeedbackTicket>('/feedback', payload)
  return data
}

export async function adminList(): Promise<FeedbackTicket[]> {
  const { data } = await apiClient.get<FeedbackTicket[]>('/admin/feedback')
  return data
}

export async function adminUpdateStatus(id: number, status: FeedbackStatus): Promise<FeedbackTicket> {
  const { data } = await apiClient.patch<FeedbackTicket>(`/admin/feedback/${id}`, { status })
  return data
}

export const feedbackAPI = { list, create, adminList, adminUpdateStatus }
export default feedbackAPI
