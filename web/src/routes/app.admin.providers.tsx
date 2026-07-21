import { createFileRoute } from '@tanstack/react-router'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Activity, AlertTriangle, CheckCircle2, RotateCcw } from 'lucide-react'
import { useState } from 'react'

import { AdminTabs, AppShell } from '#/components/app-shell'
import { ConfirmDialog } from '#/components/confirm-dialog'
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

type UncertainJob = {
  id: string
  username: string
  model_id: string
  provider_id: string
  created_at: string
  age_seconds: number
  provider_job_id?: string
  latest_attempt?: {
    operation?: string
    error_code?: string
    error_message?: string
  }
}

function ProvidersPage() {
  const client = useQueryClient()
  const [resumeTarget, setResumeTarget] = useState<Provider | null>(null)
  const [remoteIDs, setRemoteIDs] = useState<Partial<Record<string, string>>>(
    {},
  )
  const [reconcileTarget, setReconcileTarget] = useState<{
    job: UncertainJob
    action: 'confirm_absent' | 'confirm_accepted_unrecoverable'
  } | null>(null)
  const result = useQuery({
    queryKey: ['admin', 'providers'],
    queryFn: () => api<{ items: Provider[] }>('/api/v1/admin/providers'),
    refetchInterval: 15_000,
  })
  const uncertain = useQuery({
    queryKey: ['admin', 'submission-uncertain'],
    queryFn: () =>
      api<{ items: UncertainJob[] }>(
        '/api/v1/admin/jobs/submission-uncertain?limit=50',
      ),
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
  const reconcile = useMutation({
    mutationFn: ({
      jobID,
      body,
    }: {
      jobID: string
      body: Record<string, unknown>
    }) =>
      api(`/api/v1/admin/jobs/${jobID}/reconcile-submission`, {
        method: 'POST',
        body: JSON.stringify(body),
      }),
    onSuccess: () => {
      setReconcileTarget(null)
      void client.invalidateQueries({
        queryKey: ['admin', 'submission-uncertain'],
      })
      void client.invalidateQueries({ queryKey: ['admin', 'providers'] })
    },
  })

  function confirmResume(provider: Provider) {
    setResumeTarget(provider)
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
        <section className="uncertain-jobs">
          <div className="uncertain-heading">
            <div>
              <p className="eyebrow">MANUAL RECONCILIATION</p>
              <h2>需要核查的提交</h2>
            </div>
            <span>{uncertain.data?.items.length ?? 0}</span>
          </div>
          {uncertain.data?.items.map((job) => (
            <article key={job.id}>
              <div>
                <strong>
                  {job.username} · {job.model_id}
                </strong>
                <small>
                  {job.provider_id} · {Math.round(job.age_seconds / 60)} 分钟前
                  · {job.latest_attempt?.error_code ?? 'UNKNOWN'}
                </small>
              </div>
              {job.provider_id === 'legnext' && (
                <div className="uncertain-attach">
                  <input
                    aria-label="Legnext 远端任务 ID"
                    placeholder="远端 job ID"
                    value={remoteIDs[job.id] ?? ''}
                    onChange={(event) =>
                      setRemoteIDs((current) => ({
                        ...current,
                        [job.id]: event.target.value,
                      }))
                    }
                  />
                  <button
                    type="button"
                    disabled={!remoteIDs[job.id]?.trim() || reconcile.isPending}
                    onClick={() =>
                      reconcile.mutate({
                        jobID: job.id,
                        body: {
                          action: 'attach_provider_job',
                          provider_job_id: remoteIDs[job.id]!.trim(),
                        },
                      })
                    }
                  >
                    继续核查
                  </button>
                </div>
              )}
              <div className="uncertain-actions">
                <button
                  type="button"
                  onClick={() =>
                    setReconcileTarget({ job, action: 'confirm_absent' })
                  }
                >
                  确认未创建并重试
                </button>
                <button
                  type="button"
                  className="bulk-danger"
                  onClick={() =>
                    setReconcileTarget({
                      job,
                      action: 'confirm_accepted_unrecoverable',
                    })
                  }
                >
                  确认结果不可恢复
                </button>
              </div>
            </article>
          ))}
          {!uncertain.isLoading && !uncertain.data?.items.length && (
            <p className="table-empty">当前没有需要人工核查的提交。</p>
          )}
        </section>
      </main>
      <ConfirmDialog
        open={resumeTarget !== null}
        title="恢复上游服务"
        description={`确认 ${resumeTarget?.display_name ?? ''} 的 API Key、权限或额度问题已经解决。恢复后可能产生新的上游费用。`}
        confirmLabel="确认恢复"
        busy={resume.isPending}
        onCancel={() => setResumeTarget(null)}
        onConfirm={() => {
          if (!resumeTarget) return
          resume.mutate(resumeTarget.id, {
            onSuccess: () => setResumeTarget(null),
          })
        }}
      />
      <ConfirmDialog
        open={reconcileTarget !== null}
        title={
          reconcileTarget?.action === 'confirm_absent'
            ? '确认远端未创建任务'
            : '确认结果不可恢复'
        }
        description={
          reconcileTarget?.action === 'confirm_absent'
            ? '仅在远端明确不存在任务时继续；操作会重新提交并可能造成重复计费。'
            : '任务会终止并按已计费处理，结果不可恢复。'
        }
        confirmLabel={
          reconcileTarget?.action === 'confirm_absent'
            ? '确认并重新提交'
            : '确认终止'
        }
        dangerous
        busy={reconcile.isPending}
        onCancel={() => setReconcileTarget(null)}
        onConfirm={() => {
          if (!reconcileTarget) return
          const absent = reconcileTarget.action === 'confirm_absent'
          reconcile.mutate({
            jobID: reconcileTarget.job.id,
            body: absent
              ? { action: 'confirm_absent', confirmed_remote_absent: true }
              : {
                  action: 'confirm_accepted_unrecoverable',
                  confirmed_provider_accepted: true,
                  confirmed_result_unrecoverable: true,
                },
          })
        }}
      />
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
