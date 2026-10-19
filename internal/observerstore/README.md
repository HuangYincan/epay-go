# Observer identity and evidence storage

关联epay-go #6、epay-observer-android #15，SPEC-014 / MODEL-01..04。规范术语见docs/observer/CONTEXT.md。

`Migrate`在既有database.Migrate之后新增版本化SQL表，空环境默认禁用。独立事务、schema级advisory锁、脚本SHA256；未知版本/脚本漂移/表名碰撞失败，不修复或清数据。后续变化新增迁移，不能编辑已部署的001脚本。旧AutoMigrate仍是原机制，不宣称整个历史迁移原子。代码回退必须先核对迁移器支持版本：schema6裸旧构建拒绝schema8。本批零rule/pilot休眠升级已准备[已知schema8的旧业务兼容构建](../../docs/observer/schema-upgrade.md)，存在试验历史时不适用；保留当前DB，不提供删除历史的down migration。

receiver是稳定账户代号，channel/provider别名唯一并必须是epay插件；device属于环境，binding代次保留历史且不复活。credential只存固定格式的密文槽和授权元数据，密文加解密/发放不在本包。账户代号由运营登记，不能自动证明两个不同代号不是同一真实账户。

`RecordEvidence`是存储原语，要求已验证请求，重新解析原body并核对不可变credential范围。返回stored/duplicate/conflict，**不返回接收ACK**。有效性/撤销/限流、持久请求重放登记及HTTP接收事务尚未实现；不可把此函数直接挂为events API。后续#20在同一个外层事务中补齐这些条件；传入事务句柄时GORM内部事务为savepoint，最终提交仍由外层控制；自建事务显式使用READ COMMITTED，不依赖数据库默认值。接收器的外层事务也应选择READ COMMITTED并在最终提交后才ACK。

event原字节/金额/首次时间/首次key和request冻结；冲突单独保留摘要。provider_transaction_id缺失为NULL，不写入旧订单api_trade_no。没有匹配、入账或商户回调。

测试沿用既有CI的隔离PostgreSQL；没有DSN时本机skip，CI缺少DSN则失败。宿主不安装Go或部署服务。最终结果与固定提交由PR及观察器验证报告记录。SQL参数日志在存储边界关闭，外部仅收到固定错误类别。
