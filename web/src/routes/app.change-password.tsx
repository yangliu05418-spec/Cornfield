import { createFileRoute, useNavigate } from '@tanstack/react-router'
import { useQueryClient } from '@tanstack/react-query'
import { useState } from 'react'
import type { FormEvent } from 'react'
import { KeyRound } from 'lucide-react'

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
  async function submit(event: FormEvent) {
    event.preventDefault()
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
    }
  }
  return (
    <main className="login-page">
      <section className="login-panel compact">
        <div className="login-mark">
          <KeyRound size={18} />
        </div>
        <p className="eyebrow">FIRST SIGN IN</p>
        <h1>设置你的新密码</h1>
        <p className="login-copy">临时密码只能使用一次。修改后请重新登录。</p>
        <form onSubmit={submit}>
          <label>
            临时密码
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
          <button className="primary-button">更新密码</button>
        </form>
      </section>
    </main>
  )
}
