import { createFileRoute, Link, useNavigate } from '@tanstack/react-router'
import { useQueryClient } from '@tanstack/react-query'
import { useState } from 'react'
import type { FormEvent } from 'react'
import { ArrowLeft, KeyRound, LogOut } from 'lucide-react'

import { api } from '#/lib/api'

export const Route = createFileRoute('/app/change-password')({
  component: ChangePassword,
})

function ChangePassword() {
  const navigate = useNavigate()
  const queryClient = useQueryClient()
  const [currentPassword, setCurrent] = useState('')
  const [newPassword, setNext] = useState('')
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)
  async function submit(event: FormEvent) {
    event.preventDefault()
    setBusy(true)
    setError('')
    try {
      await api('/api/v1/auth/change-password', {
        method: 'POST',
        body: JSON.stringify({
          current_password: currentPassword,
          new_password: newPassword,
        }),
      })
      queryClient.clear()
      await navigate({ to: '/app/login' })
    } catch (reason) {
      setError(reason instanceof Error ? reason.message : '密码更新失败')
    } finally {
      setBusy(false)
    }
  }
  async function logout() {
    setBusy(true)
    setError('')
    try {
      await api('/api/v1/auth/logout', { method: 'POST' })
      queryClient.clear()
      await navigate({ to: '/app/login' })
    } catch (reason) {
      setError(reason instanceof Error ? reason.message : '退出失败，请重试')
      setBusy(false)
    }
  }
  return (
    <main className="login-page">
      <section className="login-panel compact">
        <Link to="/app/create" className="change-password-back">
          <ArrowLeft size={14} />
          返回工作区
        </Link>
        <div className="login-mark">
          <KeyRound size={18} />
        </div>
        <p className="eyebrow">ACCOUNT SECURITY</p>
        <h1>修改密码</h1>
        <p className="login-copy">更新账户密码后，请使用新密码重新登录。</p>
        <form onSubmit={submit}>
          <label>
            当前密码
            <input
              type="password"
              value={currentPassword}
              onChange={(e) => setCurrent(e.target.value)}
              required
            />
          </label>
          <label>
            新密码
            <input
              type="password"
              minLength={12}
              value={newPassword}
              onChange={(e) => setNext(e.target.value)}
              required
            />
          </label>
          {error && <p className="form-error">{error}</p>}
          <div className="change-password-actions">
            <button
              type="button"
              className="secondary-button"
              disabled={busy}
              onClick={() => void logout()}
            >
              <LogOut size={14} />
              退出登录
            </button>
            <button className="primary-button" disabled={busy}>
              {busy ? '处理中…' : '更新密码'}
            </button>
          </div>
        </form>
      </section>
    </main>
  )
}
