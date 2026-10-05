<template>
  <div class="refunds-container">
    <a-card title="退款管理">
      <!-- 搜索表单 -->
      <a-form :model="searchForm" layout="inline" class="search-form">
        <a-form-item label="商户ID">
          <a-input v-model="searchForm.merchant_id" placeholder="请输入商户ID" style="width: 200px" />
        </a-form-item>
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
      <a-table
        :data="refunds"
        :loading="loading"
        :pagination="pagination"
        @page-change="handlePageChange"
        @page-size-change="handlePageSizeChange"
        row-key="id"
      >
        <template #columns>
          <a-table-column title="退款单号" data-index="refund_no" />
          <a-table-column title="订单号" data-index="trade_no" />
          <a-table-column title="商户ID" data-index="merchant_id" />
          <a-table-column title="退款金额" data-index="amount" />
          <a-table-column title="退款原因" data-index="reason" ellipsis />
          <a-table-column title="状态" :width="100">
            <template #cell="{ record }">
              <a-tag v-if="record.status === 0" color="orange">待处理</a-tag>
              <a-tag v-else-if="record.status === 1" color="green">成功</a-tag>
              <a-tag v-else-if="record.status === 2" color="red">失败</a-tag>
              <a-tag v-else-if="record.status === 3" color="blue">处理中</a-tag>
            </template>
          </a-table-column>
          <a-table-column title="失败原因" data-index="fail_reason" ellipsis />
          <a-table-column title="创建时间" data-index="created_at" :width="180" />
          <a-table-column title="操作" :width="150">
            <template #cell="{ record }">
              <template v-if="record.status === 0">
                <a-button type="text" size="small" @click="handleProcess(record, true)">
                  通过
                </a-button>
                <a-button type="text" size="small" danger @click="handleProcess(record, false)">
                  驳回
                </a-button>
              </template>
              <span v-else>-</span>
            </template>
          </a-table-column>
        </template>
      </a-table>
    </a-card>

    <!-- 驳回对话框 -->
    <a-modal
      v-model:visible="rejectVisible"
      title="驳回退款"
      @ok="handleRejectConfirm"
      @cancel="rejectVisible = false"
    >
      <a-form :model="rejectForm" layout="vertical">
        <a-form-item label="驳回原因" required>
          <a-textarea
            v-model="rejectForm.fail_reason"
            placeholder="请输入驳回原因"
            :rows="4"
          />
        </a-form-item>
      </a-form>
    </a-modal>
  </div>
</template>

<script setup lang="ts">
import { ref, reactive, onMounted } from 'vue'
import { Message } from '@arco-design/web-vue'
import { getRefunds, processRefund } from '@/api/admin'
import type { Refund } from '@/api/types'

const loading = ref(false)
const refunds = ref<Refund[]>([])
const rejectVisible = ref(false)
const currentRefund = ref<Refund | null>(null)

const searchForm = reactive({
  merchant_id: '',
  status: undefined as number | undefined
})

const rejectForm = reactive({
  fail_reason: ''
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
    if (searchForm.merchant_id) {
      params.merchant_id = searchForm.merchant_id
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
  searchForm.merchant_id = ''
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

const handleProcess = (record: Refund, success: boolean) => {
  if (success) {
    // 通过退款
    processRefundRequest(record.refund_no, true, '')
  } else {
    // 驳回退款
    currentRefund.value = record
    rejectForm.fail_reason = ''
    rejectVisible.value = true
  }
}

const handleRejectConfirm = () => {
  if (!rejectForm.fail_reason.trim()) {
    Message.warning('请输入驳回原因')
    return
  }
  if (currentRefund.value) {
    processRefundRequest(currentRefund.value.refund_no, false, rejectForm.fail_reason)
  }
}

const processRefundRequest = async (refundNo: string, success: boolean, failReason: string) => {
  try {
    const res = await processRefund(refundNo, { success, fail_reason: failReason })
    if (res.code === 0) {
      Message.success(success ? '退款已通过' : '退款已驳回')
      rejectVisible.value = false
      fetchRefunds()
    } else {
      Message.error(res.msg || '操作失败')
    }
  } catch (error) {
    Message.error('操作失败')
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

.search-form {
  margin-bottom: 20px;
}
</style>
