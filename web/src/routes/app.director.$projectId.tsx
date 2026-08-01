import { createFileRoute, useNavigate } from '@tanstack/react-router'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import type { InfiniteData } from '@tanstack/react-query'
import { useCallback, useEffect, useMemo, useRef, useState } from 'react'

import { AppShell } from '#/components/app-shell'
import { ConfirmDialog } from '#/components/confirm-dialog'
import { api, APIError, getMe } from '#/lib/api'
import type { Asset, AssetPage, DirectorProject } from '#/lib/api'

export const Route = createFileRoute('/app/director/$projectId')({
  component: DirectorEditorPage,
})

const maxCaptureBytes = 25 * 1024 * 1024
const uploadDeadline = 2 * 60 * 1000

type HostMessage = { type?: unknown; payload?: unknown }
type SaveState = 'saved' | 'saving' | 'conflict' | 'error'
type AssetPages = InfiniteData<AssetPage, string>
type SessionError = { code: string; message: string }

function isRecord(value: unknown): value is Record<string, unknown> {
  return Boolean(value) && typeof value === 'object' && !Array.isArray(value)
}

function captureFile(value: unknown, fallbackName: string): File {
  if (!isRecord(value) || typeof value.dataUrl !== 'string')
    throw new Error('截图格式无效')
  const match = /^data:image\/png;base64,([A-Za-z0-9+/=]+)$/.exec(value.dataUrl)
  if (!match) throw new Error('仅支持 PNG 截图')
  const binary = window.atob(match[1])
  if (!binary.length || binary.length > maxCaptureBytes)
    throw new Error('截图为空或超过 25 MiB')
  const bytes = new Uint8Array(binary.length)
  for (let index = 0; index < binary.length; index += 1)
    bytes[index] = binary.charCodeAt(index)
  const requestedName =
    typeof value.fileName === 'string' ? value.fileName.trim() : ''
  const fileName = /^[^\\/:*?"<>|]{1,120}\.png$/i.test(requestedName)
    ? requestedName
    : fallbackName
  return new File([bytes], fileName, { type: 'image/png' })
}

async function uploadCapture(file: File): Promise<Asset> {
  const session = await api<{ id: string; content_url: string }>(
    '/api/v1/uploads',
    {
      method: 'POST',
      body: JSON.stringify({
        filename: file.name,
        media_type: file.type,
        size: file.size,
      }),
    },
  )
  await api(session.content_url, { method: 'PUT', body: file })
  const deadline = Date.now() + uploadDeadline
  let delay = 400
  while (Date.now() < deadline) {
    const state = await api<{
      status: string
      asset_id?: string
      error_code?: string
    }>(`/api/v1/uploads/${session.id}`)
    if (state.status === 'ready' && state.asset_id)
      return api<Asset>(`/api/v1/assets/${state.asset_id}`)
    if (state.status === 'failed')
      throw new Error(`截图校验失败：${state.error_code ?? 'IMAGE_INVALID'}`)
    await new Promise((resolve) => window.setTimeout(resolve, delay))
    delay = Math.min(Math.ceil(delay * 1.5), 3000)
  }
  throw new Error('截图仍在处理，请稍后重试')
}

function mergeWallAsset(
  queryClient: ReturnType<typeof useQueryClient>,
  asset: Asset,
) {
  queryClient.setQueryData<AssetPages>(['assets', 'wall'], (current) => {
    if (!current?.pages.length)
      return { pages: [{ items: [asset], next_cursor: '' }], pageParams: [''] }
    const exists = current.pages.some((page) =>
      page.items.some((item) => item.id === asset.id),
    )
    const pages = current.pages.map((page) => ({
      ...page,
      items: page.items.map((item) => (item.id === asset.id ? asset : item)),
    }))
    if (!exists) pages[0] = { ...pages[0], items: [asset, ...pages[0].items] }
    return { ...current, pages }
  })
}

function DirectorEditorPage() {
  const { projectId } = Route.useParams()
  const navigate = useNavigate()
  const queryClient = useQueryClient()
  const iframeRef = useRef<HTMLIFrameElement>(null)
  const iframeReadyRef = useRef(false)
  const sessionSentRef = useRef(false)
  const revisionRef = useRef(0)
  const pendingDocumentRef = useRef<unknown>(null)
  const saveTimerRef = useRef<number | null>(null)
  const savingRef = useRef(false)
  const conflictRef = useRef(false)
  const [saveState, setSaveState] = useState<SaveState>('saved')
  const [saveMessage, setSaveMessage] = useState('已保存')
  const [sessionError, setSessionError] = useState<SessionError | null>(null)
  const [resetConfirmOpen, setResetConfirmOpen] = useState(false)
  const [resetting, setResetting] = useState(false)
  const me = useQuery({ queryKey: ['me'], queryFn: getMe, retry: false })
  const project = useQuery({
    queryKey: ['director-project', projectId],
    queryFn: () =>
      api<DirectorProject>(`/api/v1/director-projects/${projectId}`),
  })
  useEffect(() => {
    if (project.data) revisionRef.current = project.data.revision
  }, [project.data])

  const source = useMemo(() => {
    if (typeof window === 'undefined') return '/director-desk/'
    const params = new URLSearchParams({
      instanceId: projectId,
      hostOrigin: window.location.origin,
      theme: 'dark',
      embedded: '1',
    })
    return `/director-desk/?${params}`
  }, [projectId])

  const post = useCallback(
    (type: string, payload: Record<string, unknown> = {}) => {
      iframeRef.current?.contentWindow?.postMessage(
        { type, payload },
        window.location.origin,
      )
    },
    [],
  )

  const publishSaveState = useCallback(
    (state: SaveState, message: string) => {
      setSaveState(state)
      setSaveMessage(message)
      post('storyai:director-desk-save-state', {
        state,
        message,
        revision: revisionRef.current,
      })
    },
    [post],
  )

  const flushSave = useCallback(async () => {
    if (
      savingRef.current ||
      conflictRef.current ||
      pendingDocumentRef.current === null
    )
      return
    const document = pendingDocumentRef.current
    pendingDocumentRef.current = null
    savingRef.current = true
    let retryAfterFailure = false
    publishSaveState('saving', '正在保存')
    try {
      const result = await api<{ revision: number }>(
        `/api/v1/director-projects/${projectId}/document`,
        {
          method: 'PUT',
          body: JSON.stringify({
            document,
            expected_revision: revisionRef.current,
          }),
        },
      )
      revisionRef.current = result.revision
      publishSaveState('saved', '已保存')
    } catch (error) {
      if (error instanceof APIError && error.status === 409) {
        conflictRef.current = true
        pendingDocumentRef.current = null
        publishSaveState('conflict', '检测到另一处更新，请刷新或另建项目')
      } else {
        if (pendingDocumentRef.current === null)
          pendingDocumentRef.current = document
        retryAfterFailure = true
        publishSaveState(
          'error',
          error instanceof Error ? error.message : '保存失败',
        )
      }
    } finally {
      savingRef.current = false
      if (pendingDocumentRef.current !== null && !conflictRef.current) {
        if (retryAfterFailure) {
          saveTimerRef.current = window.setTimeout(() => {
            saveTimerRef.current = null
            void flushSave()
          }, 2000)
        } else {
          void flushSave()
        }
      }
    }
  }, [projectId, publishSaveState])

  const scheduleSave = useCallback(
    (document: unknown) => {
      if (conflictRef.current) return
      pendingDocumentRef.current = document
      publishSaveState('saving', '等待保存')
      if (saveTimerRef.current !== null)
        window.clearTimeout(saveTimerRef.current)
      saveTimerRef.current = window.setTimeout(() => {
        saveTimerRef.current = null
        void flushSave()
      }, 1000)
    },
    [flushSave, publishSaveState],
  )

  const sendSession = useCallback(() => {
    if (
      !iframeReadyRef.current ||
      !project.data ||
      !me.data ||
      sessionSentRef.current
    )
      return
    sessionSentRef.current = true
    post('storyai:director-desk-session', {
      instanceId: project.data.id,
      tenantScope: me.data.user.id,
      theme: 'dark',
      embedded: true,
      projectName: project.data.name,
      projectDocument: project.data.document ?? null,
      documentRevision: revisionRef.current,
    })
  }, [me.data, post, project.data])

  useEffect(() => {
    iframeReadyRef.current = false
    sessionSentRef.current = false
    conflictRef.current = false
    pendingDocumentRef.current = null
    setSessionError(null)
  }, [projectId])

  useEffect(() => {
    sendSession()
  }, [sendSession])

  const retrySession = useCallback(() => {
    conflictRef.current = false
    sessionSentRef.current = false
    setSessionError(null)
    sendSession()
  }, [sendSession])

  const downloadOriginalDocument = useCallback(() => {
    if (!project.data) return
    const serialized = JSON.stringify(project.data.document ?? null, null, 2)
    const url = URL.createObjectURL(
      new Blob([serialized], { type: 'application/json;charset=utf-8' }),
    )
    const link = document.createElement('a')
    link.href = url
    link.download = `${project.data.name.replace(/[^\p{L}\p{N}._-]+/gu, '-') || 'director-project'}.json`
    document.body.append(link)
    link.click()
    link.remove()
    window.setTimeout(() => URL.revokeObjectURL(url), 0)
  }, [project.data])

  const resetProject = useCallback(async () => {
    if (!project.data || resetting) return
    setResetting(true)
    try {
      const result = await api<{ revision: number }>(
        `/api/v1/director-projects/${projectId}/reset`,
        {
          method: 'POST',
          body: JSON.stringify({ expected_revision: revisionRef.current }),
        },
      )
      revisionRef.current = result.revision
      queryClient.setQueryData<DirectorProject>(
        ['director-project', projectId],
        (current) =>
          current
            ? { ...current, document: null, revision: result.revision }
            : current,
      )
      conflictRef.current = false
      sessionSentRef.current = false
      setSessionError(null)
      setResetConfirmOpen(false)
    } catch (error) {
      publishSaveState(
        error instanceof APIError && error.status === 409
          ? 'conflict'
          : 'error',
        error instanceof Error ? error.message : '重置工程失败',
      )
    } finally {
      setResetting(false)
    }
  }, [project.data, projectId, publishSaveState, queryClient, resetting])

  const uploadCaptures = useCallback(
    async (payload: unknown) => {
      if (
        !isRecord(payload) ||
        !Array.isArray(payload.captures) ||
        payload.captures.length === 0
      )
        return
      const files = payload.captures.map((capture, index) =>
        captureFile(capture, `director-capture-${index + 1}.png`),
      )
      let cursor = 0
      let succeeded = 0
      let failed = 0
      post('storyai:director-desk-captures-status', {
        state: 'uploading',
        message: `正在置入 ${files.length} 张截图`,
      })
      const worker = async () => {
        while (cursor < files.length) {
          const index = cursor++
          try {
            const asset = await uploadCapture(files[index])
            mergeWallAsset(queryClient, asset)
            succeeded += 1
          } catch {
            failed += 1
          }
        }
      }
      await Promise.all(
        Array.from({ length: Math.min(3, files.length) }, worker),
      )
      await queryClient.invalidateQueries({ queryKey: ['assets'] })
      const message = failed
        ? `已置入 ${succeeded} 张，${failed} 张失败`
        : `已置入灵感墙 · ${succeeded} 张`
      post('storyai:director-desk-captures-status', {
        state: failed ? 'partial' : 'succeeded',
        message,
        succeeded,
        failed,
      })
    },
    [post, queryClient],
  )

  useEffect(() => {
    const handleMessage = (event: MessageEvent<HostMessage>) => {
      if (
        event.origin !== window.location.origin ||
        event.source !== iframeRef.current?.contentWindow
      )
        return
      if (typeof event.data.type !== 'string') return
      const payload = isRecord(event.data.payload) ? event.data.payload : {}
      switch (event.data.type) {
        case 'storyai:director-desk-ready':
          iframeReadyRef.current = true
          sendSession()
          break
        case 'storyai:director-desk-session-error': {
          if (saveTimerRef.current !== null) {
            window.clearTimeout(saveTimerRef.current)
            saveTimerRef.current = null
          }
          pendingDocumentRef.current = null
          conflictRef.current = true
          setSessionError({
            code:
              typeof payload.code === 'string'
                ? payload.code
                : 'invalid-project-document',
            message:
              typeof payload.message === 'string'
                ? payload.message
                : '工程内容无法读取',
          })
          publishSaveState('error', '工程已进入只读恢复模式')
          break
        }
        case 'storyai:director-desk-close':
          void navigate({ to: '/app/director' })
          break
        case 'storyai:director-desk-project-changed':
          if (isRecord(payload.document)) scheduleSave(payload.document)
          break
        case 'storyai:director-desk-rename': {
          const name =
            typeof payload.name === 'string' ? payload.name.trim() : ''
          if (name)
            void api(`/api/v1/director-projects/${projectId}`, {
              method: 'PATCH',
              body: JSON.stringify({ name }),
            })
              .then(() =>
                queryClient.invalidateQueries({
                  queryKey: ['director-projects'],
                }),
              )
              .catch((error: Error) => publishSaveState('error', error.message))
          break
        }
        case 'storyai:director-desk-new':
          void api<DirectorProject>('/api/v1/director-projects', {
            method: 'POST',
            body: JSON.stringify({ name: '未命名项目' }),
          })
            .then((created) =>
              navigate({
                to: '/app/director/$projectId',
                params: { projectId: created.id },
              }),
            )
            .catch((error: Error) => publishSaveState('error', error.message))
          break
        case 'storyai:director-desk-captures-sent':
          void uploadCaptures(payload).catch((error: Error) =>
            post('storyai:director-desk-captures-status', {
              state: 'failed',
              message: error.message,
            }),
          )
          break
      }
    }
    window.addEventListener('message', handleMessage)
    return () => window.removeEventListener('message', handleMessage)
  }, [
    navigate,
    post,
    projectId,
    publishSaveState,
    queryClient,
    scheduleSave,
    sendSession,
    uploadCaptures,
  ])

  useEffect(
    () => () => {
      if (saveTimerRef.current !== null)
        window.clearTimeout(saveTimerRef.current)
      if (pendingDocumentRef.current !== null && !conflictRef.current)
        void flushSave()
    },
    [flushSave],
  )

  return (
    <AppShell>
      <main
        className={`director-editor-page${sessionError ? ' is-recovering' : ''}`}
      >
        {project.isLoading && (
          <div className="director-editor-loading">
            <span className="spinner" />
            正在载入导演台…
          </div>
        )}
        {project.isError && (
          <div className="director-editor-loading">{project.error.message}</div>
        )}
        {project.data && (
          <iframe
            key={projectId}
            ref={iframeRef}
            title={`${project.data.name} · 导演台`}
            src={source}
            onLoad={() => {
              iframeReadyRef.current = true
              sendSession()
            }}
            allow="fullscreen"
            allowFullScreen
          />
        )}
        {sessionError && project.data && (
          <section
            className="director-recovery-panel"
            aria-labelledby="director-recovery-title"
          >
            <p className="eyebrow">RECOVERY MODE</p>
            <h1 id="director-recovery-title">工程内容无法读取</h1>
            <p>{sessionError.message}</p>
            <p className="director-recovery-note">
              原始数据保持不变，自动保存已停止。你可以重试载入、下载原始
              JSON，或明确重置为空工程。
            </p>
            <div className="director-recovery-actions">
              <button
                type="button"
                className="secondary-button"
                onClick={retrySession}
              >
                重试载入
              </button>
              <button
                type="button"
                className="secondary-button"
                onClick={downloadOriginalDocument}
              >
                下载原始 JSON
              </button>
              <button
                type="button"
                className="danger-button"
                onClick={() => setResetConfirmOpen(true)}
              >
                重置为空工程
              </button>
            </div>
          </section>
        )}
        <span
          className={`director-host-save-state is-${saveState}`}
          aria-live="polite"
        >
          {saveMessage}
        </span>
      </main>
      <ConfirmDialog
        open={resetConfirmOpen}
        title="重置导演台工程"
        description="当前工程内容将被替换为空工程。建议先下载原始 JSON；此操作无法撤销。"
        confirmLabel="确认重置"
        dangerous
        busy={resetting}
        onCancel={() => setResetConfirmOpen(false)}
        onConfirm={() => void resetProject()}
      />
    </AppShell>
  )
}
