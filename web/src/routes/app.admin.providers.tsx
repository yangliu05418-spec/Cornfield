import { createFileRoute } from '@tanstack/react-router'
import { useQuery } from '@tanstack/react-query'
import { Activity, AlertTriangle, CheckCircle2 } from 'lucide-react'

import { AdminTabs, AppShell } from '#/components/app-shell'
import { api } from '#/lib/api'

export const Route = createFileRoute('/app/admin/providers')({
  component: ProvidersPage,
})

type Provider = {
  id: string
  display_name: string
  enabled: boolean
  state: string
  breaker_open_until?: string
  last_probe_at?: string
  last_error_code?: string
  active_jobs: number
}

function ProvidersPage() {
  const result = useQuery({
    queryKey: ['admin', 'providers'],
    queryFn: () => api<{ items: Provider[] }>('/api/v1/admin/providers'),
    refetchInterval: 15_000,
  })
  return (
    <AppShell>
      <main className="admin-page">
        <PageTitle
          label="OPERATIONS"
          title="上游运行状态"
          copy="只显示运行事实，不在界面中修改静态模型配置。"
        />
        <AdminTabs />
        <div className="provider-grid">
          {result.data?.items.map((provider) => (
            <article key={provider.id}>
              <div className="provider-head">
                <span className={`provider-state ${provider.state}`}>
                  {provider.state === 'healthy' ? (
                    <CheckCircle2 />
                  ) : provider.state === 'paused' ? (
                    <AlertTriangle />
                  ) : (
                    <Activity />
                  )}
                </span>
                <div>
                  <h2>{provider.display_name}</h2>
                  <p>{provider.id}</p>
                </div>
                <strong>{provider.state.toUpperCase()}</strong>
              </div>
              <dl>
                <div>
                  <dt>ACTIVE DRAWS</dt>
                  <dd>{provider.active_jobs}</dd>
                </div>
                <div>
                  <dt>LAST PROBE</dt>
                  <dd>
                    {provider.last_probe_at
                      ? new Date(provider.last_probe_at).toLocaleTimeString()
                      : '—'}
                  </dd>
                </div>
                <div>
                  <dt>BREAKER</dt>
                  <dd>{provider.breaker_open_until ? 'OPEN' : 'CLOSED'}</dd>
                </div>
                <div>
                  <dt>LAST ERROR</dt>
                  <dd>{provider.last_error_code ?? 'NONE'}</dd>
                </div>
              </dl>
            </article>
          ))}
        </div>
      </main>
    </AppShell>
  )
}

export function PageTitle({
  label,
  title,
  copy,
}: {
  label: string
  title: string
  copy: string
}) {
  return (
    <header className="page-heading">
      <div>
        <p className="eyebrow">{label}</p>
        <h1>{title}</h1>
        <p>{copy}</p>
      </div>
    </header>
  )
}
