import {
  Link,
  Navigate,
  useLocation,
  useNavigate,
} from '@tanstack/react-router'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import * as Popover from '@radix-ui/react-popover'
import {
  ChevronDown,
  FolderOpen,
  KeyRound,
  LogOut,
  Settings2,
  Sparkles,
} from 'lucide-react'
import { useEffect } from 'react'
import type { ReactNode } from 'react'

import { api, authExpiredEvent, getMe } from '#/lib/api'

export function AppShell({ children }: { children: ReactNode }) {
  const navigate = useNavigate()
  const location = useLocation()
  const queryClient = useQueryClient()
  const me = useQuery({
    queryKey: ['me'],
    queryFn: getMe,
    retry: false,
    refetchInterval: 30_000,
    refetchIntervalInBackground: true,
  })
  useEffect(() => {
    const expire = () => {
      queryClient.clear()
      void navigate({ to: '/app/login', replace: true })
    }
    window.addEventListener(authExpiredEvent, expire)
    return () => window.removeEventListener(authExpiredEvent, expire)
  }, [navigate, queryClient])
  useEffect(() => {
    if (!me.isError) return
    queryClient.clear()
    void navigate({ to: '/app/login', replace: true })
  }, [me.isError, navigate, queryClient])
  if (me.isError) {
    return (
      <main className="app-loading">
        <span className="spinner" />
        正在确认工作区…
      </main>
    )
  }
  if (!me.data)
    return (
      <main className="app-loading">
        <span className="spinner" />
        载入工作区…
      </main>
    )
  const user = me.data.user
  if (user.must_change_password)
    return <Navigate to="/app/change-password" replace />
  if (location.pathname.startsWith('/app/admin') && user.role !== 'admin')
    return <Navigate to="/app/create" replace />
  return (
    <div className="app-frame">
      <header className="app-nav">
        <Link to="/app/create" className="app-brand" aria-label="Cornfield">
          <img className="brand-mark" src="/cornfield-cube.svg" alt="" />
          <span>Cornfield</span>
        </Link>
        <nav aria-label="主导航">
          <Link to="/app/create" activeProps={{ className: 'active' }}>
            <Sparkles size={14} />
            创作
          </Link>
          <Link to="/app/assets" activeProps={{ className: 'active' }}>
            <FolderOpen size={14} />
            资产
          </Link>
          {user.role === 'admin' && (
            <Link
              to="/app/admin/providers"
              activeProps={{ className: 'active' }}
            >
              <Settings2 size={14} />
              管理
            </Link>
          )}
        </nav>
        <div className="nav-account">
          <Popover.Root>
            <Popover.Trigger asChild>
              <button className="account-trigger" aria-label="打开账户菜单">
                <span className="avatar">
                  {user.display_name.slice(0, 1).toUpperCase()}
                </span>
                <span className="account-name">{user.display_name}</span>
                <ChevronDown size={13} aria-hidden="true" />
              </button>
            </Popover.Trigger>
            <Popover.Portal>
              <Popover.Content
                className="account-menu"
                align="end"
                sideOffset={8}
                collisionPadding={8}
              >
                <Popover.Close asChild>
                  <Link to="/app/change-password" className="account-menu-item">
                    <KeyRound size={14} />
                    修改密码
                  </Link>
                </Popover.Close>
                <button
                  className="account-menu-item"
                  onClick={async () => {
                    await api('/api/v1/auth/logout', { method: 'POST' })
                    queryClient.clear()
                    await navigate({ to: '/app/login' })
                  }}
                >
                  <LogOut size={14} />
                  退出登录
                </button>
              </Popover.Content>
            </Popover.Portal>
          </Popover.Root>
        </div>
      </header>
      {children}
    </div>
  )
}

export function AdminTabs() {
  return (
    <nav className="admin-tabs" aria-label="管理导航">
      <Link to="/app/admin/providers" activeProps={{ className: 'active' }}>
        上游状态
      </Link>
      <Link to="/app/admin/users" activeProps={{ className: 'active' }}>
        用户
      </Link>
    </nav>
  )
}
