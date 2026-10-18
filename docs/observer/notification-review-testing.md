# notification首次接收诊断验证

2026-10-18；Android #77 / epay-go #22；SPEC-033 REVIEW-01..04，FR-03/04/05/07。

## 交付行为与限制

网关首次认证接收notification事件时，在原外层事务内冻结候选、保存`notification-review-v1`诊断并返回`needs_review`；避免默认构建缺少合成matcher而永久pending。结果解释零候选、同额多个候选、历史证据、时钟、截止、暂停或尚未启用确认。候选不是已消费订单；重放/暂停恢复不能重新判断历史。

数据库新迁移007禁止review策略matched，并核对新决定与来源一致；不能产生consumption/收入或触发商户回调。不编辑001–006、不回填旧事件、不启用synthetic政策。Android真实来源/解析/上传仍关闭；未配置线上alias、未部署网关或APK、未创建生产订单或付款。普通个人码仍由用户手填金额，本批不更改订单金额。

当前线上alias数量为0的既有核对不等于新版本真实部署结果。现有alias/claim仍为合成下单机制，不能为了获得候选而给生产通道直接添加alias；真实通道关联和生产结算仍需后续实现。

## 本地验证

使用现有固定`golang:1.26.9`与`postgres:16`镜像，新建独立Docker网络和临时数据库，无宿主开放端口；测试各自独立schema，不接原隔离lab或生产数据库。所有事件均为合成输入，notification枚举与真实TLS/签名/数据库测试不能升级成真实支付宝兼容性。

| 验收 | 证据 |
| --- | --- |
| REVIEW-01 | 11个原因分支、两个provider、正常设备配对和真实TLS签名HTTP路由通过；ACK为needs_review，候选字段与已消费字段分开，零收入/消费/claim/回调调度 |
| REVIEW-02 | 8个并发新封套重复只保留一份事件/快照/决定；同毫秒后建订单不能升级零候选；新事件识别早先同额证据；写决定失败时事件/请求/快照/预算全部回滚，随后正常重试通过；暂停恢复不重判 |
| REVIEW-03 | default构建新测试通过，受影响observerstore/observerhttp/service/database回归通过；tagged回归发现旧负向测试只接受泛化evidence_insufficient，已改为明确断言review策略＋unmatched/no_candidate并保留零收入；受影响新测试和该旧负向测试重跑通过。实际001–006前驱升级到007及重入、数据库专门的no-credit约束/来源触发器拒绝、旧pending不回填通过 |
| REVIEW-04 | 既有管理追踪不输出正文/原body/密钥/平台流水；SPEC-032实际窗口单独补录：0回调/0样本、EXPIRED/120423ms、清理和原四笔审计通过；不重跑窗口或旧生命周期 |

本地命令（在独立测试容器内，DSN指向临时测试库）：

```sh
go test -race ./internal/observerstore -run TestNotificationReview -count=1 -v
go test -race -p 2 ./internal/observerstore ./internal/observerhttp ./internal/service ./internal/database
go test -race -tags observersynthetic -p 2 ./internal/observerstore ./internal/service
go test -race -tags observersynthetic ./internal/observerstore -run 'TestNotificationReview|TestMatchNotificationSourceNeverQualifiesEvenInTaggedBuild' -count=1 -v
```

第三条首次执行的旧预期失败保留在私有`synthetic-regression.log`，不覆盖为成功；修正后的受影响测试记录在`final-changed-tests.log`。其他私有日志为同目录`default-tests.log`，均位于`/private/tmp/observer-notification-review-20261018/`。最终全量CI及合并状态以双方PR为准。

**真实收款解析、订单归属、New API自动充值：本批未验收。** 当前缺少真实收款通知模板与生产匹配政策，#8/#33/#42继续开放。已消费的扫码窗口标记保留；没有付款确认只表示缺少用户反馈，不证明未付款或通知漏投。不得把本报告描述为完整真实充值链路已接通。
