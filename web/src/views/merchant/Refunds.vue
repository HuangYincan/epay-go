<template>
  <div class="refunds-container">
    <a-card title="退款管理">
      <!-- 操作按钮 -->
      <div class="action-bar">
        <a-button type="primary" @click="showCreateModal">申请退款</a-button>
      </div>

      <!-- 搜索表单 -->
      <a-form :model="searchForm" layout="inline" class="search-form">
        <a-form-item label="状态">
          <a-select v-model="searchForm.status" placeholder="请选择状态" style="width: 150px" allow-clear>
            <a-option :value="0">待处理</a-option>
            <a-option :value="1">成功</a-option>
            <a-option :value="2">失败</a-option>
            <a-option :value="3">处理中</a-option>
          </a-select>
        </a-form-item>
        <a-form-item>
          <a-button type="primary" @click="handleSearch">查询</a-button>
          <a-button style="margin-left: 10px" @click="handleReset">重置</a-button>
        </a-form-item>
      </a-form>

      <!-- 退款列表 -->
      <a-table :data="refunds" :loading="loading" :pagination="pagination" @page-change="handlePageChange" @page-size-change="handlePageSizeChange" row-key="id">
        <template #columns>
          <a-table-column title="退款单号" data-index="refund_no" />
          <a-table-column title="订单号" data-index="trade_no" />
          <a-table-column title="退款金额" data-index="amount" />
          <a-table-column title="退款原因" data-index="reason" />
          <a-table-column title="状态">
            <template #cell="{ record }"><a-tag>{{ ['待处理', '成功', '失败', '处理中'][record.status] || '未知' }}</a-tag></template>
          </a-table-column>
          <a-table-column title="失败原因" data-index="fail_reason" />
          <a-table-column title="处理时间" data-index="processed_at" />
          <a-table-column title="创建时间" data-index="created_at" />
        </template>
      </a-table>
    </a-card>

    <!-- 创建退款对话框 -->
    <a-modal
      v-model:visible="createVisible"
      title="申请退款"
      @ok="handleCreateConfirm"
      @cancel="createVisible = false"
      :ok-loading="submitting"
    >
      <a-form :model="createForm" layout="vertical">
        <a-form-item label="订单号" required>
          <a-input
            v-model="createForm.trade_no"
            placeholder="请输入订单号"
          />
        </a-form-item>
        <a-form-item label="退款金额" required>
          <a-input
            v-model="createForm.amount"
            placeholder="请输入退款金额"
            type="number"
            step="0.01"
          />
        </a-form-item>
        <a-form-item label="退款原因">
          <a-textarea
            v-model="createForm.reason"
            placeholder="请输入退款原因"
            :rows="4"
          />
        </a-form-item>
        <a-form-item label="异步通知地址">
          <a-input
            v-model="createForm.notify_url"
            placeholder="请输入异步通知地址（可选）"
          />
        </a-form-item>
      </a-form>
    </a-modal>
  </div>
</template>

<script setup lang="ts">
import { ref, reactive, onMounted } from 'vue'
import { Message } from '@arco-design/web-vue'
import { getRefunds, createRefund } from '@/api/merchant'
import type { Refund } from '@/api/types'

const loading = ref(false)
const submitting = ref(false)
const refunds = ref<Refund[]>([])
const createVisible = ref(false)

const searchForm = reactive({
  status: undefined as number | undefined
})

const createForm = reactive({
  trade_no: '',
  amount: '',
  reason: '',
  notify_url: ''
})

const pagination = reactive({
  current: 1,
  pageSize: 20,
  total: 0,
  showPageSize: true,
  showTotal: true
})

const fetchRefunds = async () => {
  loading.value = true
  try {
    const params: any = {
      page: pagination.current,
      page_size: pagination.pageSize
    }
    if (searchForm.status !== undefined) {
      params.status = searchForm.status
    }

    const res = await getRefunds(params)
    if (res.code === 0) {
      refunds.value = res.data.list || []
      pagination.total = res.data.total
    } else {
      Message.error(res.msg || '获取退款列表失败')
    }
  } catch (error) {
    Message.error('获取退款列表失败')
  } finally {
    loading.value = false
  }
}

const handleSearch = () => {
  pagination.current = 1
  fetchRefunds()
}

const handleReset = () => {
  searchForm.status = undefined
  pagination.current = 1
  fetchRefunds()
}

const handlePageChange = (page: number) => {
  pagination.current = page
  fetchRefunds()
}
const handlePageSizeChange = (size: number) => {
  pagination.pageSize = size
  pagination.current = 1
  fetchRefunds()
}

const showCreateModal = () => {
  createForm.trade_no = ''
  createForm.amount = ''
  createForm.reason = ''
  createForm.notify_url = ''
  createVisible.value = true
}

const handleCreateConfirm = async () => {
  if (!createForm.trade_no.trim()) {
    Message.warning('请输入订单号')
    return
  }
  if (!createForm.amount || parseFloat(createForm.amount) <= 0) {
    Message.warning('请输入有效的退款金额')
    return
  }

  submitting.value = true
  try {
    const res = await createRefund({
      trade_no: createForm.trade_no,
      amount: createForm.amount,
      reason: createForm.reason,
      notify_url: createForm.notify_url
    })
    if (res.code === 0) {
      Message.success('退款申请已提交')
      createVisible.value = false
      fetchRefunds()
    } else {
      Message.error(res.msg || '申请退款失败')
    }
  } catch (error) {
    Message.error('申请退款失败')
  } finally {
    submitting.value = false
  }
}

onMounted(() => {
  fetchRefunds()
})
</script>

<style scoped>
.refunds-container {
  padding: 20px;
}

.action-bar {
  margin-bottom: 20px;
}

.search-form {
  margin-bottom: 20px;
}
</style>
