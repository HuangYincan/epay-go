// web/src/api/merchant.ts
import request from './request'
import type { ApiResponse, PageData, Merchant, Order, Settlement, BalanceRecord, Refund } from './types'

 const merchantApiBase = '/api/merchant'

// 注册
export function register(data: { username: string; password: string; email?: string; phone?: string }) {
  return request.post<any, ApiResponse>(`${merchantApiBase}/auth/register`, data)
}

// 登录
export function login(data: { username: string; password: string }) {
  return request.post<any, ApiResponse>(`${merchantApiBase}/auth/login`, data)
}

// 仪表盘
export function getDashboard() {
  return request.get<any, ApiResponse>(`${merchantApiBase}/dashboard`)
}

// 获取个人信息
export function getProfile() {
  return request.get<any, ApiResponse<Merchant>>(`${merchantApiBase}/profile`)
}

// 更新个人信息
export function updateProfile(data: { email?: string; phone?: string }) {
  return request.put<any, ApiResponse>(`${merchantApiBase}/profile`, data)
}

// 修改密码
export function updatePassword(data: { old_password: string; new_password: string }) {
  return request.put<any, ApiResponse>(`${merchantApiBase}/profile/password`, data)
}

// 重置API密钥
export function resetApiKey() {
  return request.post<any, ApiResponse<{ api_key: string }>>(`${merchantApiBase}/profile/reset-key`)
}

// 订单列表
export function getOrders(params: { page: number; page_size: number; status?: number }) {
  return request.get<any, ApiResponse<PageData<Order>>>(`${merchantApiBase}/orders`, { params })
}

// 订单详情
export function getOrder(tradeNo: string) {
  return request.get<any, ApiResponse<Order>>(`${merchantApiBase}/orders/${tradeNo}`)
}

// 结算列表
export function getSettlements(params: { page: number; page_size: number; status?: number }) {
  return request.get<any, ApiResponse<PageData<Settlement>>>(`${merchantApiBase}/settlements`, { params })
}

// 申请结算
export function applySettlement(data: {
  amount: string
  account_type: string
  account_no: string
  account_name: string
}) {
  return request.post<any, ApiResponse>(`${merchantApiBase}/settlements`, data)
}

// 资金记录
export function getRecords(params: { page: number; page_size: number }) {
  return request.get<any, ApiResponse<PageData<BalanceRecord>>>(`${merchantApiBase}/records`, { params })
}

// 退款管理
export const createRefund = (data: {
  trade_no: string
  amount: string
  reason?: string
  notify_url?: string
}) => request.post<any, ApiResponse>(`${merchantApiBase}/refunds`, data)

export const getRefunds = (params: any) =>
  request.get<any, ApiResponse<PageData<Refund>>>(`${merchantApiBase}/refunds`, { params })

// 商户测试支付
export function testPayment(data: { amount: string; pay_type: string; pay_method?: string; openid?: string }) {
  return request.post<any, ApiResponse>(`${merchantApiBase}/test-payment`, data)
}
