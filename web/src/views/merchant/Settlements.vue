<!-- web/src/views/merchant/Settlements.vue -->
<template>
  <div>
    <div class="header-bar">
      <a-button type="primary" @click="openApplyModal">
        <template #icon><icon-plus /></template>
        申请结算
      </a-button>
    </div>

    <a-table :data="settlements" :loading="loading" :pagination="pagination" @page-change="handlePageChange">
      <template #columns>
        <a-table-column title="结算单号" data-index="settle_no" :width="200" />
        <a-table-column title="申请金额" data-index="amount" :width="120">
          <template #cell="{ record }">¥{{ record.amount }}</template>
        </a-table-column>
        <a-table-column title="手续费" data-index="fee" :width="100">
          <template #cell="{ record }">¥{{ record.fee }}</template>
        </a-table-column>
        <a-table-column title="实际到账" data-index="actual_amount" :width="120">
          <template #cell="{ record }">¥{{ record.actual_amount }}</template>
        </a-table-column>
        <a-table-column title="账户类型" data-index="account_type" :width="100">
          <template #cell="{ record }">{{ accountTypeText(record.account_type) }}</template>
        </a-table-column>
        <a-table-column title="收款账号" data-index="account_no" :width="180" />
        <a-table-column title="状态" data-index="status" :width="100">
          <template #cell="{ record }">
            <a-tag :color="statusColor(record.status)">{{ statusText(record.status) }}</a-tag>
          </template>
        </a-table-column>
        <a-table-column title="备注" data-index="remark" :width="150" />
        <a-table-column title="申请时间" data-index="created_at" :width="180" />
      </template>
    </a-table>

    <a-modal v-model:visible="applyVisible" title="申请结算" @ok="handleApply" :ok-loading="applying">
      <a-form :model="applyForm" layout="vertical">
        <a-form-item label="结算金额" required>
          <a-input-number v-model="applyForm.amount" :precision="2" :min="1" placeholder="请输入结算金额" style="width: 100%" />
        </a-form-item>
        <a-form-item label="账户类型" required>
          <a-select v-model="applyForm.account_type" placeholder="请选择账户类型">
            <a-option value="alipay">支付宝</a-option>
            <a-option value="bank">银行卡</a-option>
          </a-select>
        </a-form-item>
        <a-form-item label="收款账号" required>
          <a-input v-model="applyForm.account_no" placeholder="请输入收款账号" />
        </a-form-item>
        <a-form-item label="收款人姓名" required>
          <a-input v-model="applyForm.account_name" placeholder="请输入收款人姓名" />
        </a-form-item>
      </a-form>
    </a-modal>
  </div>
</template>

<script setup lang="ts">
import { ref, reactive, onMounted } from 'vue'
import { Message } from '@arco-design/web-vue'
import { getSettlements, applySettlement } from '@/api/merchant'
import type { Settlement } from '@/api/types'

const loading = ref(false)
const applying = ref(false)
const settlements = ref<Settlement[]>([])
const pagination = reactive({
  current: 1,
  pageSize: 20,
  total: 0,
})

const applyVisible = ref(false)
const applyForm = reactive({
  amount: 0,
  account_type: '',
  account_no: '',
  account_name: '',
})

const statusText = (status: number) => {
  const map: Record<number, string> = { 0: '待审核', 1: '待打款', 2: '已完成', 3: '已驳回' }
  return map[status] || '未知'
}

const statusColor = (status: number) => {
  const map: Record<number, string> = { 0: 'orange', 1: 'blue', 2: 'green', 3: 'red' }
  return map[status] || 'gray'
}

const accountTypeText = (type: string) => {
  const map: Record<string, string> = { alipay: '支付宝', bank: '银行卡' }
  return map[type] || type
}

const fetchData = async () => {
  loading.value = true
  try {
    const res = await getSettlements({ page: pagination.current, page_size: pagination.pageSize })
    settlements.value = res.data.list
    pagination.total = res.data.total
  } catch (e) {
    // ignore
  } finally {
    loading.value = false
  }
}

const handlePageChange = (page: number) => {
  pagination.current = page
  fetchData()
}

const openApplyModal = () => {
  applyForm.amount = 0
  applyForm.account_type = ''
  applyForm.account_no = ''
  applyForm.account_name = ''
  applyVisible.value = true
}

const handleApply = async () => {
  if (!applyForm.amount || !applyForm.account_type || !applyForm.account_no || !applyForm.account_name) {
    Message.warning('请填写完整信息')
    return
  }
  applying.value = true
  try {
    await applySettlement({
      ...applyForm,
      amount: String(applyForm.amount),
    })
    Message.success('申请已提交')
    applyVisible.value = false
    fetchData()
  } catch (e) {
    // ignore
  } finally {
    applying.value = false
  }
}

onMounted(() => {
  fetchData()
})
</script>

<style scoped>
.header-bar {
  margin-bottom: 16px;
}
</style>
