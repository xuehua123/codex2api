import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import { dirname, resolve } from 'node:path'

const here = dirname(fileURLToPath(import.meta.url))
const usagePage = readFileSync(resolve(here, '../src/pages/Usage.tsx'), 'utf8')

const forbiddenSnippets = [
  { label: 'total billing card', snippet: "t('usage.totalCostCard')" },
  { label: 'price table header', snippet: "t('usage.tableCost')" },
  { label: 'API key table header', snippet: "t('usage.tableApiKey')" },
  { label: 'API key filter', snippet: "t('usage.allApiKeys')" },
  { label: 'API key option formatter', snippet: 'formatAPIKeyOptionLabel' },
  { label: 'usage API key label formatter', snippet: 'formatUsageAPIKeyLabel' },
  { label: 'cost table cell', snippet: '<UsageCostCell' },
]

const failures = forbiddenSnippets.filter(({ snippet }) => usagePage.includes(snippet))

if (failures.length > 0) {
  console.error('Usage page still exposes hidden billing/API-key UI:')
  for (const failure of failures) {
    console.error(`- ${failure.label}: ${failure.snippet}`)
  }
  process.exit(1)
}
