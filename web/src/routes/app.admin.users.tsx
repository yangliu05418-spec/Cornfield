import { createFileRoute } from '@tanstack/react-router'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Plus, X } from 'lucide-react'
import { useState } from 'react'
import type { FormEvent } from 'react'

import { AdminTabs, AppShell } from '#/components/app-shell'
import { PageTitle } from './app.admin.providers'
import { api } from '#/lib/api'

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
  })
  const [open, setOpen] = useState(false)
  const [temporary, setTemporary] = useState('')
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
          <div className="table-row table-head">
            <span>用户</span>
            <span>角色</span>
            <span>状态</span>
            <span>最近登录</span>
          </div>
          {users.data?.items.map((user) => (
            <div className="table-row" key={user.id}>
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
            </div>
          ))}
        </div>
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
      </main>
    </AppShell>
  )
}
