<!-- web/src/views/merchant/TestPayment.vue -->
<template>
  <div class="test-payment-page">
    <a-card title="测试支付">
      <a-form :model="form" layout="vertical" style="max-width: 600px">
        <a-form-item label="支付接口" required>
          <a-select v-model="form.pay_type" placeholder="请选择支付接口">
            <a-option v-for="option in paymentOptions" :key="option.value" :value="option.value">{{ option.label }}</a-option>
          </a-select>
        </a-form-item>

        <a-form-item label="JSAPI付款人 OpenID（仅JSAPI需要）"><a-input v-model="form.openid" placeholder="填写此应用下付款人的OpenID" /></a-form-item>
        <a-form-item label="支付金额" required>
          <a-input-number
            v-model="form.amount"
            :precision="2"
            :min="0.01"
            :max="10000"
            placeholder="请输入测试金额"
            style="width: 100%"
          >
            <template #prefix>¥</template>
          </a-input-number>
          <template #extra>
            <div style="color: #86909c; font-size: 12px">建议使用小额金额测试，如 0.01 元</div>
          </template>
        </a-form-item>

        <a-form-item>
          <a-button type="primary" :loading="testing" @click="handleTest" long>开始测试支付</a-button>
        </a-form-item>
      </a-form>
    </a-card>

    <a-card v-if="payResult" title="支付结果" style="margin-top: 20px">
      <a-descriptions :column="1" bordered>
        <a-descriptions-item label="订单号">
          {{ payResult.order.trade_no }}
        </a-descriptions-item>
        <a-descriptions-item label="商户订单号">
          {{ payResult.order.out_trade_no }}
        </a-descriptions-item>
        <a-descriptions-item label="支付金额"> ¥{{ payResult.order.amount }} </a-descriptions-item>
        <a-descriptions-item label="支付类型"> {{ payResult.order.pay_type }} </a-descriptions-item>
        <a-descriptions-item label="订单状态">
          <a-tag :color="payResult.order.status === 1 ? 'green' : 'orange'">
            {{ payResult.order.status === 0 ? '待支付' : '已支付' }}
          </a-tag>
        </a-descriptions-item>
      </a-descriptions>

      <div v-if="payResult.pay_data.pay_url && isQRCodePayment" style="margin-top: 20px">
        <a-divider>扫码支付</a-divider>
        <div style="text-align: center">
          <div ref="qrcodeContainer" style="display: inline-block"></div>
          <div style="margin-top: 10px; color: #86909c">请使用支付 App 扫码完成支付</div>
          <a-button type="text" @click="copyPayUrl" style="margin-top: 10px">复制支付链接</a-button>
        </div>
      </div>

      <div v-else-if="payResult.pay_data.pay_url" style="margin-top: 20px">
        <a-divider>跳转支付</a-divider>
        <div style="text-align: center">
          <a-button type="primary" @click="openPayUrl">打开支付页面</a-button>
          <div style="margin-top: 10px; color: #86909c">点击按钮将打开新窗口进行支付</div>
        </div>
      </div>

      <div v-if="payResult.pay_data.pay_params" style="margin-top: 20px">
        <a-divider>支付参数</a-divider>
        <a-textarea
          :model-value="payResult.pay_data.pay_params"
          :auto-size="{ minRows: 3, maxRows: 10 }"
          readonly
        />
        <a-button type="text" @click="copyPayParams" style="margin-top: 10px">复制参数</a-button>
      </div>

      <a-alert type="info" style="margin-top: 20px">
        测试订单创建成功！完成支付后可在订单列表中查看订单状态。
      </a-alert>
    </a-card>
  </div>
</template>

<script setup lang="ts">
import { ref, reactive, computed, nextTick } from 'vue'
import { Message } from '@arco-design/web-vue'
import { testPayment } from '@/api/merchant'
import { findPaymentOption, getPaymentOptionsByProvider } from '@/utils/paymentOptions'

const testing = ref(false)
const payResult = ref<any>(null)
const qrcodeContainer = ref<HTMLElement>()
const paymentOptions = getPaymentOptionsByProvider('wechat').concat(getPaymentOptionsByProvider('alipay'))

const form = reactive({
  openid: '',
  pay_type: '',
  amount: 0.01
})

const selectedPayOption = computed(() => findPaymentOption(form.pay_type))

const isQRCodePayment = computed(() => {
  return selectedPayOption.value?.mode === 'qrcode'
})

const handleTest = async () => {
  if (!form.pay_type) {
    Message.warning('请选择支付接口')
    return
  }
  if (form.amount <= 0) {
    Message.warning('请输入有效的支付金额')
    return
  }

  testing.value = true
  payResult.value = null
  try {
    const payOption = findPaymentOption(form.pay_type)
    if (!payOption) {
      Message.warning('请选择有效的支付接口')
      return
    }
    const res = await testPayment({
      amount: form.amount.toString(),
      openid: form.openid || undefined,
      pay_type: payOption.payType,
      pay_method: payOption.payMethod || undefined
    })
    payResult.value = res.data
    Message.success('测试订单创建成功')

    if (isQRCodePayment.value && res.data.pay_data.pay_url) {
      await nextTick()
      generateQRCode(res.data.pay_data.pay_url)
    }
  } catch (e: any) {
    Message.error(e.response?.data?.msg || '测试支付失败')
  } finally {
    testing.value = false
  }
}

const generateQRCode = async (url: string) => {
  if (!qrcodeContainer.value) return
  qrcodeContainer.value.innerHTML = ''
  const qrCodeUrl = `https://api.qrserver.com/v1/create-qr-code/?size=200x200&data=${encodeURIComponent(url)}`
  const img = document.createElement('img')
  img.src = qrCodeUrl
  img.alt = '支付二维码'
  img.style.width = '200px'
  img.style.height = '200px'
  qrcodeContainer.value.appendChild(img)
}

const copyPayUrl = () => {
  const url = payResult.value?.pay_data?.pay_url
  if (!url) return
  navigator.clipboard.writeText(url)
  Message.success('支付链接已复制到剪贴板')
}

const openPayUrl = () => {
  const url = payResult.value?.pay_data?.pay_url
  if (!url) return
  window.open(url, '_blank')
}

const copyPayParams = () => {
  const params = payResult.value?.pay_data?.pay_params
  if (!params) return
  navigator.clipboard.writeText(params)
  Message.success('支付参数已复制到剪贴板')
}
</script>

<style scoped>
.test-payment-page {
  padding: 20px;
}
</style>

