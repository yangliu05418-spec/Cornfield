import { createFileRoute, Link, useNavigate } from '@tanstack/react-router'
import { useQueryClient } from '@tanstack/react-query'
import { useState } from 'react'
import type { FormEvent } from 'react'
import { ArrowLeft, ArrowRight } from 'lucide-react'

import { api } from '#/lib/api'
import type { User } from '#/lib/api'

export const Route = createFileRoute('/app/login')({ component: LoginPage })

function LoginPage() {
  const navigate = useNavigate()
  const queryClient = useQueryClient()
  const [username, setUsername] = useState('')
  const [password, setPassword] = useState('')
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)
  async function submit(event: FormEvent) {
    event.preventDefault()
    setBusy(true)
    setError('')
    try {
      const result = await api<{ user: User }>('/api/v1/auth/login', {
        method: 'POST',
        body: JSON.stringify({ username, password }),
      })
      // A browser can be handed from one internal user to another. Remove all
      // prior prompts/assets before installing the newly authenticated user.
      queryClient.clear()
      queryClient.setQueryData(['me'], { user: result.user })
      await navigate({
        to: result.user.must_change_password
          ? '/app/change-password'
          : '/app/create',
      })
    } catch (reason) {
      setError(reason instanceof Error ? reason.message : '登录失败')
    } finally {
      setBusy(false)
    }
  }
  return (
    <main className="login-page">
      <div className="login-atmosphere" aria-hidden="true" />
      <Link to="/" className="back-link">
        <ArrowLeft size={14} />
        返回首页
      </Link>
      <section className="login-panel">
        <img className="login-mark" src="/cornfield-mark.svg" alt="" />
        <p className="eyebrow">PRIVATE WORKSPACE</p>
        <h1>回到创作现场</h1>
        <p className="login-copy">使用管理员分配的内部账号进入 Cornfield。</p>
        <form onSubmit={submit}>
          <label>
            用户名
            <input
              autoFocus
              autoComplete="username"
              value={username}
              onChange={(event) => setUsername(event.target.value)}
              required
            />
          </label>
          <label>
            密码
            <input
              type="password"
              autoComplete="current-password"
              value={password}
              onChange={(event) => setPassword(event.target.value)}
              required
            />
          </label>
          {error && (
            <p className="form-error" role="alert">
              {error}
            </p>
          )}
          <button className="primary-button" disabled={busy}>
            {busy ? (
              '正在进入…'
            ) : (
              <>
                进入工作区 <ArrowRight size={15} />
              </>
            )}
          </button>
        </form>
        <p className="login-note">没有公开注册。需要账号请联系工作区管理员。</p>
      </section>
    </main>
  )
}
