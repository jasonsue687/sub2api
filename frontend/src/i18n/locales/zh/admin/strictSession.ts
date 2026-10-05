const strictSession = {
        title: '严格会话绑定',
        description: '仅按客户端会话 ID 永久绑定订阅账号。同一会话更换 API Key 后仍使用原绑定。原账号不可用时直接返回错误，由上层决定如何切换分组。保存后生效，无需重启。',
        enabled: '启用严格会话绑定',
        enabledHint: '默认开启。关闭后恢复普通调度，已有会话可能切换账号；绑定记录保留，重新开启后继续使用。',
        retryLimit: '同账号重试上限',
        retryLimitHint: '-1 沿用账号自己的重试次数，0 禁用同账号重试，大于 0 为固定上限。',
        sessionHeader: '附加会话头',
      }

export default strictSession
