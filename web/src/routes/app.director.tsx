import {
  createFileRoute,
  Link,
  Outlet,
  useLocation,
  useNavigate,
} from '@tanstack/react-router'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import {
  ArrowRight,
  BookOpen,
  Clapperboard,
  Pencil,
  Plus,
  Trash2,
} from 'lucide-react'
import { useMemo, useState } from 'react'

import { AppShell } from '#/components/app-shell'
import { ConfirmDialog } from '#/components/confirm-dialog'
import { api } from '#/lib/api'
import type { DirectorProjectSummary } from '#/lib/api'

export const Route = createFileRoute('/app/director')({
  component: DirectorRoute,
})

function DirectorRoute() {
  const location = useLocation()
  return location.pathname.replace(/\/+$/, '') === '/app/director' ? (
    <DirectorProjectsPage />
  ) : (
    <Outlet />
  )
}

function DirectorProjectsPage() {
  const navigate = useNavigate()
  const queryClient = useQueryClient()
  const [renaming, setRenaming] = useState<DirectorProjectSummary | null>(null)
  const [nameDraft, setNameDraft] = useState('')
  const [deleting, setDeleting] = useState<DirectorProjectSummary | null>(null)
  const [notice, setNotice] = useState('')
  const projects = useQuery({
    queryKey: ['director-projects'],
    queryFn: () =>
      api<{ items: DirectorProjectSummary[] }>('/api/v1/director-projects'),
  })
  const defaultName = useMemo(
    () =>
      `未命名项目 ${String((projects.data?.items.length ?? 0) + 1).padStart(2, '0')}`,
    [projects.data?.items.length],
  )
  const createProject = useMutation({
    mutationFn: () =>
      api<DirectorProjectSummary>('/api/v1/director-projects', {
        method: 'POST',
        body: JSON.stringify({ name: defaultName }),
      }),
    onSuccess: async (project) => {
      await queryClient.invalidateQueries({ queryKey: ['director-projects'] })
      await navigate({
        to: '/app/director/$projectId',
        params: { projectId: project.id },
      })
    },
    onError: (error) => setNotice(error.message),
  })
  const renameProject = useMutation({
    mutationFn: ({ id, name }: { id: string; name: string }) =>
      api(`/api/v1/director-projects/${id}`, {
        method: 'PATCH',
        body: JSON.stringify({ name }),
      }),
    onSuccess: () => {
      setRenaming(null)
      void queryClient.invalidateQueries({ queryKey: ['director-projects'] })
    },
    onError: (error) => setNotice(error.message),
  })
  const deleteProject = useMutation({
    mutationFn: (id: string) =>
      api(`/api/v1/director-projects/${id}`, { method: 'DELETE' }),
    onSuccess: () => {
      setDeleting(null)
      void queryClient.invalidateQueries({ queryKey: ['director-projects'] })
    },
    onError: (error) => setNotice(error.message),
  })

  return (
    <AppShell>
      <main className="director-projects-page">
        <header className="director-projects-header">
          <div>
            <p className="eyebrow">3D DIRECTOR WORKSPACE</p>
            <h1>导演台</h1>
            <p>先完成场景、人物与机位预演，再把画面送入灵感墙继续创作。</p>
          </div>
          <div className="director-projects-actions">
            <Link to="/app/director/help" className="secondary-button">
              <BookOpen size={15} />
              使用教程
            </Link>
            <button
              className="primary-button"
              type="button"
              disabled={createProject.isPending}
              onClick={() => createProject.mutate()}
            >
              <Plus size={16} />
              {createProject.isPending ? '正在创建' : '新建项目'}
            </button>
          </div>
        </header>
        {notice && (
          <p className="director-notice" role="status">
            {notice}
          </p>
        )}
        {projects.isLoading ? (
          <div className="director-projects-empty">
            <span className="spinner" />
            正在载入项目…
          </div>
        ) : projects.data?.items.length ? (
          <section className="director-project-grid" aria-label="导演台项目">
            {projects.data.items.map((project, index) => (
              <article className="director-project-card" key={project.id}>
                <button
                  type="button"
                  className="director-project-open"
                  onClick={() =>
                    void navigate({
                      to: '/app/director/$projectId',
                      params: { projectId: project.id },
                    })
                  }
                >
                  <span className="director-project-icon">
                    <Clapperboard size={22} />
                  </span>
                  <span className="director-project-copy">
                    <strong>{project.name}</strong>
                    <small>
                      {new Intl.DateTimeFormat('zh-CN', {
                        month: '2-digit',
                        day: '2-digit',
                        hour: '2-digit',
                        minute: '2-digit',
                      }).format(new Date(project.updated_at))}
                    </small>
                  </span>
                  <span className="director-project-index">
                    {String(index + 1).padStart(2, '0')}
                  </span>
                  <ArrowRight size={18} />
                </button>
                <div className="director-project-card-actions">
                  <button
                    type="button"
                    aria-label={`重命名 ${project.name}`}
                    onClick={() => {
                      setRenaming(project)
                      setNameDraft(project.name)
                    }}
                  >
                    <Pencil size={14} />
                  </button>
                  <button
                    type="button"
                    aria-label={`删除 ${project.name}`}
                    onClick={() => setDeleting(project)}
                  >
                    <Trash2 size={14} />
                  </button>
                </div>
              </article>
            ))}
          </section>
        ) : (
          <section className="director-projects-empty">
            <Clapperboard size={28} />
            <h2>还没有导演台项目</h2>
            <p>创建一个空场景，从人物、空间与镜头开始。</p>
          </section>
        )}
      </main>
      {renaming && (
        <dialog
          className="director-name-dialog"
          open
          aria-labelledby="director-rename-title"
        >
          <form
            onSubmit={(event) => {
              event.preventDefault()
              const trimmedName = nameDraft.trim()
              if (trimmedName)
                renameProject.mutate({ id: renaming.id, name: trimmedName })
            }}
          >
            <p className="eyebrow">PROJECT NAME</p>
            <h2 id="director-rename-title">重命名项目</h2>
            <input
              value={nameDraft}
              maxLength={64}
              autoFocus
              onChange={(event) => setNameDraft(event.target.value)}
            />
            <div className="confirm-dialog-actions">
              <button
                type="button"
                className="secondary-button"
                onClick={() => setRenaming(null)}
              >
                取消
              </button>
              <button
                type="submit"
                className="primary-button"
                disabled={!nameDraft.trim() || renameProject.isPending}
              >
                保存
              </button>
            </div>
          </form>
        </dialog>
      )}
      <ConfirmDialog
        open={Boolean(deleting)}
        title="删除导演台项目"
        description={`“${deleting?.name ?? ''}”的云端场景将被永久删除，本地导入的模型文件不受影响。`}
        confirmLabel="确认删除"
        dangerous
        busy={deleteProject.isPending}
        onCancel={() => setDeleting(null)}
        onConfirm={() => deleting && deleteProject.mutate(deleting.id)}
      />
    </AppShell>
  )
}
