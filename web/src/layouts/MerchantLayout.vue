<!-- web/src/layouts/MerchantLayout.vue -->
<template>
  <a-layout class="layout">
    <a-layout-sider collapsible v-model:collapsed="collapsed" :width="220">
      <div class="logo">
        <span v-if="!collapsed">EPay 商户中心</span>
        <span v-else>EP</span>
      </div>
      <a-menu
        :selected-keys="[currentRoute]"
        @menu-item-click="handleMenuClick"
      >
        <a-menu-item key="dashboard">
          <template #icon><icon-dashboard /></template>
          仪表盘
        </a-menu-item>
        <a-menu-item key="orders">
          <template #icon><icon-file /></template>
          订单管理
        </a-menu-item>
        <a-menu-item key="refunds">
          <template #icon><icon-undo /></template>
          退款管理
        </a-menu-item>
        <a-menu-item key="settlements">
          <template #icon><icon-swap /></template>
          结算管理
        </a-menu-item>
        <a-menu-item key="records">
          <template #icon><icon-history /></template>
          资金记录
        </a-menu-item>
        <a-menu-item key="profile">
          <template #icon><icon-settings /></template>
          个人信息
        </a-menu-item>
        <a-menu-item key="test-payment">
          <template #icon><icon-check-circle /></template>
          测试支付
        </a-menu-item>
      </a-menu>
    </a-layout-sider>
    <a-layout>
      <a-layout-header class="header">
        <div class="header-right">
          <span class="balance">余额: ¥{{ balance }}</span>
          <a-dropdown>
            <a-button type="text">
              <icon-user /> {{ username }}
            </a-button>
            <template #content>
              <a-doption @click="handleLogout">退出登录</a-doption>
            </template>
          </a-dropdown>
        </div>
      </a-layout-header>
      <a-layout-content class="content">
        <router-view />
      </a-layout-content>
    </a-layout>
  </a-layout>
</template>

<script setup lang="ts">
import { ref, computed, onMounted } from 'vue'
import { useRouter, useRoute } from 'vue-router'
import { getProfile } from '@/api/merchant'

const router = useRouter()
const route = useRoute()
const collapsed = ref(false)
const username = ref('')
const balance = ref('0.00')

const currentRoute = computed(() => {
  const path = route.path.split('/')[2] || 'dashboard'
  return path
})

const handleMenuClick = (key: string) => {
  router.push(`/merchant/${key}`)
}

const handleLogout = () => {
  localStorage.removeItem('merchant_token')
  router.push('/merchant/login')
}

onMounted(async () => {
  try {
    const res = await getProfile()
    username.value = res.data.username
    balance.value = res.data.balance
  } catch (e) {
    // ignore
  }
})
</script>

<style scoped>
.layout {
  min-height: 100vh;
}
.logo {
  height: 64px;
  display: flex;
  align-items: center;
  justify-content: center;
  color: #fff;
  font-size: 18px;
  font-weight: bold;
  background: rgba(255, 255, 255, 0.1);
}
.header {
  background: #fff;
  padding: 0 24px;
  display: flex;
  justify-content: flex-end;
  align-items: center;
  box-shadow: 0 1px 4px rgba(0, 0, 0, 0.1);
}
.header-right {
  display: flex;
  align-items: center;
  gap: 16px;
}
.balance {
  color: #165dff;
  font-weight: 500;
}
.content {
  margin: 24px;
  padding: 24px;
  background: #fff;
  min-height: calc(100vh - 64px - 48px);
}
</style>
