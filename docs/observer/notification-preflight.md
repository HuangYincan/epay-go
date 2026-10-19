# 真实通知试验只读准入检查

2026-10-19；epay-go #26，承接#24；Android #82 / SPEC-041（epay-observer-android）。增加独立operator CLI，不新增公网接口或启用默认规则。

`InspectNotificationPilot`在REPEATABLE READ READ ONLY事务内核对001–008精确校验和、环境、管理员、指定支付宝receiver/当前binding/device/有效凭据、已核验规则、现有原订单/通道/商户与截止、历史同额（包括软删除/已付）、旧证据、alias、永久pilot占用和暂停。只输出`ready`与稳定阻塞编号，不输出原通知、账号、凭据、订单号或设备标识。不迁移、不调用主动支付查询、不登记pilot、不发回调、不改余额。失败丢弃部分报告。

本机可用已有Go容器构建，无需全局安装工具：

```sh
go build -mod=readonly -o /tmp/observer-pilot-preflight ./cmd/observer-pilot-preflight
# EPAY_PREFLIGHT_DSN由私有环境/secret提供；禁止写入命令历史或公开报告。
/tmp/observer-pilot-preflight -environment '<existing-environment>' \
  -receiver '<specified-receiver>' -parser '<verified-parser-version>' \
  -order-id '<original-order-id>' -actor-id '<operator-id>' \
  -expires-at-ms '<explicit-unix-ms>'
```

退出0表示本次快照条件满足；1表示阻塞；2表示输入/存储/输出错误。快照没有保留金额，也没有取得收款确认权限；原Arm事务和首次认证接收始终重新核验。独立检查不需要解密设备密钥。当前实际真实模板/聊天排除/身份与归属风险政策仍未核验，不得把测试规则或readiness输出当作实际支付宝通过。

## 验证

- 使用既有golang:1.26.9与独立postgres:16、独立network/DB，没有公开端口和生产数据。`go test -mod=readonly -race ./internal/observerstore -run 'TestNotificationPreflight|TestNotificationPilot' -count=1`通过；新增3个顶层检查测试含13类阻塞子项。测试通过driver连接确认每个只读查询均在REPEATABLE READ READ ONLY事务中，业务表无写入，检查成功后仍须另行Arm。
- 覆盖缺schema、含点parser版本、不完整/失效条件、软删除同额历史、旧事件、已有pilot、暂停及取消context，不把部分输出当ready。测试首次失败是合成撤销记录未满足revoked_at约束，修正测试数据后通过，不放松数据库约束。
- 相关vet及linux/amd64静态CLI构建通过。实际现网只读执行返回`ready=false/schema_not_ready`，符合当前仍运行旧版本的事实；未迁移、未启用、未修改业务记录，临时远端工具删除。本批真实支付验收Not run。
