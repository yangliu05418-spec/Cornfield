import { useEffect, useId, useRef } from 'react'

export function ConfirmDialog({
  open,
  title,
  description,
  confirmLabel = '确认',
  dangerous = false,
  busy = false,
  onCancel,
  onConfirm,
}: {
  open: boolean
  title: string
  description: string
  confirmLabel?: string
  dangerous?: boolean
  busy?: boolean
  onCancel: () => void
  onConfirm: () => void
}) {
  const ref = useRef<HTMLDialogElement>(null)
  const titleID = useId()
  const descriptionID = useId()
  useEffect(() => {
    const dialog = ref.current
    if (!dialog) return
    if (open && !dialog.open) dialog.showModal()
    if (!open && dialog.open) dialog.close()
  }, [open])
  return (
    <dialog
      ref={ref}
      className="confirm-dialog"
      aria-labelledby={titleID}
      aria-describedby={descriptionID}
      onCancel={(event) => {
        event.preventDefault()
        if (!busy) onCancel()
      }}
      onClose={() => {
        if (open && !busy) onCancel()
      }}
    >
      <p className="eyebrow">CONFIRM ACTION</p>
      <h2 id={titleID}>{title}</h2>
      <p id={descriptionID}>{description}</p>
      <div className="confirm-dialog-actions">
        <button
          type="button"
          className="secondary-button"
          disabled={busy}
          onClick={onCancel}
          autoFocus
        >
          取消
        </button>
        <button
          type="button"
          className={dangerous ? 'danger-button' : 'primary-button'}
          disabled={busy}
          onClick={onConfirm}
        >
          {busy ? '处理中…' : confirmLabel}
        </button>
      </div>
    </dialog>
  )
}
