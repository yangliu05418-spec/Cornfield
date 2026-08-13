import { Sparkles } from 'lucide-react'

export function EditorOperationWaiting({
  status,
  message,
  elapsed,
}: {
  status?: string
  message?: string
  elapsed: number
}) {
  const messages: Record<string, string> = {
    queued: '等待处理资源',
    dispatched: '等待处理资源',
    snapshotting: '识别画面结构',
    submitting: '区分主体与背景',
    provider_processing: '整理图层关系',
    ingesting: '生成图层预览',
  }
  return (
    <div className="decomposition-wait" aria-live="polite">
      <div className="decomposition-scan" />
      <Sparkles size={24} />
      <strong>{message || messages[status ?? ''] || '准备透明图层'}</strong>
      <span>{elapsed} 秒 · 可以返回，任务会在后台继续</span>
    </div>
  )
}
