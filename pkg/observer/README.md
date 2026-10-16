# Observer v1 protocol core

关联 HuangYincan/epay-go#6 与 HuangYincan/epay-observer-android#20/#25；SPEC-002/013。

只验证固定POST路由、单值headers、精确body HMAC、时间窗口和严格JSON。`VerifiedRequest`不是持久ACK，不校验数据库授权/撤销/环境，也不做重放登记、事件幂等、订单匹配或记账。尚未注册到任何HTTP路由。后续接收事务需要全部补齐后才能发送202/200回执。

黄金向量来自观察器仓库原有 `docs/testing/protocol-v1/vectors.json`，本批采用 `observer-events-v1-rc1`。这里复制的是本项目合成测试数据，不是第三方实现；key=00..1f为公开夹具，不是服务凭据。双方字节必须同步，不以重新计算新向量掩盖不一致。

宿主未找到Go工具链，未安装额外工具；使用仓库既有GitHub CI（Go1.26.8、race、vet、build和隔离PostgreSQL测试）验证。没有启动或部署应用服务。测试结果以PR最终head为准；数据库接收/反重放/支付行为仍Not run。
