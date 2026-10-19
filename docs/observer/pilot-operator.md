# 本地受控试验operator

2026-10-19；#28 / Android #82，SPEC-043，承接SPEC-036/041。补齐实际登记与Android私有handoff；不增加公网接口，不部署，不批准任何实际规则。当前真实模板、聊天边界、身份/金额政策及线上New API契约仍不完整，**不得在线apply**。

## 使用

构建`./cmd/observer-pilot-operator`。`EPAY_OPERATOR_DSN`仅从私有环境提供，不写入公开命令/日志。输入是最多4096字节的严格平面JSON：`schema_version=1`、origin/environment/receiver_id、现有order_id/actor_id、显式expires_at_ms、固定parser_version、非秘密evidence_ref，以及五个0/1事实（template_verified、chat_boundary_verified、identity_policy_approved、amount_policy_approved、newapi_contract_verified）。缺项/重复/未知/非整数拒绝。五个事实须来自独立审查，不能复制测试向量代替审批；当前任何缺项保持0。

默认 `-input <private-review.json>` 只读检查，补review和配对origin检查，不迁移/取密钥/写规则/调用支付接口。结果是咨询快照，不取得登记权。

仅在事实已核验时，显式增加 `-apply -output <new-private-handoff.json>`。输出父目录须由operator私有管理；文件独占创建0600，不覆盖旧文件或追随最终路径symlink。服务端登记事务锁environment/receiver，复查当前有效配对origin、管理员、暂停和不可变rule批准引用，再调用原Arm/数据库约束。规则与pilot共同提交或回滚；不确认订单、不记账、不发回调。

输出恰为Android严格codec字段，无订单提示、密钥或正文。第一次输出失败时登记可能已提交：退出3/`pilot_committed_handoff_unavailable`不能解释为零写入。相同输入、同一批准人/规则/截止，在原窗口未过期且当前绑定/原订单仍满足条件时，可重试输出到新文件，只取回原armed_at/expires_at；不延长或重登记。已存在的输出在数据库操作前拒绝。

退出：0检查ready或apply/恢复成功；1只读阻塞；2输入/检查/登记失败；3提交后输出/汇总不可用。标准输出仅固定汇总，不含身份、订单或密钥。失败日志不打印原始数据库错误/配置内容。

## 验证与边界

恢复路径在receiver锁内复用完整准入条件；有四类可控交错回归，在咨询检查后禁用设备/删除订单/停用通道或商户，均拒绝旧handoff。公共preflight仍严格只读。

新增严格输入、缺事实、只读、origin、事务回滚、并发同范围恢复、不可延长/改引用、实际CLI的0600/独占/symlink/默认无写入及恢复测试，全部使用隔离PostgreSQL和合成数据。实际CLI binary是默认构建，测试不会操作真实网关。共享`pilot-operator-v1/handoff.json`只为合成协议向量，两端必须一致。

首个实际CLI测试失败源于测试子进程未进入隔离schema（pgx.ConnString不序列化RuntimeParams变更），已改为仅测试进程PGOPTIONS并校验schema名，原失败日志保留；未放宽产品约束。测试通过不证明真实支付宝或New API增额，线上apply尚未运行。
