# 真实来源单订单结算开发验证

2026-10-18；epay-go #24 / Android #82，SPEC-036 LIVE-01..03。**所有事件输入均为合成数据，包括使用notification枚举的输入；没有实际支付宝收款或线上New API充值。**

## 实现

新增008迁移和`notification-pilot-v1`。001–007未修改。规则表默认空，不提供规则启用或试验登记的公网路由。仅服务端operator原语可登记既有未付订单，数据库独立检查notification环境、指定支付宝receiver/当前设备代次/有效凭据、EPay通道、商户、已打开的静态码收银台、非空商户回调、规则、金额全部历史和显式截止。

首次正常认证接收在既有外层事务与receiver锁内完成快照、资格判断、收入、唯一消费和持久待回调。ACK在提交后返回。无需合成alias/claim；旧MD5回调不能确认试验订单。登记、订单插入/金额变更、alias登记共同序列化同额范围，试验金额及历史永久保留。二维码、回调地址和订单身份在登记后固定；原静态码没有checkout截止时可使用试验显式截止。

## 验证结果

- 默认构建相关PostgreSQL回归通过：`go test ./internal/observerstore ./internal/observerhttp ./internal/service ./internal/accounting`。
- 默认构建race回归通过：`go test -race ./internal/observerstore ./internal/service`。最终静态码/回调固定修改后，9个新顶层测试及34个子用例通过`go test -race ./internal/observerstore -run TestNotificationPilot`。
- 合成构建相关race回归通过：`go test -race -tags observersynthetic ./internal/observerstore ./internal/service`。最终变更后全量CI另核验，不以本地旧代码结果替代。
- 相关`go vet`、默认网关构建通过。宿主未安装Go，使用已有`golang:1.26.9`/`postgres:16`，独立网络/数据库，无公开端口或生产数据。第一次离线构建因两个固定依赖未缓存而失败，后续仅下载go.mod/go.sum已有版本，`-mod=readonly`构建成功。
- 12并发签名重复仅一次收入/消费；第二事件不能再确认。覆盖未知规则、不确定身份、伪造平台引用、暂停、禁用、跨认证范围、过期/迟到/未来时间、删除、全部同额历史、既有证据、旧review/恢复不追溯、6轮登记与同额下单竞争。提交故障返回503且事件/请求/快照/账务全部回滚，正常重试成功。
- 数据库拒绝删改试验历史、延长截止、改规则核验引用、改金额/费率、无消费直接设已付及金额复用。旧合成数据库结算约束保留，真实策略使用独立约束分支。

## 尚未验收

LIVE-04真实收款模板缺样本；Android生产SourcePolicy/ParserRegistry/OutboundPolicy仍关闭。LIVE-05实际New API下单/用户付款/平台通知/实际回调与前后余额Not run。既有New API不是M3隔离补丁版本，单独只读核对保存在本机私有证据，不将隔离测试结论套在线上。未部署、未登记线上试验、未改变通道或实际订单/余额。Proposed金额归属政策在启用前仍须明确；不关闭#8/#33/#42。
