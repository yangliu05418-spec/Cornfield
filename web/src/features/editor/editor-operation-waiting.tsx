import { Sparkles } from 'lucide-react'

type WaitEstimate = {
  lower_seconds: number
  upper_seconds: number
  sample_size: number
  basis: string
}

export function EditorOperationWaiting({
  status,
  message,
  elapsed,
  estimate,
}: {
  status?: string
  message?: string
  elapsed: number
  estimate?: WaitEstimate
}) {
  const messages: Record<string, string> = {
    queued: '等待处理资源',
    dispatched: '等待处理资源',
    snapshotting: '识别画面结构',
    submitting: '区分主体与背景',
    provider_processing: '整理图层关系',
    ingesting: '生成图层预览',
  }
  const estimateText = estimate
    ? elapsed > estimate.upper_seconds
      ? '比预计稍久，任务仍在后台处理'
      : `预计 ${estimate.lower_seconds}–${estimate.upper_seconds} 秒，已等待 ${elapsed} 秒`
    : `已等待 ${elapsed} 秒`
  return (
    <div className="decomposition-wait" aria-live="polite">
      <div className="decomposition-scan" />
      <Sparkles size={24} />
      <strong>{message || messages[status ?? ''] || '准备透明图层'}</strong>
      <span>{estimateText}</span>
      <small>可以返回，任务会在后台继续</small>
    </div>
  )
}
