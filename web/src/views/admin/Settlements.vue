<!-- web/src/views/admin/Settlements.vue -->
<template>
  <div>
    <a-table :data="settlements" :loading="loading" :pagination="pagination" @page-change="handlePageChange">
      <template #columns>
        <a-table-column title="结算单号" data-index="settle_no" :width="200" />
        <a-table-column title="商户ID" data-index="merchant_id" :width="80" />
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
        <a-table-column title="申请时间" data-index="created_at" :width="180" />
        <a-table-column title="操作" :width="150">
          <template #cell="{ record }">
            <template v-if="record.status === 0">
              <a-button type="text" size="small" status="success" @click="handleApprove(record.id)">
                通过
              </a-button>
              <a-button type="text" size="small" status="danger" @click="openRejectModal(record.id)">
                驳回
              </a-button>
            </template>
            <a-button v-else-if="record.status === 1" type="text" @click="handleComplete(record.id)">确认已打款</a-button>
            <span v-else class="text-gray">-</span>
          </template>
        </a-table-column>
      </template>
    </a-table>

    <a-modal v-model:visible="rejectVisible" title="驳回结算申请" @ok="handleReject" :ok-loading="rejecting">
      <a-form layout="vertical">
        <a-form-item label="驳回原因">
          <a-textarea v-model="rejectRemark" :auto-size="{ minRows: 3 }" placeholder="请输入驳回原因" />
        </a-form-item>
      </a-form>
    </a-modal>
  </div>
</template>

<script setup lang="ts">
import { ref, reactive, onMounted } from 'vue'
import { Message, Modal } from '@arco-design/web-vue'
import { getSettlements, approveSettlement, rejectSettlement, completeSettlement } from '@/api/admin'
import type { Settlement } from '@/api/types'

const loading = ref(false)
const settlements = ref<Settlement[]>([])
const pagination = reactive({
  current: 1,
  pageSize: 20,
  total: 0,
})

const rejectVisible = ref(false)
const rejecting = ref(false)
const rejectId = ref(0)
const rejectRemark = ref('')

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

const handleApprove = async (id: number) => {
  try {
    await approveSettlement(id)
    Message.success('审核通过')
    fetchData()
  } catch (e) {
    // ignore
  }
}

const handleComplete = (id: number) => {
  Modal.confirm({ title: '确认结算完成', content: '请确认已向该商户实际打款。本操作会扣除预留的冻结资金。', onOk: async () => { await completeSettlement(id); Message.success('结算已完成'); await fetchData() } })
}

const openRejectModal = (id: number) => {
  rejectId.value = id
  rejectRemark.value = ''
  rejectVisible.value = true
}

const handleReject = async () => {
  if (!rejectRemark.value.trim()) {
    Message.warning('请输入驳回原因')
    return
  }
  rejecting.value = true
  try {
    await rejectSettlement(rejectId.value, rejectRemark.value)
    Message.success('已驳回')
    rejectVisible.value = false
    fetchData()
  } catch (e) {
    // ignore
  } finally {
    rejecting.value = false
  }
}

onMounted(() => {
  fetchData()
})
</script>

<style scoped>
.text-gray {
  color: #86909c;
}
</style>
