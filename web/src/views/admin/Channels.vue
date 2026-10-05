<!-- web/src/views/admin/Channels.vue -->
<template>
  <div>
    <div class="header-bar">
      <a-button type="primary" @click="handleCreate">
        <template #icon><icon-plus /></template>
        新建通道
      </a-button>
    </div>
    <a-table :data="channels" :loading="loading" :pagination="pagination" @page-change="handlePageChange">
      <template #columns>
        <a-table-column title="ID" data-index="id" :width="60" />
        <a-table-column title="通道名称" data-index="name" :width="150" />
        <a-table-column title="插件" data-index="plugin" :width="100" />
        <a-table-column title="支付类型" data-index="pay_types" :width="150" />
        <a-table-column title="费率" data-index="rate" :width="80">
          <template #cell="{ record }">{{ record.rate }}%</template>
        </a-table-column>
        <a-table-column title="日限额" data-index="daily_limit" :width="120">
          <template #cell="{ record }">¥{{ record.daily_limit }}</template>
        </a-table-column>
        <a-table-column title="排序" data-index="sort" :width="60" />
        <a-table-column title="状态" data-index="status" :width="80">
          <template #cell="{ record }">
            <a-tag :color="record.status === 1 ? 'green' : 'red'">
              {{ record.status === 1 ? '启用' : '禁用' }}
            </a-tag>
          </template>
        </a-table-column>
        <a-table-column title="操作" :width="150">
          <template #cell="{ record }">
            <a-button type="text" size="small" @click="handleEdit(record)">编辑</a-button>
            <a-popconfirm content="确定要删除此通道吗？" @ok="handleDelete(record.id)">
              <a-button type="text" size="small" status="danger">删除</a-button>
            </a-popconfirm>
          </template>
        </a-table-column>
      </template>
    </a-table>

    <a-modal
      v-model:visible="modalVisible"
      :title="isEdit ? '编辑通道' : '新建通道'"
      @ok="handleSubmit"
      :ok-loading="submitting"
      width="700px"
    >
      <a-form :model="form" layout="vertical">
        <a-form-item label="通道名称" required>
          <a-input v-model="form.name" placeholder="如: 支付宝扫码支付" />
        </a-form-item>

        <a-form-item label="支付插件" required>
          <a-select
            v-model="form.plugin"
            :disabled="isEdit"
            placeholder="请选择支付插件"
            @change="handlePluginChange"
          >
            <a-option
              v-for="plugin in plugins"
              :key="plugin.name"
              :value="plugin.name"
            >
              {{ plugin.show_name }}
            </a-option>
          </a-select>
        </a-form-item>

        <a-form-item label="回调地址（可选）">
          <a-input
            v-model="form.callback_url"
            placeholder="留空则自动使用当前访问域名拼接；如需自定义请填写完整地址，如 https://pay.example.com/api/pay/notify/alipay"
          />
        </a-form-item>

        <!-- 动态配置字段 -->
        <template v-if="currentPluginConfig">
          <a-divider>支付配置</a-divider>

          <a-form-item
            v-for="field in currentPluginConfig.inputs"
            :key="field.key"
            :label="field.name"
            :required="field.required"
          >
            <!-- 普通输入框 -->
            <a-input
              v-if="field.type === 'input'"
              v-model="form.config[field.key]"
              :placeholder="field.placeholder"
            />

            <!-- 多行文本框 -->
            <a-textarea
              v-else-if="field.type === 'textarea'"
              v-model="form.config[field.key]"
              :placeholder="field.placeholder"
              :rows="4"
            />

            <!-- 下拉选择 -->
            <a-select
              v-else-if="field.type === 'select'"
              v-model="form.config[field.key]"
              :placeholder="field.placeholder"
            >
              <a-option
                v-for="(label, value) in field.options"
                :key="value"
                :value="value"
              >
                {{ label }}
              </a-option>
            </a-select>

            <template v-if="field.note" #extra>
              <div style="color: #86909c; font-size: 12px">{{ field.note }}</div>
            </template>
          </a-form-item>

          <a-divider>支付接口</a-divider>

          <a-form-item label="支持的支付接口" required>
            <a-checkbox-group v-model="form.app_types">
              <a-checkbox
                v-for="payType in currentPluginConfig.pay_types"
                :key="payType.code"
                :value="payType.code"
              >
                {{ payType.name }}
              </a-checkbox>
            </a-checkbox-group>
          </a-form-item>

          <a-alert v-if="currentPluginConfig.note" type="info" style="margin-bottom: 16px">
            {{ currentPluginConfig.note }}
          </a-alert>
        </template>

        <a-divider>费率设置</a-divider>

        <a-form-item label="费率(%)" required>
          <a-input-number
            v-model="form.rate"
            :precision="4"
            :min="0"
            :max="99.9999"
            placeholder="如: 0.6"
          />
        </a-form-item>

        <a-form-item label="日限额">
          <a-input-number
            v-model="form.daily_limit"
            :precision="2"
            :min="0"
            placeholder="0表示不限制"
          />
        </a-form-item>

        <a-form-item label="排序">
          <a-input-number v-model="form.sort" :min="0" placeholder="数字越小越靠前" />
        </a-form-item>

        <a-form-item label="状态">
          <a-switch v-model="form.status" :checked-value="1" :unchecked-value="0" />
          <span style="margin-left: 8px; color: #86909c">{{ form.status === 1 ? '启用' : '禁用' }}</span>
        </a-form-item>
      </a-form>
    </a-modal>
  </div>
</template>

<script setup lang="ts">
import { ref, reactive, onMounted } from 'vue'
import { Message } from '@arco-design/web-vue'
import { IconPlus } from '@arco-design/web-vue/es/icon'
import {
  getChannels,
  createChannel,
  updateChannel,
  deleteChannel,
  getPlugins,
  getPluginConfig,
} from '@/api/admin'
import type { Channel, PluginConfig } from '@/api/types'

const loading = ref(false)
const submitting = ref(false)
const channels = ref<Channel[]>([])
const pagination = reactive({
  current: 1,
  pageSize: 20,
  total: 0,
})

const modalVisible = ref(false)
const isEdit = ref(false)
const editId = ref(0)

const plugins = ref<{ name: string; show_name: string }[]>([])
const currentPluginConfig = ref<PluginConfig | null>(null)

const defaultForm = {
  name: '',
  plugin: '',
  pay_types: '',
  app_types: [] as string[],
  callback_url: '',
  rate: 0,
  daily_limit: 0,
  sort: 0,
  status: 1,
  config: {} as Record<string, string>,
}

const form = reactive({ ...defaultForm })

const fetchData = async () => {
  loading.value = true
  try {
    const res = await getChannels({ page: pagination.current, page_size: pagination.pageSize })
    channels.value = res.data.list
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

// 加载插件列表
const loadPlugins = async () => {
  try {
    const res = await getPlugins()
    plugins.value = res.data
  } catch (e) {
    // error handled
  }
}

// 插件切换时加载配置模板
const handlePluginChange = async (plugin: string) => {
  try {
    const res = await getPluginConfig(plugin)
    currentPluginConfig.value = res.data

    // 重置配置
    form.config = {}
    form.app_types = []
  } catch (e) {
    Message.error('加载插件配置失败')
  }
}

const handleCreate = () => {
  isEdit.value = false
  Object.assign(form, { ...defaultForm, config: {}, app_types: [] })
  currentPluginConfig.value = null
  modalVisible.value = true
}

const handleEdit = async (record: Channel) => {
  isEdit.value = true
  editId.value = record.id

  // 加载该插件的配置模板
  try {
    const res = await getPluginConfig(record.plugin)
    currentPluginConfig.value = res.data
  } catch (e) {
    currentPluginConfig.value = null
    Message.error('加载插件配置失败')
    return
  }

  form.name = record.name
  form.plugin = record.plugin
  form.pay_types = record.pay_types
  form.app_types = (record.app_type || '').split(',').map(value => value.trim()).filter(value => currentPluginConfig.value?.pay_types.some(option => option.code === value))
  form.callback_url = record.callback_url || ''
  form.rate = Number(record.rate)
  form.daily_limit = Number(record.daily_limit)
  form.sort = record.sort
  form.status = record.status
  form.config = typeof record.config === 'object' ? record.config : {}

  modalVisible.value = true
}

const handleSubmit = async () => {
  if (!form.name || !form.plugin) {
    Message.warning('请填写必填项')
    return
  }

  if (form.app_types.length === 0) {
    Message.warning('请至少选择一个支付接口')
    return
  }

  const data = {
    name: form.name,
    plugin: form.plugin,
    pay_types: form.app_types.join(','),  // 使用选中的支付接口
    app_type: form.app_types.join(','),
    callback_url: form.callback_url,
    rate: form.rate,
    daily_limit: form.daily_limit,
    sort: form.sort,
    status: form.status,
    config: form.config,
  }

  submitting.value = true
  try {
    if (isEdit.value) {
      await updateChannel(editId.value, data)
      Message.success('更新成功')
    } else {
      await createChannel(data)
      Message.success('创建成功')
    }
    modalVisible.value = false
    fetchData()
  } catch (e) {
    // ignore
  } finally {
    submitting.value = false
  }
}

const handleDelete = async (id: number) => {
  try {
    await deleteChannel(id)
    Message.success('删除成功')
    fetchData()
  } catch (e) {
    // ignore
  }
}

onMounted(() => {
  fetchData()
  loadPlugins()
})
</script>

<style scoped>
.header-bar {
  margin-bottom: 16px;
}
</style>
