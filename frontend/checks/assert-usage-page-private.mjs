import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import { dirname, resolve } from 'node:path'

const here = dirname(fileURLToPath(import.meta.url))
const usagePage = readFileSync(resolve(here, '../src/pages/Usage.tsx'), 'utf8')
const apiKeyTokenPanel = readFileSync(resolve(here, '../src/components/APIKeyTokenUsagePanel.tsx'), 'utf8')

const forbiddenSnippets = [
  { file: usagePage, label: 'total billing card', snippet: "t('usage.totalCostCard')" },
  { file: usagePage, label: 'price table header', snippet: "t('usage.tableCost')" },
  { file: usagePage, label: 'API key table header', snippet: "t('usage.tableApiKey')" },
  { file: usagePage, label: 'API key filter', snippet: "t('usage.allApiKeys')" },
  { file: usagePage, label: 'API key option formatter', snippet: 'formatAPIKeyOptionLabel' },
  { file: usagePage, label: 'usage API key label formatter', snippet: 'formatUsageAPIKeyLabel' },
  { file: usagePage, label: 'cost table cell', snippet: '<UsageCostCell' },
  { file: apiKeyTokenPanel, label: 'API key token cost column', snippet: "tokenUsageColCost" },
  { file: apiKeyTokenPanel, label: 'API key token billed value', snippet: "user_billed" },
  { file: apiKeyTokenPanel, label: 'API key token USD formatter', snippet: "formatUSD" },
]

const failures = forbiddenSnippets.filter(({ file, snippet }) => file.includes(snippet))

if (failures.length > 0) {
  console.error('Usage page still exposes hidden billing/API-key UI:')
  for (const failure of failures) {
    console.error(`- ${failure.label}: ${failure.snippet}`)
  }
  process.exit(1)
}
