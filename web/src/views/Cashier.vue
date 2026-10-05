<!-- web/src/views/Cashier.vue -->
<template>
  <div class="cashier-container">
    <div class="cashier-box" v-if="!loading && order">
      <h2>收银台</h2>
      <div class="order-info">
        <div class="info-row">
          <span class="label">商品名称:</span>
          <span class="value">{{ order.name }}</span>
        </div>
        <div class="info-row">
          <span class="label">订单号:</span>
          <span class="value">{{ order.trade_no }}</span>
        </div>
        <div class="info-row amount-row">
          <span class="label">支付金额:</span>
          <span class="amount">¥{{ order.amount }}</span>
        </div>
      </div>

      <div class="pay-methods" v-if="order.status === 0">
        <div class="method-title">选择支付方式</div>
        <div class="methods">
          <div
            v-if="order.pay_type === 'alipay'"
            class="method-item"
            :class="{ active: selectedMethod === 'alipay' }"
            @click="selectedMethod = 'alipay'"
          >
            <icon-alipay-circle style="font-size: 32px; color: #1677ff" />
            <span>支付宝</span>
          </div>
          <div v-if="order.pay_type === 'wxpay'"
            class="method-item"
            :class="{ active: selectedMethod === 'wxpay' }"
            @click="selectedMethod = 'wxpay'"
          >
            <icon-wechat style="font-size: 32px; color: #07c160" />
            <span>微信支付</span>
          </div>
        </div>
        <a-button type="primary" size="large" long @click="handlePay" :loading="paying">
          立即支付
        </a-button>
      </div>

      <div v-if="qrImage" style="text-align:center"><img :src="qrImage" alt="支付二维码" width="240" height="240" /><p>请使用原支付方式扫码，页面会自动更新支付状态。</p></div>
      <div class="pay-result" v-if="order.status === 1">
        <icon-check-circle style="font-size: 64px; color: #00b42a" />
        <div class="result-text">支付成功</div>
        <div class="result-hint">感谢您的支付</div>
      </div>

      <div class="pay-result" v-else-if="order.status !== 0">
        <icon-close-circle style="font-size: 64px; color: #f53f3f" />
        <div class="result-text">订单已关闭</div>
      </div>
    </div>

    <div class="cashier-box" v-else-if="loading">
      <a-spin tip="加载中..." />
    </div>

    <div class="cashier-box" v-else>
      <a-result status="error" title="订单不存在" subtitle="请检查订单号是否正确">
        <template #extra>
          <a-button type="primary" @click="$router.push('/')">返回首页</a-button>
        </template>
      </a-result>
    </div>
  </div>
</template>

<script setup lang="ts">
import { ref, onMounted, onUnmounted } from 'vue'
import { useRoute } from 'vue-router'
import { Message } from '@arco-design/web-vue'
import request from '@/api/request'
import QRCode from 'qrcode'

interface CashierOrder {
  trade_no: string
  name: string
  amount: string
  status: number
  pay_type: string
}

const route = useRoute()
const loading = ref(true)
const paying = ref(false)
const order = ref<CashierOrder | null>(null)
const selectedMethod = ref('alipay')
const qrImage = ref('')
let pollTimer: ReturnType<typeof setInterval> | undefined

const fetchOrder = async () => {
  const tradeNo = route.params.tradeNo as string
  if (!tradeNo) {
    loading.value = false
    return
  }
  try {
    const res = await request.get(`/api/cashier/${tradeNo}`)
    order.value = res.data
    selectedMethod.value = res.data.pay_type
    if (res.data.status !== 0) qrImage.value = ''
  } catch (e) {
    order.value = null
  } finally {
    loading.value = false
  }
}

const handlePay = async () => {
  if (!order.value) return
  paying.value = true
  try {
    const res = await request.post(`/api/cashier/${order.value.trade_no}/pay`, {
      pay_type: selectedMethod.value,
    })
    if (res.data.pay_type === 'qrcode' && res.data.pay_url) {
      qrImage.value = await QRCode.toDataURL(res.data.pay_url)
    } else if (res.data.pay_type === 'jsapi' && res.data.pay_params) {
      const bridge = (window as unknown as { WeixinJSBridge?: { invoke: (name: string, args: object, cb: (result: { err_msg: string }) => void) => void } }).WeixinJSBridge
      if (!bridge) { Message.warning('请在微信内打开此页面'); return }
      bridge.invoke('getBrandWCPayRequest', JSON.parse(res.data.pay_params), result => {
        if (result.err_msg === 'get_brand_wcpay_request:ok') fetchOrder()
        else Message.warning('支付未完成，请重试')
      })
    } else if (res.data.pay_url) {
      window.location.href = res.data.pay_url
    }

  } catch (e) {
    // ignore
  } finally {
    paying.value = false
  }
}

onMounted(() => {
  fetchOrder()
  pollTimer = setInterval(() => { if (order.value?.status === 0) fetchOrder() }, 3000)
})
onUnmounted(() => { if (pollTimer) clearInterval(pollTimer) })
</script>

<style scoped>
.cashier-container {
  min-height: 100vh;
  display: flex;
  align-items: center;
  justify-content: center;
  background: linear-gradient(135deg, #667eea 0%, #764ba2 100%);
  padding: 20px;
}

.cashier-box {
  width: 420px;
  padding: 40px;
  background: #fff;
  border-radius: 12px;
  box-shadow: 0 8px 32px rgba(0, 0, 0, 0.15);
}

.cashier-box h2 {
  text-align: center;
  margin-bottom: 32px;
  color: #1d2129;
}

.order-info {
  background: #f7f8fa;
  border-radius: 8px;
  padding: 20px;
  margin-bottom: 24px;
}

.info-row {
  display: flex;
  justify-content: space-between;
  margin-bottom: 12px;
}

.info-row:last-child {
  margin-bottom: 0;
}

.info-row .label {
  color: #86909c;
}

.info-row .value {
  color: #1d2129;
}

.amount-row {
  padding-top: 12px;
  border-top: 1px dashed #e5e6eb;
  margin-top: 12px;
}

.amount-row .amount {
  font-size: 24px;
  font-weight: bold;
  color: #f53f3f;
}

.pay-methods {
  margin-top: 24px;
}

.method-title {
  color: #86909c;
  margin-bottom: 16px;
}

.methods {
  display: flex;
  gap: 16px;
  margin-bottom: 24px;
}

.method-item {
  flex: 1;
  display: flex;
  flex-direction: column;
  align-items: center;
  gap: 8px;
  padding: 16px;
  border: 2px solid #e5e6eb;
  border-radius: 8px;
  cursor: pointer;
  transition: all 0.2s;
}

.method-item:hover {
  border-color: #165dff;
}

.method-item.active {
  border-color: #165dff;
  background: #f2f3ff;
}

.method-item span {
  font-size: 14px;
  color: #1d2129;
}

.pay-result {
  text-align: center;
  padding: 40px 0;
}

.result-text {
  font-size: 20px;
  font-weight: 500;
  color: #1d2129;
  margin-top: 16px;
}

.result-hint {
  color: #86909c;
  margin-top: 8px;
}
</style>
