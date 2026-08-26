import { useEffect, useId, useMemo, useRef } from 'react'

type Selection = { start: number; end: number }

export function mapRefinedSelection(
  before: string,
  after: string,
  selection: Selection,
): Selection {
  let prefix = 0
  const prefixLimit = Math.min(before.length, after.length)
  while (prefix < prefixLimit && before[prefix] === after[prefix]) prefix++

  let suffix = 0
  while (
    suffix < before.length - prefix &&
    suffix < after.length - prefix &&
    before[before.length - 1 - suffix] === after[after.length - 1 - suffix]
  ) {
    suffix++
  }

  const sourceChangeEnd = before.length - suffix
  const targetChangeEnd = after.length - suffix
  const mapPosition = (position: number) => {
    if (position <= prefix) return position
    if (position >= sourceChangeEnd)
      return Math.max(
        0,
        Math.min(after.length, position + after.length - before.length),
      )
    return targetChangeEnd
  }

  return {
    start: mapPosition(selection.start),
    end: mapPosition(selection.end),
  }
}

export function promptChangeParts(before: string, after: string) {
  let prefix = 0
  const prefixLimit = Math.min(before.length, after.length)
  while (prefix < prefixLimit && before[prefix] === after[prefix]) prefix++

  let suffix = 0
  while (
    suffix < before.length - prefix &&
    suffix < after.length - prefix &&
    before[before.length - 1 - suffix] === after[after.length - 1 - suffix]
  ) {
    suffix++
  }

  return {
    before: [
      before.slice(0, prefix),
      before.slice(prefix, before.length - suffix),
      suffix ? before.slice(before.length - suffix) : '',
    ] as const,
    after: [
      after.slice(0, prefix),
      after.slice(prefix, after.length - suffix),
      suffix ? after.slice(after.length - suffix) : '',
    ] as const,
  }
}

export function PromptRefinerReview({
  open,
  before,
  after,
  onClose,
}: {
  open: boolean
  before: string
  after: string
  onClose: () => void
}) {
  const ref = useRef<HTMLDialogElement>(null)
  const titleID = useId()
  const parts = useMemo(() => promptChangeParts(before, after), [after, before])

  useEffect(() => {
    const dialog = ref.current
    if (!dialog) return
    if (open && !dialog.open) dialog.showModal()
    if (!open && dialog.open) dialog.close()
  }, [open])

  return (
    <dialog
      ref={ref}
      className="prompt-refiner-review"
      aria-labelledby={titleID}
      onCancel={(event) => {
        event.preventDefault()
        onClose()
      }}
      onClose={() => {
        if (open) onClose()
      }}
    >
      <header>
        <div>
          <p className="eyebrow">PROMPT REFINED</p>
          <h2 id={titleID}>查看提示词修改</h2>
        </div>
        <button type="button" aria-label="关闭修改对比" onClick={onClose}>
          关闭
        </button>
      </header>
      <div className="prompt-refiner-review-grid">
        <section aria-label="修改前">
          <span>修改前</span>
          <p>
            {parts.before[0]}
            {parts.before[1] && <del>{parts.before[1]}</del>}
            {parts.before[2]}
          </p>
        </section>
        <section aria-label="修改后">
          <span>修改后</span>
          <p>
            {parts.after[0]}
            {parts.after[1] && <ins>{parts.after[1]}</ins>}
            {parts.after[2]}
          </p>
        </section>
      </div>
    </dialog>
  )
}
