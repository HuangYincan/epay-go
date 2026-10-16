# SPEC-024 受限本地合成回调链路

2026-10-16；Android #27/#29，epay-go #8/#9。仅长期Goal隔离原型；默认safehttp不改动，真实来源自动确认继续关闭。

## 范围和传输

- 本地适配器仅编译于`observersynthetic`，并要求既有显式`OBSERVER_SYNTHETIC_ENVIRONMENT`。默认构建不读取本地回调配置，继续使用safehttp。
- 显式配置`OBSERVER_SYNTHETIC_CALLBACK_URL`和`OBSERVER_SYNTHETIC_CALLBACK_CA_FILE`；URL必须是HTTPS、数值环回IP、显式非特权端口和唯一固定路径`/api/user/epay/notify`，不得有用户信息、query、fragment或编码路径变体。CA只来自任务私有文件。禁止DNS、代理、跳转、HTTP、InsecureSkipVerify或系统范围信任修改。
- URL必须与订单原notify_url逐字相同；不替换客户保存的地址。拨号固定原IP/端口，TLS仍验证服务端证书链与IP SAN。只为该URL创建独立client，其余路径继续默认safehttp。
- 创建普通测试订单时，在已锁定和验证的synthetic环境/receiver/测试merchant范围内验证该唯一URL；无observer alias、其他environment或非白名单merchant不能获得内网访问。
- 投递前重新从持久订单和claim/consumption/credited event确认synthetic scope，当前环境/receiver/测试merchant仍enabled且merchant有效。无已提交观察事件收入的普通订单不得借用本地传输。配对轮换不改变已完成收入的历史归属；停止测试环境可暂停投递，保留失败/重试状态。

## 原有持久回调

- 继续使用订单的持久租约、token、尝试次数和退避。参数使用数据库原订单金额、out_trade_no、provider与**当前商户key**签名；不缓存旧key，不以通知内容提供这些参数。
- 单次响应必须是2xx且有界body等于success（既有trim/case语义）。失败、连接中断、TLS错误、重定向都不能记成功。
- 支付/收入事务只写pending；发送失败只重试通知。重启回收过期租约；失去租约的晚结果不能覆盖新结果。成功后普通worker不重发；显式管理员resend仍保留。
- Key轮换期间New API必须配置一致的新key；旧key回调失败不能增加余额，重试使用新key。没有跨库原子提交，依靠至少一次投递和New API原单幂等。

## 验收

- CALLBACK-01：默认构建拒绝相同环回配置；专用构建仅许可精确URL和已授权scope。变端口/路径/IP、HTTP、query/fragment、普通订单、错环境/禁用scope全部失败关闭。
- CALLBACK-02：实际TLS成功，错CA/IP SAN、HTTP、重定向均失败；默认safehttp网络/解析/重定向安全回归通过。
- CALLBACK-03：普通测试订单→真实匹配/收入→持久通知→New API实际余额一次；两provider同环境，无通知订单提示。进程退出、回调失败/响应丢失/重放/当前key轮换可恢复。
- CALLBACK-04：部署与客户端配置仅任务资源，干净数据可复现，保留版本/脱敏结果；无真实App/资金/公网结论。
