import { act, render, screen, waitFor } from '@testing-library/react'
import AuthGate from './AuthGate'
import { AUTH_REQUIRED_EVENT, setAdminKey } from '../api'

const secretPlaceholder = /输入管理密钥|Enter admin secret/i

describe('AuthGate', () => {
  it('shows login screen instead of children when health check returns 429', async () => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(
      new Response(JSON.stringify({ error: '密码错误次数过多，IP 已被封禁 5 分钟，请稍后再试' }), {
        status: 429,
        headers: { 'Content-Type': 'application/json' },
      }),
    ))
    setAdminKey('stale-key')

    render(
      <AuthGate>
        <div>protected app shell</div>
      </AuthGate>,
    )

    expect(await screen.findByPlaceholderText(secretPlaceholder)).toBeInTheDocument()
    expect(screen.queryByText('protected app shell')).not.toBeInTheDocument()
    expect(screen.getByText('密码错误次数过多，IP 已被封禁 5 分钟，请稍后再试')).toBeInTheDocument()
  })

  it('hides children when an auth-required event is emitted after initial success', async () => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(
      new Response('{}', {
        status: 200,
        headers: { 'Content-Type': 'application/json' },
      }),
    ))
    setAdminKey('valid-key')

    render(
      <AuthGate>
        <div>protected app shell</div>
      </AuthGate>,
    )

    expect(await screen.findByText('protected app shell')).toBeInTheDocument()

    act(() => {
      window.dispatchEvent(new CustomEvent(AUTH_REQUIRED_EVENT, {
        detail: {
          status: 429,
          message: '密码错误次数过多，IP 已被封禁 5 分钟，请稍后再试',
        },
      }))
    })

    await waitFor(() => {
      expect(screen.queryByText('protected app shell')).not.toBeInTheDocument()
    })
    expect(screen.getByPlaceholderText(secretPlaceholder)).toBeInTheDocument()
    expect(screen.getByText('密码错误次数过多，IP 已被封禁 5 分钟，请稍后再试')).toBeInTheDocument()
  })
})
