import { createFileRoute } from '@tanstack/react-router'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Activity, AlertTriangle, CheckCircle2, RotateCcw } from 'lucide-react'

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
  terminal_successes_1h: number
  availability_failures_1h: number
  success_rate_1h?: number
}

function ProvidersPage() {
  const client = useQueryClient()
  const result = useQuery({
    queryKey: ['admin', 'providers'],
    queryFn: () => api<{ items: Provider[] }>('/api/v1/admin/providers'),
    refetchInterval: 15_000,
  })
  const resume = useMutation({
    mutationFn: (providerID: string) =>
      api<{ id: string; state: string; resumed: boolean }>(
        `/api/v1/admin/providers/${encodeURIComponent(providerID)}/resume`,
        { method: 'POST' },
      ),
    onSuccess: () =>
      client.invalidateQueries({ queryKey: ['admin', 'providers'] }),
  })

  function confirmResume(provider: Provider) {
    const confirmed = window.confirm(
      `确认恢复 ${provider.display_name}？请先确认 API Key、权限或额度问题已经解决。恢复后可能产生新的上游费用。`,
    )
    if (confirmed) resume.mutate(provider.id)
  }

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
                <div>
                  <dt>1H TERMINAL SUCCESS</dt>
                  <dd>{provider.terminal_successes_1h}</dd>
                </div>
                <div>
                  <dt>1H AVAILABILITY FAILURES</dt>
                  <dd>{provider.availability_failures_1h}</dd>
                </div>
                <div>
                  <dt>1H SUCCESS RATE</dt>
                  <dd>
                    {provider.success_rate_1h == null
                      ? '—'
                      : `${Math.round(provider.success_rate_1h * 100)}%`}
                  </dd>
                </div>
              </dl>
              {provider.state === 'paused' && provider.enabled && (
                <div className="provider-action">
                  <p>暂停状态不会被健康探针自动解除。</p>
                  <button
                    type="button"
                    className="provider-resume-button"
                    disabled={resume.isPending}
                    onClick={() => confirmResume(provider)}
                  >
                    <RotateCcw size={13} />
                    {resume.isPending && resume.variables === provider.id
                      ? '正在恢复…'
                      : '确认问题已解决并恢复'}
                  </button>
                  {resume.isError && resume.variables === provider.id && (
                    <p className="provider-action-error" role="alert">
                      {resume.error.message}
                    </p>
                  )}
                </div>
              )}
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
