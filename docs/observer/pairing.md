# Observer pairing v1

关联 epay-go #6、epay-observer-android #17/#25；契约为同目录 SPEC-016。仅配对/激活/撤销/轮换，不开放 events，不确认订单。默认配置完全不启动此监听器；普通 HTTP router 不注册这些接口。

启用必须同时配置 `.env.example` 中全部 OBSERVER_* 字段。独立 KEK 文件只含32字节密钥的canonical base64url，文件权限0600，不能使用商户密钥/JWT/notify_key；私钥及KEK只通过私有文件挂载。配置不完整、KEK权限不符或TLS证书错误会拒绝启动；TLS最低1.2，无明文fallback，不信任Forwarded头。origin必须与客户端使用的精确HTTPS主机/端口一致。

环境与receiver由运营预先登记且显式enabled；服务固定environment/source_class，不接受设备自报范围。管理员可通过本TLS入口 `/api/admin/auth/login` 登录，再用正常管理员Bearer JWT访问：

| POST | JSON字段（均含schema_version=1） |
| --- | --- |
| `/api/admin/observer/grants` | receiver_id、mode（replace或rotate） |
| `/api/admin/observer/grants/revoke` | grant_id |
| `/api/admin/observer/bindings/revoke` | receiver_id、binding_version |

create返回scope及短期token，其他接口不返回凭据。设备仅取得这个token，不能取得管理员JWT。grant五分钟、单次enrollment、resume摘要恢复；rotate仍需管理员创建受限grant，APK要求本机已经具有匹配的活跃绑定，不提供无管理员授权的自助轮换接口。replace创建新device/generation，rotate保留范围，新key激活即撤销旧key。撤销grant不等于撤销已激活binding。

每次兑换先持久化pending credential，再由客户端确认安全存储后activate。AES-GCM AAD绑定完整不可变身份，密文60字节。当前KEK不可原位重封装，运维轮换需另行实现，不把设备key轮换冒充KEK轮换。

测试：`EPAY_TEST_DATABASE_DSN=... go test -race ./...`，必须指向专用测试库。测试自动创建并删除schema，覆盖迁移、并发、失效授权、持久限流、响应丢失、提交失败以及实际TLS路由。`observeravd`标签只编译临时AVD桥接测试，不能进入server二进制；步骤和跨库报告见观察器 `docs/testing/M2-pairing-report.md`。不上传任何生成的grant、私钥、KEK或数据库凭据。

跨库记录：[epay-go PR #14](https://github.com/HuangYincan/epay-go/pull/14)、[Android PR #50](https://github.com/HuangYincan/epay-observer-android/pull/50)。网关代码f961159/Android代码9b74816；Go完整CI run38850098132通过，Android的28+8 AVD及双渠道实际HTTPS/负例证据位于配套报告。后续只更新文档时仍核对PR最终head checks，不把此配对子集当成整个#6完成。
