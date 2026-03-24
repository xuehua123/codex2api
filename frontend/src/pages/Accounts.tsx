import type { ChangeEvent } from 'react'
import { useCallback, useState } from 'react'
import { api } from '../api'
import Modal from '../components/Modal'
import PageHeader from '../components/PageHeader'
import Pagination from '../components/Pagination'
import StateShell from '../components/StateShell'
import StatusBadge from '../components/StatusBadge'
import { useDataLoader } from '../hooks/useDataLoader'
import { useToast } from '../hooks/useToast'
import type { AccountRow, AddAccountRequest } from '../types'
import { getErrorMessage } from '../utils/error'
import { formatRelativeTime } from '../utils/time'
import { Card, CardContent } from '@/components/ui/card'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '@/components/ui/table'
import { Plus, RefreshCw, Trash2 } from 'lucide-react'

export default function Accounts() {
  const [showAdd, setShowAdd] = useState(false)
  const [page, setPage] = useState(1)
  const PAGE_SIZE = 20
  const [addForm, setAddForm] = useState<AddAccountRequest>({
    refresh_token: '',
    proxy_url: '',
  })
  const [submitting, setSubmitting] = useState(false)
  const [selected, setSelected] = useState<Set<number>>(new Set())
  const [refreshingIds, setRefreshingIds] = useState<Set<number>>(new Set())
  const [batchLoading, setBatchLoading] = useState(false)
  const { toast, showToast } = useToast()

  const loadAccounts = useCallback(async () => {
    const data = await api.getAccounts()
    return data.accounts ?? []
  }, [])

  const { data: accounts, loading, error, reload } = useDataLoader<AccountRow[]>({
    initialData: [],
    load: loadAccounts,
  })

  const totalPages = Math.max(1, Math.ceil(accounts.length / PAGE_SIZE))
  const pagedAccounts = accounts.slice((page - 1) * PAGE_SIZE, page * PAGE_SIZE)
  const allPageSelected = pagedAccounts.length > 0 && pagedAccounts.every((a) => selected.has(a.id))

  const toggleSelect = (id: number) => {
    setSelected((prev) => {
      const next = new Set(prev)
      if (next.has(id)) next.delete(id)
      else next.add(id)
      return next
    })
  }

  const toggleSelectAll = () => {
    if (allPageSelected) {
      setSelected((prev) => {
        const next = new Set(prev)
        for (const a of pagedAccounts) next.delete(a.id)
        return next
      })
    } else {
      setSelected((prev) => {
        const next = new Set(prev)
        for (const a of pagedAccounts) next.add(a.id)
        return next
      })
    }
  }

  const handleAdd = async () => {
    if (!addForm.refresh_token.trim()) return
    setSubmitting(true)
    try {
      const result = await api.addAccount(addForm)
      showToast(result.message || '账号添加成功')
      setShowAdd(false)
      setAddForm({ refresh_token: '', proxy_url: '' })
      void reload()
    } catch (error) {
      showToast(`添加失败: ${getErrorMessage(error)}`, 'error')
    } finally {
      setSubmitting(false)
    }
  }

  const handleDelete = async (account: AccountRow) => {
    if (!confirm(`确定删除账号 "${account.email || account.id}" 吗？`)) return
    try {
      await api.deleteAccount(account.id)
      showToast('已删除')
      void reload()
    } catch (error) {
      showToast(`删除失败: ${getErrorMessage(error)}`, 'error')
    }
  }

  const handleRefresh = async (account: AccountRow) => {
    setRefreshingIds((prev) => new Set(prev).add(account.id))
    try {
      await api.refreshAccount(account.id)
      showToast('刷新请求已发送')
    } catch (error) {
      showToast(`刷新失败: ${getErrorMessage(error)}`, 'error')
    } finally {
      setRefreshingIds((prev) => {
        const next = new Set(prev)
        next.delete(account.id)
        return next
      })
    }
  }

  const handleBatchDelete = async () => {
    if (selected.size === 0) return
    if (!confirm(`确定删除选中的 ${selected.size} 个账号吗？`)) return
    setBatchLoading(true)
    let success = 0
    let fail = 0
    for (const id of selected) {
      try {
        await api.deleteAccount(id)
        success++
      } catch {
        fail++
      }
    }
    showToast(`批量删除完成：成功 ${success}，失败 ${fail}`)
    setSelected(new Set())
    setBatchLoading(false)
    void reload()
  }

  const handleBatchRefresh = async () => {
    if (selected.size === 0) return
    setBatchLoading(true)
    let success = 0
    let fail = 0
    for (const id of selected) {
      try {
        await api.refreshAccount(id)
        success++
      } catch {
        fail++
      }
    }
    showToast(`批量刷新完成：成功 ${success}，失败 ${fail}`)
    setBatchLoading(false)
    void reload()
  }

  return (
    <StateShell
      variant="page"
      loading={loading}
      error={error}
      onRetry={() => void reload()}
      loadingTitle="正在加载账号列表"
      loadingDescription="账号池和实时状态正在同步。"
      errorTitle="账号页加载失败"
    >
      <>
        <PageHeader
          title="账号管理"
          description="管理 Codex 反代账号（Refresh Token）"
          onRefresh={() => void reload()}
          actions={(
            <Button onClick={() => setShowAdd(true)}>
              <Plus className="size-3.5" />
              添加账号
            </Button>
          )}
        />

        {selected.size > 0 && (
          <div className="flex items-center justify-between gap-3 px-4 py-2.5 mb-4 rounded-2xl bg-primary/10 border border-primary/20 text-sm font-semibold text-primary">
            <span>已选 {selected.size} 项</span>
            <div className="flex items-center gap-1.5">
              <Button variant="outline" size="sm" disabled={batchLoading} onClick={() => void handleBatchRefresh()}>
                批量刷新
              </Button>
              <Button variant="destructive" size="sm" disabled={batchLoading} onClick={() => void handleBatchDelete()}>
                批量删除
              </Button>
              <Button variant="outline" size="sm" onClick={() => setSelected(new Set())}>
                取消选择
              </Button>
            </div>
          </div>
        )}

        <Card>
          <CardContent className="p-6">
            <StateShell
              variant="section"
              isEmpty={accounts.length === 0}
              emptyTitle="还没有账号"
              emptyDescription="导入 Refresh Token 后，账号会立即加入号池并显示在这里。"
              action={<Button onClick={() => setShowAdd(true)}>添加账号</Button>}
            >
              <div className="overflow-auto border border-border rounded-xl">
                <Table>
                  <TableHeader>
                    <TableRow>
                      <TableHead className="w-10">
                        <input
                          type="checkbox"
                          className="size-4 cursor-pointer accent-[hsl(var(--primary))]"
                          checked={allPageSelected}
                          onChange={toggleSelectAll}
                        />
                      </TableHead>
                      <TableHead className="text-[13px] font-semibold">ID</TableHead>
                      <TableHead className="text-[13px] font-semibold">邮箱</TableHead>
                      <TableHead className="text-[13px] font-semibold">套餐</TableHead>
                      <TableHead className="text-[13px] font-semibold">状态</TableHead>
                      <TableHead className="text-[13px] font-semibold">更新时间</TableHead>
                      <TableHead className="text-[13px] font-semibold text-right">操作</TableHead>
                    </TableRow>
                  </TableHeader>
                  <TableBody>
                    {pagedAccounts.map((account) => (
                      <TableRow key={account.id} className={selected.has(account.id) ? 'bg-primary/5' : ''}>
                        <TableCell>
                          <input
                            type="checkbox"
                            className="size-4 cursor-pointer accent-[hsl(var(--primary))]"
                            checked={selected.has(account.id)}
                            onChange={() => toggleSelect(account.id)}
                          />
                        </TableCell>
                        <TableCell className="text-[14px] font-mono text-muted-foreground">{account.id}</TableCell>
                        <TableCell className="text-[14px] text-muted-foreground">{account.email || '-'}</TableCell>
                        <TableCell className="text-[14px] font-mono">{account.plan_type || '-'}</TableCell>
                        <TableCell><StatusBadge status={account.status} /></TableCell>
                        <TableCell className="text-[14px] text-muted-foreground">{formatRelativeTime(account.updated_at)}</TableCell>
                        <TableCell className="text-right">
                          <div className="flex items-center gap-1.5 justify-end">
                            <Button
                              variant="outline"
                              size="sm"
                              disabled={refreshingIds.has(account.id)}
                              onClick={() => void handleRefresh(account)}
                              title="刷新 AT"
                            >
                              <RefreshCw className={`size-3 ${refreshingIds.has(account.id) ? 'animate-spin' : ''}`} />
                              {refreshingIds.has(account.id) ? '刷新中' : '刷新'}
                            </Button>
                            <Button variant="destructive" size="sm" onClick={() => void handleDelete(account)}>
                              <Trash2 className="size-3" />
                              删除
                            </Button>
                          </div>
                        </TableCell>
                      </TableRow>
                    ))}
                  </TableBody>
                </Table>
              </div>
              <Pagination
                page={page}
                totalPages={totalPages}
                onPageChange={setPage}
                totalItems={accounts.length}
                pageSize={PAGE_SIZE}
              />
            </StateShell>
          </CardContent>
        </Card>

        <Modal
          show={showAdd}
          title="添加账号"
          onClose={() => setShowAdd(false)}
          footer={(
            <>
              <Button variant="outline" onClick={() => setShowAdd(false)}>取消</Button>
              <Button onClick={() => void handleAdd()} disabled={submitting || !addForm.refresh_token.trim()}>
                {submitting ? '添加中...' : '添加'}
              </Button>
            </>
          )}
        >
          <div className="space-y-4">
            <div>
              <label className="block mb-2 text-sm font-semibold text-muted-foreground">Refresh Token *</label>
              <textarea
                className="w-full min-h-[96px] p-3 border border-input rounded-xl bg-background text-sm resize-y focus:outline-none focus:ring-2 focus:ring-ring"
                placeholder="每行一个 Refresh Token，支持批量粘贴"
                value={addForm.refresh_token}
                onChange={(event: ChangeEvent<HTMLTextAreaElement>) =>
                  setAddForm((form) => ({ ...form, refresh_token: event.target.value }))
                }
                rows={3}
              />
            </div>
            <div>
              <label className="block mb-2 text-sm font-semibold text-muted-foreground">代理地址（可选）</label>
              <Input
                placeholder="例如：http://127.0.0.1:7890"
                value={addForm.proxy_url}
                onChange={(event: ChangeEvent<HTMLInputElement>) =>
                  setAddForm((form) => ({ ...form, proxy_url: event.target.value }))
                }
              />
            </div>
          </div>
        </Modal>

        {toast ? (
          <div
            className={`fixed right-6 bottom-6 z-[2000] px-4 py-3 rounded-2xl text-white text-sm font-bold shadow-lg ${
              toast.type === 'error' ? 'bg-destructive' : 'bg-[hsl(var(--success))]'
            }`}
            style={{ animation: 'toast-slide-up 0.22s ease' }}
          >
            {toast.msg}
          </div>
        ) : null}
      </>
    </StateShell>
  )
}
