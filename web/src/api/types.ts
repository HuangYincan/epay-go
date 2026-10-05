// web/src/api/types.ts
// 通用响应
export interface ApiResponse<T = any> {
  code: number
  msg: string
  data: T
}

// 分页响应
export interface PageData<T> {
  list: T[]
  total: number
  page: number
  page_size: number
}

// 商户
export interface Merchant {
  id: number
  username: string
  email: string
  phone: string
  api_key: string
  balance: string
  frozen_balance: string
  status: number
  created_at: string
}

// 订单
export interface Order {
  id: number
  trade_no: string
  out_trade_no: string
  merchant_id: number
  channel_id: number
  pay_type: string
  amount: string
  real_amount: string
  fee: string
  name: string
  status: number
  notify_status: number
  paid_at: string | null
  created_at: string
}

// 通道
export interface Channel {
  id: number
  name: string
  plugin: string
  pay_types: string
  app_type?: string
  config: any
  callback_url?: string
  rate: string | number
  daily_limit: string | number
  status: number
  sort: number
}

// 结算
export interface Settlement {
  id: number
  settle_no: string
  merchant_id: number
  amount: string
  fee: string
  actual_amount: string
  account_type: string
  account_no: string
  account_name: string
  status: number
  remark: string
  created_at: string
}

// 资金记录
export interface BalanceRecord {
  id: number
  merchant_id: number
  action: number
  amount: string
  before_balance: string
  after_balance: string
  type: string
  trade_no: string
  created_at: string
}

// 退款
export interface Refund {
  id: number
  refund_no: string
  trade_no: string
  merchant_id: number
  order_id: number
  channel_id: number
  amount: string
  refund_fee: string
  reason: string
  status: number // 0待处理 1成功 2失败
  api_refund_no: string
  fail_reason: string
  notify_url: string
  notify_status: number
  notify_count: number
  next_notify_time: string | null
  processed_at: string | null
  created_at: string
}

// 插件配置字段
export interface PluginConfigField {
  key: string
  name: string
  type: 'input' | 'textarea' | 'select' | 'checkbox' | 'boolean'
  required: boolean
  placeholder: string
  note: string
  options?: Record<string, string>
}

// 支付接口选项
export interface PayTypeOption {
  code: string
  name: string
}

// 插件配置
export interface PluginConfig {
  name: string
  show_name: string
  author: string
  link: string
  inputs: PluginConfigField[]
  pay_types: PayTypeOption[]
  bind_wxmp: boolean
  bind_wxa: boolean
  note: string
}
