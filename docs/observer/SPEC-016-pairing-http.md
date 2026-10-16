# SPEC-016 实际配对 HTTP 与恢复

2026-10-16；#17/#25、epay-go #6；`observer-pairing-v1` 实施契约。扩展 SPEC-015；验收状态以报告为准。

## 隔离与授权

用户本次长期 Goal 授权隔离 Docker/数据库/HTTPS 服务、Android 模拟器以及 New API 独立测试 checkout。禁止生产数据、真实资金、公网部署、正式 APK 发布和第三方上游推送。历史文档中“不得启动测试服务/修改第三仓库”仅为旧批次边界，不再阻止本 Goal。真实通知自动匹配仍关闭。

服务启动显式固定 environment、source_class、HTTPS origin 与独立 KEK；默认不开启观察器接口。只接受直接 TLS，不信任客户端 Forwarded/X-Forwarded-Proto。普通网关 HTTP 入口不获得配对能力；TLS 入口与已有管理员鉴权共享正常权限验证。KEK 为32字节独立随机材料，通过受限文件加载，与商户/管理员密钥隔离。密文为12字节 nonce +32字节 ciphertext +16字节 tag。AAD 为 JSON 字符串数组 `["observer-credential-v1",key_id,environment,receiver_id,binding_version十进制字符串,device_id,provider,source_class,kek_id]`；原始密文不可移植到不同范围。凭据有效期30天，不延长旧凭据。

管理员创建 grant 固定 receiver、mode（replace/rotate）及当前 generation，5分钟一次消费。replace 新建 device 与 generation，终止旧绑定/凭据；rotate 只允许当前活跃绑定，保留 device/generation，激活新 key 时立即撤销旧 key（v1 无重叠期）。不能用 rotate 重新激活被撤销的绑定。管理员撤销 binding 同时撤销其凭据；撤销 grant 阻止后续兑换/恢复/激活，已激活凭据须另行撤销 binding。

## 设备路由

固定 POST `/api/observer/v1/pairing/inspect`、`/redeem`、`/activate`；无query、尾斜线、编码变体、重定向。单值 `Authorization: ObserverPairing <token>`，token 为32随机字节的43字符 canonical base64url 无padding。单值 `Content-Type: application/json`（允许 charset=utf-8），无Content-Encoding；UTF-8、最多4096字节，拒绝未知/重复字段、null、非法surrogate、尾随内容与非整数版本。

inspect body 精确字段：`{"schema_version":1}`。返回字段：schema_version、grant_id、origin、environment、receiver_id、provider、source_class、mode、expected_binding_version、device_id、expires_at_ms。replace 的 device_id 为 `""`；rotate 返回既有device。用户必须先确认完整范围，不能凭通知内容决定范围。

redeem/activate body 精确字段：schema_version、enrollment_id（小写UUIDv4）、resume_secret（独立32随机字节canonical base64url）。客户端首次发送前以Android Keystore AES-GCM保护 token/resume_secret/inspect快照并原子写入noBackup pending日志。兑换成功创建 pending credential；服务器只保存 token/resume 摘要。redeem 返回 schema_version、enrollment_id、origin、environment、receiver_id、provider、source_class、device_id、binding_version、key_id、key_base64url、not_before_ms、expires_at_ms。所有响应范围须与确认快照相符。

先幂等导入 HMAC KeyStore，再 activate；activate 返回 schema_version、enrollment_id、key_id、state=`active`，不返回key。最后在Room单事务保存完整非秘密范围/凭据元数据并激活采集/队列，清除pending日志。每步崩溃都可从pending日志恢复；已激活后丢响应可再次activate。清日志失败不取消已提交Room激活。旧UNBOUND事件绝不补绑。

同grant+enrollment+resume在原5分钟窗口返回同一密钥/身份，无重建、不延长有效期。恢复/激活仍检查环境、receiver、binding、grant和credential的有效性及撤销状态；不同enrollment/secret为409。窗口失效后needs_repair，经新grant重新配对。数据库提交失败不返回密钥或成功；HTTP响应丢失不能撤销已提交兑换。grant/receiver锁与凭据变更在同事务内；并发兑换只产生一组身份。rotate重试不得恢复被更新key取代的旧key。

## 错误和泄露控制

响应 `Cache-Control: no-store`，错误精确字段 schema_version/code，不含输入、SQL、内部异常或凭据。400 invalid_payload/invalid_route；401 invalid_grant（未知/到期/撤销统一）；403 scope_denied（失效环境/receiver/绑定）；409 enrollment_conflict/binding_changed；413 body_too_large；415 unsupported_media_type；429 rate_limited（Retry-After）；503 unavailable。禁止把HTTP 2xx空体当配对成功。网络失败/5xx保留pending，凭据错误转needs_repair，不在后台静默新建身份。

服务限制同时请求数、总请求速率及每grant一分钟20次持久预算（包括错误恢复）。普通日志不记录Authorization、body、token、secret、key、签名；秘密不得进入URL。无效/陌生token统一错误。限流不是token盗取后的授权防护。

## 验收

- PAIR-01：双方严格JSON与canonical base64向量、范围比较、错误解析。
- PAIR-02：真实PostgreSQL并发兑换/冲突/commit失败/服务重建恢复；密文不能跨scope或KEK解密。
- PAIR-03：正常TLS实际inspect/redeem/activate；拒绝明文、转发头伪装、redirect、未知CA及非预期origin。
- PAIR-04：grant到期/撤销、binding替换、凭据轮换/撤销；旧授权不能上报。
- PAIR-05：Android受保护pending、KeyStore与Room各崩溃边界恢复；旧UNBOUND不补绑、Release隔离。

实现与测试通过前均为Not run；完整#17还须同时满足SPEC-015及实际Android网络验收。
