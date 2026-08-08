仅持有抽象 + 编排：
  - types.go      ExecRequest / ExecResult / RiskLevel / ValidationResult / Sandbox/SecurityConfig
  - validator.go  静态安全规则（Block / Warn 双级），暴露 PolicySnapshot 给 Constraints 槽位
  - sandbox.go    Sandbox 编排（校验 → 执行 → 审计）+ Executor 接口

具体执行器（Docker / Local / Mock）在 infrastructure/sandbox。
工厂函数 sandboximpl.NewSandbox 按 backend 字符串组装出 Sandbox。
