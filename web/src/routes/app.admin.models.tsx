import { createFileRoute } from '@tanstack/react-router'
import { useQuery } from '@tanstack/react-query'

import { AdminTabs, AppShell } from '#/components/app-shell'
import { PageTitle } from './app.admin.providers'
import { api } from '#/lib/api'
import type { Model } from '#/lib/api'

export const Route = createFileRoute('/app/admin/models')({
  component: ModelsPage,
})

function ModelsPage() {
  const result = useQuery({
    queryKey: ['admin', 'models'],
    queryFn: () =>
      api<{ revision: string; models: Model[] }>('/api/v1/admin/models'),
  })
  return (
    <AppShell>
      <main className="admin-page">
        <PageTitle
          label="MODEL CATALOG"
          title="模型能力协议"
          copy="配置由代码评审与部署更新，此处仅用于确认生效状态。"
        />
        <AdminTabs />
        <div className="revision-bar">
          <span>CAPABILITY REVISION</span>
          <code>{result.data?.revision ?? '—'}</code>
        </div>
        <div className="data-table">
          <div className="table-row table-head">
            <span>模型</span>
            <span>Provider</span>
            <span>输出/抽卡</span>
            <span>能力</span>
          </div>
          {result.data?.models.map((model) => (
            <div className="table-row" key={model.id}>
              <span>
                <strong>{model.display_name}</strong>
                <small>{model.id}</small>
              </span>
              <span>{model.provider}</span>
              <span>{model.outputs_per_draw}</span>
              <span>
                {model.capabilities.text_to_image ? 'T2I' : ''}
                {model.capabilities.image_to_image ? ' · I2I' : ''}
              </span>
            </div>
          ))}
        </div>
      </main>
    </AppShell>
  )
}
