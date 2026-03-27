import zh from './zh.json'
import en from './en.json'

describe('account import copy', () => {
  it('mentions CPA original format support in Chinese add/import hints', () => {
    expect(zh.accounts.importTxtDesc).toContain('CPA 原版格式')
    expect(zh.accounts.refreshTokenPlaceholder).toContain('CPA 原版格式')
    expect(zh.accounts.refreshTokenPlaceholder).toContain('自动提取 RT')
  })

  it('mentions original-format auto extraction in English add/import hints', () => {
    expect(en.accounts.importTxtDesc).toContain('original format')
    expect(en.accounts.refreshTokenPlaceholder).toContain('original format')
    expect(en.accounts.refreshTokenPlaceholder).toContain('auto-extracted')
  })
})
