export default {
  fingerprints: {
    title: '账号身份指纹',
    description: '登记 Anthropic 请求携带的身份指纹，按相同指纹累计次数，并保留从现有账号缓存导入的身份。',
    stagedNotice: '当前仅登记身份和保存绑定关系。绑定尚未用于发送请求，现有指纹选择方式保持不变。',
    importCache: '导入现有缓存身份',
    importResult: '已导入或确认 {imported} 个账号；{missing} 个无缓存；{failed} 个读取失败。重复导入不会重复建档。',
    search: '搜索指纹 ID、UA 或设备标识',
    searchAccount: '搜索订阅账号名称或 ID',
    allSources: '全部来源', source: '来源', request: '入站请求', cache: '账号缓存',
    identity: '身份指纹', device: '设备标识', details: '查看完整指纹',
    incoming: '请求实际携带的身份字段（登记指纹复用现有校验与默认值逻辑）',
    origin_client: '设备标识来自客户端', origin_generated: '客户端未提供设备标识；使用原有逻辑生成登记标识', origin_cache: '保留现有缓存设备标识',
    observations: '登记记录', count: '{count} 次入站请求', firstSeen: '首次登记', lastSeen: '最近登记',
    boundAccounts: '已绑定订阅账号', unbound: '未绑定', bindAccount: '绑定订阅账号',
    bindingAction: '身份绑定', bindingTitle: '订阅账号身份绑定', account: '订阅账号',
    selectAccount: '请选择订阅账号', currentBinding: '当前绑定', selectedFingerprint: '要绑定的身份指纹',
    empty: '暂无身份指纹，可导入现有缓存身份；新的请求也会自动登记。',
    loadFailed: '加载失败，请重试。', saveFailed: '保存绑定失败，请刷新后重试。',
    importFailed: '缓存导入未完成，已导入的记录会保留，可重新执行。'
  }
}
