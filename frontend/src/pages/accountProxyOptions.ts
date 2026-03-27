import type { ProxyRow } from '../api'

export interface AccountProxyOption {
  label: string
  value: string
}

export function buildAccountProxyOptions(
  proxies: ProxyRow[],
  labels: {
    clearBinding: string
    unlabeled: string
    boundAccounts: (count: number) => string
  },
): AccountProxyOption[] {
  const options: AccountProxyOption[] = [
    { value: '', label: labels.clearBinding },
  ]

  for (const proxy of proxies) {
    const prefix = proxy.label?.trim() ? `${proxy.label.trim()} · ` : `${labels.unlabeled} · `
    options.push({
      value: proxy.url,
      label: `${prefix}${proxy.url} · ${labels.boundAccounts(proxy.bound_accounts ?? 0)}`,
    })
  }

  return options
}
