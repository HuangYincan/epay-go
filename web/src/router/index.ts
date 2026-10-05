// web/src/router/index.ts
import { createRouter, createWebHistory, type RouteRecordRaw } from 'vue-router'

const routes: RouteRecordRaw[] = [
  // 首页重定向
  {
    path: '/',
    redirect: '/merchant/login',
  },

  // 管理后台
  {
    path: '/admin/login',
    name: 'AdminLogin',
    component: () => import('@/views/admin/Login.vue'),
    meta: { title: '管理员登录' },
  },
  {
    path: '/admin',
    component: () => import('@/layouts/AdminLayout.vue'),
    meta: { requiresAuth: true, authType: 'admin' },
    children: [
      {
        path: '',
        redirect: '/admin/dashboard',
      },
      {
        path: 'dashboard',
        name: 'AdminDashboard',
        component: () => import('@/views/admin/Dashboard.vue'),
        meta: { title: '仪表盘' },
      },
      {
        path: 'merchants',
        name: 'AdminMerchants',
        component: () => import('@/views/admin/Merchants.vue'),
        meta: { title: '商户管理' },
      },
      {
        path: 'orders',
        name: 'AdminOrders',
        component: () => import('@/views/admin/Orders.vue'),
        meta: { title: '订单管理' },
      },
      {
        path: 'channels',
        name: 'AdminChannels',
        component: () => import('@/views/admin/Channels.vue'),
        meta: { title: '通道管理' },
      },
      {
        path: 'settlements',
        name: 'AdminSettlements',
        component: () => import('@/views/admin/Settlements.vue'),
        meta: { title: '结算管理' },
      },
      {
        path: 'refunds',
        name: 'AdminRefunds',
        component: () => import('@/views/admin/Refunds.vue'),
        meta: { title: '退款管理' },
      },
      {
        path: 'test-payment',
        name: 'AdminTestPayment',
        component: () => import('@/views/admin/TestPayment.vue'),
        meta: { title: '测试支付' },
      },
    ],
  },

  // 商户中心
  {
    path: '/merchant/login',
    name: 'MerchantLogin',
    component: () => import('@/views/merchant/Login.vue'),
    meta: { title: '商户登录' },
  },
  {
    path: '/merchant/register',
    name: 'MerchantRegister',
    component: () => import('@/views/merchant/Register.vue'),
    meta: { title: '商户注册' },
  },
  {
    path: '/merchant',
    component: () => import('@/layouts/MerchantLayout.vue'),
    meta: { requiresAuth: true, authType: 'merchant' },
    children: [
      {
        path: '',
        redirect: '/merchant/dashboard',
      },
      {
        path: 'dashboard',
        name: 'MerchantDashboard',
        component: () => import('@/views/merchant/Dashboard.vue'),
        meta: { title: '仪表盘' },
      },
      {
        path: 'orders',
        name: 'MerchantOrders',
        component: () => import('@/views/merchant/Orders.vue'),
        meta: { title: '订单管理' },
      },
      {
        path: 'settlements',
        name: 'MerchantSettlements',
        component: () => import('@/views/merchant/Settlements.vue'),
        meta: { title: '结算管理' },
      },
      {
        path: 'records',
        name: 'MerchantRecords',
        component: () => import('@/views/merchant/Records.vue'),
        meta: { title: '资金记录' },
      },
      {
        path: 'profile',
        name: 'MerchantProfile',
        component: () => import('@/views/merchant/Profile.vue'),
        meta: { title: '个人信息' },
      },
      {
        path: 'refunds',
        name: 'MerchantRefunds',
        component: () => import('@/views/merchant/Refunds.vue'),
        meta: { title: '退款管理' },
      },
      {
        path: 'test-payment',
        name: 'MerchantTestPayment',
        component: () => import('@/views/merchant/TestPayment.vue'),
        meta: { title: '测试支付' },
      },
    ],
  },

  // 收银台
  {
    path: '/cashier/:tradeNo',
    name: 'Cashier',
    component: () => import('@/views/Cashier.vue'),
    meta: { title: '收银台' },
  },

  // 404
  {
    path: '/:pathMatch(.*)*',
    name: 'NotFound',
    component: () => import('@/views/NotFound.vue'),
  },
]

const router = createRouter({
  history: createWebHistory(),
  routes,
})

// 路由守卫
router.beforeEach((to, _from, next) => {
  // 设置页面标题
  document.title = (to.meta.title as string) || 'EPay'

  if (to.meta.requiresAuth) {
    const authType = to.meta.authType as string
    const tokenKey = authType === 'admin' ? 'admin_token' : 'merchant_token'
    const token = localStorage.getItem(tokenKey)

    if (!token) {
      const loginPath = authType === 'admin' ? '/admin/login' : '/merchant/login'
      next({ path: loginPath, query: { redirect: to.fullPath } })
      return
    }
  }

  next()
})

export default router
