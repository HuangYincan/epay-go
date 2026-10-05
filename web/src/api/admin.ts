// web/src/api/admin.ts
import request from './request'
import type { ApiResponse, PageData, Merchant, Order, Channel, Settlement, Refund, PluginConfig } from './types'

 const adminApiBase = '/api/admin'

// 登录
export function adminLogin(data: { username: string; password: string }) {
  return request.post<any, ApiResponse>(`${adminApiBase}/auth/login`, data)
}

// 修改密码
export function updateAdminPassword(data: { old_password: string; new_password: string }) {
  return request.put<any, ApiResponse>(`${adminApiBase}/profile/password`, data)
}

// 仪表盘
export function getDashboard() {
  return request.get<any, ApiResponse>(`${adminApiBase}/dashboard`)
}

// 商户列表
export function getMerchants(params: { page: number; page_size: number; status?: number }) {
  return request.get<any, ApiResponse<PageData<Merchant>>>(`${adminApiBase}/merchants`, { params })
}

// 商户详情
export function getMerchant(id: number) {
  return request.get<any, ApiResponse<Merchant>>(`${adminApiBase}/merchants/${id}`)
}

// 更新商户状态
export function updateMerchantStatus(id: number, status: number) {
  return request.patch<any, ApiResponse>(`${adminApiBase}/merchants/${id}/status`, { status })
}

// 订单列表
export function getOrders(params: { page: number; page_size: number; merchant_id?: number; status?: number }) {
  return request.get<any, ApiResponse<PageData<Order>>>(`${adminApiBase}/orders`, { params })
}

// 订单详情
export function getOrder(tradeNo: string) {
  return request.get<any, ApiResponse<Order>>(`${adminApiBase}/orders/${tradeNo}`)
}

// 重发通知
export function renotifyOrder(tradeNo: string) {
  return request.post<any, ApiResponse>(`${adminApiBase}/orders/${tradeNo}/renotify`)
}

// 通道列表
export function getChannels(params: { page: number; page_size: number }) {
  return request.get<any, ApiResponse<PageData<Channel>>>(`${adminApiBase}/channels`, { params })
}

// 创建通道
export function createChannel(data: Partial<Channel>) {
  return request.post<any, ApiResponse>(`${adminApiBase}/channels`, data)
}

// 更新通道
export function updateChannel(id: number, data: Partial<Channel>) {
  return request.put<any, ApiResponse>(`${adminApiBase}/channels/${id}`, data)
}

// 删除通道
export function deleteChannel(id: number) {
  return request.delete<any, ApiResponse>(`${adminApiBase}/channels/${id}`)
}

// 获取所有插件
export const getPlugins = () =>
  request.get<any, ApiResponse<{ name: string; show_name: string; author: string }[]>>(`${adminApiBase}/plugins`)

// 获取插件配置模板
export const getPluginConfig = (plugin: string) =>
  request.get<any, ApiResponse<PluginConfig>>(`${adminApiBase}/plugins/${plugin}/config`)

// 结算列表
export function getSettlements(params: { page: number; page_size: number; merchant_id?: number; status?: number }) {
  return request.get<any, ApiResponse<PageData<Settlement>>>(`${adminApiBase}/settlements`, { params })
}

// 审核通过
export function approveSettlement(id: number) {
  return request.patch<any, ApiResponse>(`${adminApiBase}/settlements/${id}/approve`)
}

// 驳回
export function rejectSettlement(id: number, remark: string) {
  return request.patch<any, ApiResponse>(`${adminApiBase}/settlements/${id}/reject`, { remark })
}

// 退款管理
export const getRefunds = (params: any) =>
  request.get<any, ApiResponse<PageData<Refund>>>(`${adminApiBase}/refunds`, { params })

export const createRefund = (data: { trade_no: string; amount: string; reason?: string; notify_url?: string }) =>
  request.post<any, ApiResponse<Refund>>(`${adminApiBase}/refunds`, data)

export const processRefund = (refundNo: string, data: { success: boolean; fail_reason?: string }) =>
  request.post<any, ApiResponse>(`${adminApiBase}/refunds/${refundNo}/process`, data)

// 测试支付
export const testPayment = (data: { channel_id: number; amount: string; pay_type: string; openid?: string }) => {
  return request.post<any, ApiResponse>(`${adminApiBase}/test-payment`, data)
}

export function completeSettlement(id: number) { return request.patch<any, ApiResponse>(`${adminApiBase}/settlements/${id}/complete`) }
