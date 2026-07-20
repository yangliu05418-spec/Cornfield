import { createFileRoute } from '@tanstack/react-router'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Ban, Plus, RotateCcw, Trash2, X } from 'lucide-react'
import { useState } from 'react'
import type { FormEvent } from 'react'

import { AdminTabs, AppShell } from '#/components/app-shell'
import { PageTitle } from './app.admin.providers'
import { api, getMe } from '#/lib/api'

export const Route = createFileRoute('/app/admin/users')({
  component: UsersPage,
})
type AdminUser = {
  id: string
  username: string
  display_name: string
  role: string
  status: string
  must_change_password: boolean
  last_login_at?: string
}

function UsersPage() {
  const client = useQueryClient()
  const users = useQuery({
    queryKey: ['admin', 'users'],
    queryFn: () => api<{ items: AdminUser[] }>('/api/v1/admin/users'),
    refetchInterval: (query) =>
      query.state.data?.items.some((user) => user.status === 'deleting')
        ? 2_000
        : false,
  })
  const me = useQuery({ queryKey: ['me'], queryFn: getMe })
  const [open, setOpen] = useState(false)
  const [temporary, setTemporary] = useState('')
  const [deleteTarget, setDeleteTarget] = useState<AdminUser | null>(null)
  const [deleteConfirmation, setDeleteConfirmation] = useState('')
  const [actionError, setActionError] = useState('')
  const create = useMutation({
    mutationFn: (body: object) =>
      api<{ temporary_password: string }>('/api/v1/admin/users', {
        method: 'POST',
        body: JSON.stringify(body),
      }),
    onSuccess: (data) => {
      setTemporary(data.temporary_password)
      void client.invalidateQueries({ queryKey: ['admin', 'users'] })
    },
  })
  const updateStatus = useMutation({
    mutationFn: ({
      id,
      status,
    }: {
      id: string
      status: 'active' | 'disabled'
    }) =>
      api(`/api/v1/admin/users/${id}`, {
        method: 'PATCH',
        body: JSON.stringify({ status }),
      }),
    onSuccess: () => {
      setActionError('')
      void client.invalidateQueries({ queryKey: ['admin', 'users'] })
    },
    onError: (error) => setActionError(error.message),
  })
  const removeUser = useMutation({
    mutationFn: (user: AdminUser) =>
      api(`/api/v1/admin/users/${user.id}/deletion`, {
        method: 'POST',
        body: JSON.stringify({ username: deleteConfirmation }),
      }),
    onSuccess: () => {
      setDeleteTarget(null)
      setDeleteConfirmation('')
      setActionError('')
      void client.invalidateQueries({ queryKey: ['admin', 'users'] })
    },
    onError: (error) => setActionError(error.message),
  })
  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    const form = new FormData(event.currentTarget)
    create.mutate({
      username: form.get('username'),
      display_name: form.get('display_name'),
      role: form.get('role'),
    })
  }
  return (
    <AppShell>
      <main className="admin-page">
        <div className="heading-action">
          <PageTitle
            label="ACCESS CONTROL"
            title="内部用户"
            copy="账号由管理员创建；临时密码只显示一次。"
          />
          <button
            className="secondary-button"
            onClick={() => {
              setOpen(true)
              setTemporary('')
            }}
          >
            <Plus size={14} />
            新用户
          </button>
        </div>
        <AdminTabs />
        <div className="data-table">
          <div className="table-row users-table-row table-head">
            <span>用户</span>
            <span>角色</span>
            <span>状态</span>
            <span>最近登录</span>
            <span>操作</span>
          </div>
          {users.data?.items.map((user) => (
            <div className="table-row users-table-row" key={user.id}>
              <span>
                <strong>{user.display_name}</strong>
                <small>@{user.username}</small>
              </span>
              <span>{user.role}</span>
              <span>
                <i className={`status-pill ${user.status}`}>{user.status}</i>
              </span>
              <span>
                {user.last_login_at
                  ? new Date(user.last_login_at).toLocaleString()
                  : '从未'}
              </span>
              <span className="user-actions">
                {user.status === 'active' && (
                  <button
                    type="button"
                    disabled={user.id === me.data?.user.id}
                    onClick={() =>
                      updateStatus.mutate({ id: user.id, status: 'disabled' })
                    }
                  >
                    <Ban size={13} /> 停用
                  </button>
                )}
                {user.status === 'disabled' && (
                  <button
                    type="button"
                    onClick={() =>
                      updateStatus.mutate({ id: user.id, status: 'active' })
                    }
                  >
                    <RotateCcw size={13} /> 恢复
                  </button>
                )}
                {!['deleting', 'deleted'].includes(user.status) && (
                  <button
                    className="danger-action"
                    type="button"
                    disabled={user.id === me.data?.user.id}
                    onClick={() => {
                      setDeleteTarget(user)
                      setDeleteConfirmation('')
                      setActionError('')
                    }}
                  >
                    <Trash2 size={13} /> 删除
                  </button>
                )}
                {user.status === 'deleting' && <small>正在清理…</small>}
                {user.status === 'deleted' && <small>已确认清理</small>}
              </span>
            </div>
          ))}
        </div>
        {actionError && (
          <p className="form-error admin-action-error">{actionError}</p>
        )}
        {open && (
          <div className="modal-layer">
            <section className="admin-modal">
              <button className="modal-close" onClick={() => setOpen(false)}>
                <X />
              </button>
              {temporary ? (
                <>
                  <p className="eyebrow">COPY ONCE</p>
                  <h2>临时密码已创建</h2>
                  <code className="temporary-password">{temporary}</code>
                  <p>24 小时内有效。关闭后不会再次显示。</p>
                  <button
                    className="primary-button"
                    onClick={() => navigator.clipboard.writeText(temporary)}
                  >
                    复制密码
                  </button>
                </>
              ) : (
                <>
                  <p className="eyebrow">NEW MEMBER</p>
                  <h2>创建内部用户</h2>
                  <form onSubmit={submit}>
                    <label>
                      用户名
                      <input name="username" required />
                    </label>
                    <label>
                      显示名
                      <input name="display_name" required />
                    </label>
                    <label>
                      角色
                      <select name="role">
                        <option value="member">Member</option>
                        <option value="admin">Admin</option>
                      </select>
                    </label>
                    {create.error && (
                      <p className="form-error">{create.error.message}</p>
                    )}
                    <button className="primary-button">
                      创建并生成临时密码
                    </button>
                  </form>
                </>
              )}
            </section>
          </div>
        )}
        {deleteTarget && (
          <div className="modal-layer">
            <section className="admin-modal danger-modal">
              <button
                className="modal-close"
                onClick={() => setDeleteTarget(null)}
              >
                <X />
              </button>
              <p className="eyebrow">PERMANENT DELETION</p>
              <h2>永久删除用户</h2>
              <p>
                账号将立即停用，生成记录与图片会由 Worker
                物理清理。此操作无法撤销。
              </p>
              <label>
                输入用户名 <strong>{deleteTarget.username}</strong> 确认
                <input
                  autoFocus
                  value={deleteConfirmation}
                  onChange={(event) =>
                    setDeleteConfirmation(event.target.value)
                  }
                />
              </label>
              {removeUser.error && (
                <p className="form-error">{removeUser.error.message}</p>
              )}
              <button
                className="danger-button"
                disabled={
                  deleteConfirmation !== deleteTarget.username ||
                  removeUser.isPending
                }
                onClick={() => removeUser.mutate(deleteTarget)}
              >
                {removeUser.isPending ? '正在提交…' : '永久删除用户'}
              </button>
            </section>
          </div>
        )}
      </main>
    </AppShell>
  )
}
