<!-- web/src/views/admin/TestPayment.vue -->
<template>
  <div class="test-payment-page">
    <a-card title="通道测试支付">
      <a-form :model="form" layout="vertical" style="max-width: 600px">
        <a-form-item label="选择通道" required>
          <a-select
            v-model="form.channel_id"
            placeholder="请选择要测试的支付通道"
            :loading="loadingChannels"
            @change="handleChannelChange"
          >
            <a-option
              v-for="channel in channels"
              :key="channel.id"
              :value="channel.id"
            >
              {{ channel.name }} ({{ channel.plugin }})
            </a-option>
          </a-select>
        </a-form-item>

        <a-form-item v-if="selectedChannel" label="支付接口" required>
          <a-select
            v-model="form.pay_type"
            placeholder="请选择支付接口"
          >
            <a-option
              v-for="payType in payTypes"
              :key="payType.value"
              :value="payType.value"
            >
              {{ payType.label }}
            </a-option>
          </a-select>
          <template #extra>
            <div style="color: #86909c; font-size: 12px">
              支持的支付接口: {{ payTypes.map(item => item.label).join('、') }}
            </div>
          </template>
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
            <div style="color: #86909c; font-size: 12px">
              建议使用小额金额进行测试，如 0.01 元
            </div>
          </template>
        </a-form-item>

        <a-form-item>
          <a-button
            type="primary"
            :loading="testing"
            @click="handleTest"
            long
          >
            开始测试支付
          </a-button>
        </a-form-item>
      </a-form>
    </a-card>

    <!-- 支付结果 -->
    <a-card v-if="payResult" title="支付结果" style="margin-top: 20px">
      <a-descriptions :column="1" bordered>
        <a-descriptions-item label="订单号">
          {{ payResult.order.trade_no }}
        </a-descriptions-item>
        <a-descriptions-item label="商户订单号">
          {{ payResult.order.out_trade_no }}
        </a-descriptions-item>
        <a-descriptions-item label="支付金额">
          ¥{{ payResult.order.amount }}
        </a-descriptions-item>
        <a-descriptions-item label="支付方式">
          {{ payResult.order.pay_type }}
        </a-descriptions-item>
        <a-descriptions-item label="订单状态">
          <a-tag :color="payResult.order.status === 1 ? 'green' : 'orange'">
            {{ payResult.order.status === 0 ? '待支付' : '已支付' }}
          </a-tag>
        </a-descriptions-item>
      </a-descriptions>

      <!-- 二维码支付 -->
      <div v-if="payResult.pay_data.pay_url && isQRCodePayment" style="margin-top: 20px">
        <a-divider>扫码支付</a-divider>
        <div style="text-align: center">
          <div ref="qrcodeContainer" style="display: inline-block"></div>
          <div style="margin-top: 10px; color: #86909c">
            请使用{{ getPaymentAppName() }}扫码完成支付
          </div>
          <a-button type="text" @click="copyPayUrl" style="margin-top: 10px">
            复制支付链接
          </a-button>
        </div>
      </div>

      <!-- 跳转支付 -->
      <div v-else-if="payResult.pay_data.pay_url" style="margin-top: 20px">
        <a-divider>跳转支付</a-divider>
        <div style="text-align: center">
          <a-button type="primary" @click="openPayUrl">
            打开支付页面
          </a-button>
          <div style="margin-top: 10px; color: #86909c">
            点击按钮将打开新窗口进行支付
          </div>
        </div>
      </div>

      <!-- 支付参数 -->
      <div v-if="payResult.pay_data.pay_params" style="margin-top: 20px">
        <a-divider>支付参数</a-divider>
        <a-textarea
          :model-value="payResult.pay_data.pay_params"
          :auto-size="{ minRows: 3, maxRows: 10 }"
          readonly
        />
        <a-button type="text" @click="copyPayParams" style="margin-top: 10px">
          复制参数
        </a-button>
      </div>

      <a-alert type="info" style="margin-top: 20px">
        <template #icon><icon-info-circle /></template>
        测试订单创建成功！请完成支付以验证通道配置是否正确。支付成功后可在订单列表中查看订单状态。
      </a-alert>
    </a-card>
  </div>
</template>

<script setup lang="ts">
import { ref, reactive, computed, nextTick } from 'vue'
import { Message } from '@arco-design/web-vue'
import { IconInfoCircle } from '@arco-design/web-vue/es/icon'
import { getChannels, testPayment } from '@/api/admin'
import type { Channel } from '@/api/types'
import { findPaymentOption, getPaymentOptionsByPlugin } from '@/utils/paymentOptions'

const loadingChannels = ref(false)
const testing = ref(false)
const channels = ref<Channel[]>([])
const selectedChannel = ref<Channel | null>(null)
const payResult = ref<any>(null)
const qrcodeContainer = ref<HTMLElement>()

const form = reactive({
  openid: '',
  channel_id: undefined as number | undefined,
  pay_type: '',
  amount: 0.01
})

// 获取支付类型列表
const payTypes = computed(() => {
  if (!selectedChannel.value) return []
  return getPaymentOptionsByPlugin(selectedChannel.value.plugin)
})

const selectedPayOption = computed(() => findPaymentOption(form.pay_type))

// 判断是否是二维码支付
const isQRCodePayment = computed(() => {
  return selectedPayOption.value?.mode === 'qrcode'
})

// 获取支付应用名称
const getPaymentAppName = () => {
  if (selectedPayOption.value?.provider === 'alipay') return '支付宝'
  if (selectedPayOption.value?.provider === 'wechat') return '微信'
  if (!selectedChannel.value) return '支付应用'
  const plugin = selectedChannel.value.plugin.toLowerCase()
  if (plugin.includes('alipay')) return '支付宝'
  if (plugin.includes('wechat') || plugin.includes('wxpay')) return '微信'
  return '支付应用'
}

// 加载通道列表
const loadChannels = async () => {
  loadingChannels.value = true
  try {
    const res = await getChannels({ page: 1, page_size: 100 })
    channels.value = res.data.list.filter((c: Channel) => c.status === 1)
  } catch (e) {
    Message.error('加载通道列表失败')
  } finally {
    loadingChannels.value = false
  }
}

// 通道切换
const handleChannelChange = (channelId: number) => {
  selectedChannel.value = channels.value.find(c => c.id === channelId) || null
  form.pay_type = ''
  payResult.value = null
}

// 测试支付
const handleTest = async () => {
  if (!form.channel_id) {
    Message.warning('请选择支付通道')
    return
  }
  if (!form.pay_type) {
    Message.warning('请选择支付接口')
    return
  }
  if (form.amount <= 0) {
    Message.warning('请输入有效的支付金额')
    return
  }

  testing.value = true
  try {
    const payOption = findPaymentOption(form.pay_type)
    if (!payOption) {
      Message.warning('请选择有效的支付接口')
      return
    }
    const res = await testPayment({
      channel_id: form.channel_id,
      amount: form.amount.toString(),
      openid: form.openid || undefined,
      pay_type: payOption.payMethod || payOption.payType
    })

    payResult.value = res.data
    Message.success('测试订单创建成功')

    // 如果是二维码支付，生成二维码
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

// 生成二维码
const generateQRCode = async (url: string) => {
  if (!qrcodeContainer.value) return

  // 清空容器
  qrcodeContainer.value.innerHTML = ''

  try {
    // 使用在线二维码 API 生成二维码图片
    const qrCodeUrl = `https://api.qrserver.com/v1/create-qr-code/?size=200x200&data=${encodeURIComponent(url)}`
    const img = document.createElement('img')
    img.src = qrCodeUrl
    img.alt = '支付二维码'
    img.style.width = '200px'
    img.style.height = '200px'
    qrcodeContainer.value.appendChild(img)
  } catch (e) {
    console.error('生成二维码失败', e)
  }
}

// 复制支付链接
const copyPayUrl = () => {
  if (!payResult.value?.pay_data.pay_url) return

  navigator.clipboard.writeText(payResult.value.pay_data.pay_url)
  Message.success('支付链接已复制到剪贴板')
}

// 打开支付链接
const openPayUrl = () => {
  if (!payResult.value?.pay_data.pay_url) return
  window.open(payResult.value.pay_data.pay_url, '_blank')
}

// 复制支付参数
const copyPayParams = () => {
  if (!payResult.value?.pay_data.pay_params) return

  navigator.clipboard.writeText(payResult.value.pay_data.pay_params)
  Message.success('支付参数已复制到剪贴板')
}

// 初始化
loadChannels()
</script>

<style scoped>
.test-payment-page {
  padding: 20px;
}

.header-bar {
  margin-bottom: 20px;
}
</style>
