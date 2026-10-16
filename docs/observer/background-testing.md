# 隔离后台恢复测试桥

关联epay-go #6、HuangYincan/epay-observer-android#23及SPEC-018。仅observeravd标签测试，生产server没有新行为。

OBSERVER_AVD_MODE=background在独立schema中运行真实配对/事件接收，私有目录原子写脱敏events/requests计数，供宿主在启动verify instrumentation前确认自动上传。首次有效ACK响应体故意丢失，真实事件事务仍提交；最终10事件/11请求，双provider、平台交易号缺失、payment_verified=false。

实际过程/重启/强停/Doze四场景和测试准备的周期-only遗漏扫描已通过；Android38+8 AVD、JVM27/141及严格APK检查。fixture-only NetworkRequest用于无系统公网验证的隔离AVD，TLS/签名未放宽；不能宣称正式构建或真实支付通过。复现、产物hash及完整证据见Android仓库docs/testing/M2-background-report.md与scripts/background_tls_avd.py。

跨库：[Android PR #52](https://github.com/HuangYincan/epay-observer-android/pull/52)、[gateway PR #16](https://github.com/HuangYincan/epay-go/pull/16)。Android41e5ba9/f8f47e4、gateway184887b/2b8350d；最终head CI见各PR Checks。
