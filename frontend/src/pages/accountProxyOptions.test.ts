import { buildAccountProxyOptions } from './accountProxyOptions'

describe('buildAccountProxyOptions', () => {
  it('includes clear binding and bound account counts in labels', () => {
    const options = buildAccountProxyOptions([
      {
        id: 1,
        url: 'socks5://10.0.0.2:10001',
        label: '新加坡-01',
        enabled: true,
        created_at: '2026-03-27T00:00:00Z',
        test_ip: '',
        test_location: '',
        test_latency_ms: 0,
        bound_accounts: 12,
      },
      {
        id: 2,
        url: 'socks5://10.0.0.2:10002',
        label: '',
        enabled: true,
        created_at: '2026-03-27T00:00:00Z',
        test_ip: '',
        test_location: '',
        test_latency_ms: 0,
        bound_accounts: 0,
      },
    ], {
      clearBinding: '清空代理绑定',
      unlabeled: '未命名',
      boundAccounts: (count) => `已绑定 ${count} 个账号`,
    })

    expect(options).toEqual([
      { value: '', label: '清空代理绑定' },
      { value: 'socks5://10.0.0.2:10001', label: '新加坡-01 · socks5://10.0.0.2:10001 · 已绑定 12 个账号' },
      { value: 'socks5://10.0.0.2:10002', label: '未命名 · socks5://10.0.0.2:10002 · 已绑定 0 个账号' },
    ])
  })
})
