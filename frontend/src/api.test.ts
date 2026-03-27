import { api, AUTH_REQUIRED_EVENT, getAdminKey, setAdminKey } from './api'

describe('api auth handling', () => {
  it('clears stale admin key and emits auth event on 429', async () => {
    const listener = vi.fn()
    window.addEventListener(AUTH_REQUIRED_EVENT, listener as EventListener)
    setAdminKey('stale-key')

    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(
      new Response(JSON.stringify({ error: '密码错误次数过多，IP 已被封禁 5 分钟，请稍后再试' }), {
        status: 429,
        headers: { 'Content-Type': 'application/json' },
      }),
    ))

    await expect(api.getStats()).rejects.toThrow('密码错误次数过多，IP 已被封禁 5 分钟，请稍后再试')

    expect(getAdminKey()).toBe('')
    expect(listener).toHaveBeenCalledTimes(1)
    const event = listener.mock.calls[0][0] as CustomEvent<{ status: number; message: string }>
    expect(event.detail).toEqual({
      status: 429,
      message: '密码错误次数过多，IP 已被封禁 5 分钟，请稍后再试',
    })

    window.removeEventListener(AUTH_REQUIRED_EVENT, listener as EventListener)
  })

  it('sends batch proxy assignment payload for selected accounts', async () => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(
      new Response(JSON.stringify({ message: 'ok', updated: 2 }), {
        status: 200,
        headers: { 'Content-Type': 'application/json' },
      }),
    ))

    await expect(api.batchAssignAccountsProxy([11, 22], 'socks5://10.0.0.2:10005')).resolves.toEqual({
      message: 'ok',
      updated: 2,
    })

    expect(fetch).toHaveBeenCalledWith('/api/admin/accounts/batch-assign-proxy', expect.objectContaining({
      method: 'POST',
      body: JSON.stringify({ ids: [11, 22], proxy_url: 'socks5://10.0.0.2:10005' }),
    }))
  })
})
